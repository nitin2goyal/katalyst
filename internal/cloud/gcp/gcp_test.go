package gcp

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// rewriteTransport redirects all HTTP requests to a test server while
// preserving the original path and query parameters.
// ---------------------------------------------------------------------------

type rewriteTransport struct {
	base    http.RoundTripper
	baseURL string // e.g. "http://127.0.0.1:PORT"
}

func (rt *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	parsed, _ := url.Parse(rt.baseURL)
	req.URL.Scheme = parsed.Scheme
	req.URL.Host = parsed.Host
	transport := rt.base
	if transport == nil {
		transport = http.DefaultTransport
	}
	return transport.RoundTrip(req)
}

// newRewriteClient returns an *http.Client that redirects all requests to the
// given httptest.Server while preserving paths and query params.
func newRewriteClient(ts *httptest.Server) *http.Client {
	return &http.Client{
		Transport: &rewriteTransport{
			base:    ts.Client().Transport,
			baseURL: ts.URL,
		},
	}
}

// ---------------------------------------------------------------------------
// 1. Test extractGCPFamily
// ---------------------------------------------------------------------------

func TestExtractGCPFamily(t *testing.T) {
	tests := []struct {
		name         string
		instanceType string
		want         string
	}{
		{name: "n2-standard-4", instanceType: "n2-standard-4", want: "n2"},
		{name: "e2-medium", instanceType: "e2-medium", want: "e2"},
		{name: "c2d-standard-8", instanceType: "c2d-standard-8", want: "c2d"},
		{name: "single_component", instanceType: "custom", want: "custom"},
		{name: "a2-highgpu-1g", instanceType: "a2-highgpu-1g", want: "a2"},
		{name: "n2d-highmem-16", instanceType: "n2d-highmem-16", want: "n2d"},
		{name: "t2a-standard-1", instanceType: "t2a-standard-1", want: "t2a"},
		{name: "empty_string", instanceType: "", want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractGCPFamily(tc.instanceType)
			if got != tc.want {
				t.Errorf("extractGCPFamily(%q) = %q, want %q", tc.instanceType, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 2. Test estimateSpotDiscount
// ---------------------------------------------------------------------------

func TestEstimateSpotDiscount(t *testing.T) {
	tests := []struct {
		family string
		want   float64
	}{
		{"e2", 0.69},
		{"n1", 0.80},
		{"c3", 0.65},
		{"a2", 0.60},
		{"c4", 0.63},
		{"n4", 0.67},
		{"n2", 0.69},
		{"n2d", 0.69},
		{"c2", 0.69},
		{"c2d", 0.69},
		{"c3d", 0.65},
		{"m1", 0.69},
		{"m2", 0.69},
		{"m3", 0.65},
		{"a3", 0.60},
		{"g2", 0.60},
		{"t2a", 0.69},
		{"t2d", 0.69},
		{"h3", 0.65},
		// Unknown families should return the default of 0.69.
		{"unknown", 0.69},
		{"z9", 0.69},
		{"", 0.69},
	}

	for _, tc := range tests {
		t.Run(tc.family, func(t *testing.T) {
			got := estimateSpotDiscount(tc.family)
			if got != tc.want {
				t.Errorf("estimateSpotDiscount(%q) = %v, want %v", tc.family, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 3. Test estimateGCPPreemptionRate
// ---------------------------------------------------------------------------

func TestEstimateGCPPreemptionRate(t *testing.T) {
	tests := []struct {
		family string
		want   float64
	}{
		{"a2", 15.0},
		{"g2", 15.0},
		{"a3", 15.0},
		{"n1", 12.0},
		{"e2", 10.0},
		{"n2", 7.0},
		{"c2", 7.0},
		{"c3", 7.0},
		{"n4", 7.0},
		// Unknown families should return the default of 7.0.
		{"unknown", 7.0},
		{"z9", 7.0},
	}

	for _, tc := range tests {
		t.Run(tc.family, func(t *testing.T) {
			got := estimateGCPPreemptionRate(tc.family)
			if got != tc.want {
				t.Errorf("estimateGCPPreemptionRate(%q) = %v, want %v", tc.family, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 4. Test mapCommitmentTypeToFamily
// ---------------------------------------------------------------------------

func TestMapCommitmentTypeToFamily(t *testing.T) {
	tests := []struct {
		commitmentType string
		want           string
	}{
		{"COMPUTE_OPTIMIZED", "c2"},
		{"COMPUTE_OPTIMIZED_C2D", "c2d"},
		{"GENERAL_PURPOSE", "n1"},
		{"GENERAL_PURPOSE_N2", "n2"},
		{"GENERAL_PURPOSE_N2D", "n2d"},
		{"GENERAL_PURPOSE_E2", "e2"},
		{"GENERAL_PURPOSE_T2D", "t2d"},
		{"GENERAL_PURPOSE_T2A", "t2a"},
		{"MEMORY_OPTIMIZED", "m2"},
		{"ACCELERATOR_OPTIMIZED", "a2"},
		{"COMPUTE_OPTIMIZED_C3", "c3"},
		{"COMPUTE_OPTIMIZED_C3D", "c3d"},
		{"GENERAL_PURPOSE_N4", "n4"},
		{"COMPUTE_OPTIMIZED_C4", "c4"},
		{"MEMORY_OPTIMIZED_M3", "m3"},
		// Unknown types should return the default "n1".
		{"UNKNOWN_TYPE", "n1"},
		{"", "n1"},
		{"something_random", "n1"},
		// Case insensitivity: the function calls strings.ToUpper internally.
		{"compute_optimized", "c2"},
		{"general_purpose_n2", "n2"},
	}

	for _, tc := range tests {
		t.Run(tc.commitmentType, func(t *testing.T) {
			got := mapCommitmentTypeToFamily(tc.commitmentType)
			if got != tc.want {
				t.Errorf("mapCommitmentTypeToFamily(%q) = %q, want %q", tc.commitmentType, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 5. Test mapCommitment
// ---------------------------------------------------------------------------

func TestMapCommitment(t *testing.T) {
	t.Run("1-year CUD with VCPU and MEMORY", func(t *testing.T) {
		c := gceCommitment{
			ID:           "123",
			Name:         "cud-1yr",
			Status:       "ACTIVE",
			Plan:         "TWELVE_MONTH",
			EndTimestamp: "2026-06-15T00:00:00Z",
			Type:         "GENERAL_PURPOSE_N2",
			Resources: []gceCommitmentResource{
				{Type: "VCPU", Amount: "8"},
				{Type: "MEMORY", Amount: "32768"}, // 32 GB in MB
			},
		}

		result, err := mapCommitment(c, "us-central1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if result.ID != "123" {
			t.Errorf("ID = %q, want %q", result.ID, "123")
		}
		if result.Type != "cud" {
			t.Errorf("Type = %q, want %q", result.Type, "cud")
		}
		if result.InstanceFamily != "n2" {
			t.Errorf("InstanceFamily = %q, want %q", result.InstanceFamily, "n2")
		}
		if result.Region != "us-central1" {
			t.Errorf("Region = %q, want %q", result.Region, "us-central1")
		}
		if result.Count != 8 {
			t.Errorf("Count = %d, want %d", result.Count, 8)
		}
		if result.Status != "active" {
			t.Errorf("Status = %q, want %q", result.Status, "active")
		}

		// For 1-year CUD, discount factor is 0.63.
		// On-demand cost = cpuPerHour * vcpus + memPerHour * memGB
		n2Pricing := gcpFamilyPricing["n2"]
		memGB := 32768.0 / 1024.0 // 32 GB
		expectedOnDemand := n2Pricing.cpuPerHour*8 + n2Pricing.memPerHour*memGB
		expectedHourly := expectedOnDemand * 0.63

		if math.Abs(result.OnDemandCostUSD-expectedOnDemand) > 0.0001 {
			t.Errorf("OnDemandCostUSD = %v, want ~%v", result.OnDemandCostUSD, expectedOnDemand)
		}
		if math.Abs(result.HourlyCostUSD-expectedHourly) > 0.0001 {
			t.Errorf("HourlyCostUSD = %v, want ~%v", result.HourlyCostUSD, expectedHourly)
		}
	})

	t.Run("3-year CUD has 0.45 discount factor", func(t *testing.T) {
		c := gceCommitment{
			ID:           "456",
			Name:         "cud-3yr",
			Status:       "ACTIVE",
			Plan:         "THIRTY_SIX_MONTH",
			EndTimestamp: "2028-01-01T00:00:00Z",
			Type:         "GENERAL_PURPOSE_N2",
			Resources: []gceCommitmentResource{
				{Type: "VCPU", Amount: "4"},
				{Type: "MEMORY", Amount: "16384"}, // 16 GB in MB
			},
		}

		result, err := mapCommitment(c, "us-central1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		n2Pricing := gcpFamilyPricing["n2"]
		memGB := 16384.0 / 1024.0
		expectedOnDemand := n2Pricing.cpuPerHour*4 + n2Pricing.memPerHour*memGB
		expectedHourly := expectedOnDemand * 0.45

		if math.Abs(result.HourlyCostUSD-expectedHourly) > 0.0001 {
			t.Errorf("HourlyCostUSD = %v, want ~%v (3-year discount)", result.HourlyCostUSD, expectedHourly)
		}
	})

	t.Run("valid end timestamp parsed correctly", func(t *testing.T) {
		endTime := "2027-03-15T12:30:00Z"
		c := gceCommitment{
			ID:           "789",
			Name:         "cud-expiry",
			Status:       "ACTIVE",
			Plan:         "TWELVE_MONTH",
			EndTimestamp: endTime,
			Type:         "GENERAL_PURPOSE",
			Resources: []gceCommitmentResource{
				{Type: "VCPU", Amount: "2"},
				{Type: "MEMORY", Amount: "8192"},
			},
		}

		result, err := mapCommitment(c, "us-central1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		expected, _ := time.Parse(time.RFC3339, endTime)
		if !result.ExpiresAt.Equal(expected) {
			t.Errorf("ExpiresAt = %v, want %v", result.ExpiresAt, expected)
		}
	})

	t.Run("invalid end timestamp returns error", func(t *testing.T) {
		c := gceCommitment{
			ID:           "bad",
			Name:         "bad-ts",
			Status:       "ACTIVE",
			Plan:         "TWELVE_MONTH",
			EndTimestamp: "not-a-timestamp",
			Type:         "GENERAL_PURPOSE",
			Resources: []gceCommitmentResource{
				{Type: "VCPU", Amount: "2"},
			},
		}

		_, err := mapCommitment(c, "us-central1")
		if err == nil {
			t.Fatal("expected error for invalid timestamp, got nil")
		}
	})

	t.Run("GENERAL_PURPOSE maps to n1", func(t *testing.T) {
		c := gceCommitment{
			ID:           "gp",
			Name:         "gp-cud",
			Status:       "ACTIVE",
			Plan:         "TWELVE_MONTH",
			EndTimestamp: "2027-01-01T00:00:00Z",
			Type:         "GENERAL_PURPOSE",
			Resources: []gceCommitmentResource{
				{Type: "VCPU", Amount: "4"},
				{Type: "MEMORY", Amount: "8192"},
			},
		}

		result, err := mapCommitment(c, "us-east1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.InstanceFamily != "n1" {
			t.Errorf("InstanceFamily = %q, want %q", result.InstanceFamily, "n1")
		}
	})

	t.Run("COMPUTE_OPTIMIZED maps to c2", func(t *testing.T) {
		c := gceCommitment{
			ID:           "co",
			Name:         "co-cud",
			Status:       "ACTIVE",
			Plan:         "TWELVE_MONTH",
			EndTimestamp: "2027-01-01T00:00:00Z",
			Type:         "COMPUTE_OPTIMIZED",
			Resources: []gceCommitmentResource{
				{Type: "VCPU", Amount: "16"},
				{Type: "MEMORY", Amount: "65536"},
			},
		}

		result, err := mapCommitment(c, "us-west1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.InstanceFamily != "c2" {
			t.Errorf("InstanceFamily = %q, want %q", result.InstanceFamily, "c2")
		}

		// Verify hourly cost includes both CPU and memory components.
		c2Pricing := gcpFamilyPricing["c2"]
		memGB := 65536.0 / 1024.0
		expectedOnDemand := c2Pricing.cpuPerHour*16 + c2Pricing.memPerHour*memGB
		expectedHourly := expectedOnDemand * 0.63

		if math.Abs(result.HourlyCostUSD-expectedHourly) > 0.0001 {
			t.Errorf("HourlyCostUSD = %v, want ~%v", result.HourlyCostUSD, expectedHourly)
		}
		if result.HourlyCostUSD <= 0 {
			t.Error("HourlyCostUSD should be positive")
		}
		if result.OnDemandCostUSD <= 0 {
			t.Error("OnDemandCostUSD should be positive")
		}
	})

	t.Run("empty end timestamp does not error", func(t *testing.T) {
		c := gceCommitment{
			ID:     "noend",
			Name:   "no-end-cud",
			Status: "ACTIVE",
			Plan:   "TWELVE_MONTH",
			Type:   "GENERAL_PURPOSE",
			Resources: []gceCommitmentResource{
				{Type: "VCPU", Amount: "2"},
				{Type: "MEMORY", Amount: "4096"},
			},
		}

		result, err := mapCommitment(c, "us-central1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.ExpiresAt.IsZero() {
			t.Errorf("ExpiresAt should be zero time for empty end timestamp, got %v", result.ExpiresAt)
		}
	})

	t.Run("status is lowercased", func(t *testing.T) {
		c := gceCommitment{
			ID:           "status-test",
			Name:         "status-cud",
			Status:       "ACTIVE",
			Plan:         "TWELVE_MONTH",
			EndTimestamp: "2027-01-01T00:00:00Z",
			Type:         "GENERAL_PURPOSE",
			Resources: []gceCommitmentResource{
				{Type: "VCPU", Amount: "2"},
			},
		}

		result, err := mapCommitment(c, "us-central1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Status != "active" {
			t.Errorf("Status = %q, want %q", result.Status, "active")
		}
	})
}

// ---------------------------------------------------------------------------
// 6. Test fetchGCPPreemptibleRates with httptest
// ---------------------------------------------------------------------------

// buildBillingCatalogResponse creates a JSON billing catalog response body.
func buildBillingCatalogResponse(skus []billingCatalogSKU, nextPageToken string) []byte {
	resp := billingCatalogResponse{
		Skus:          skus,
		NextPageToken: nextPageToken,
	}
	data, _ := json.Marshal(resp)
	return data
}

// makeSKU is a helper to build a billingCatalogSKU for testing.
func makeSKU(description, resourceGroup, usageType string, regions []string, units string, nanos int64) billingCatalogSKU {
	return billingCatalogSKU{
		SkuID:       "test-sku",
		Description: description,
		Category: skuCategory{
			ResourceFamily: "Compute",
			ResourceGroup:  resourceGroup,
			UsageType:      usageType,
		},
		ServiceRegions: regions,
		PricingInfo: []skuPricingInfo{
			{
				PricingExpression: skuPricingExpression{
					UsageUnit: "h",
					TieredRates: []skuTieredRate{
						{
							StartUsageAmount: 0,
							UnitPrice: skuUnitPrice{
								CurrencyCode: "USD",
								Units:        units,
								Nanos:        nanos,
							},
						},
					},
				},
			},
		},
	}
}

func TestFetchGCPPreemptibleRates(t *testing.T) {
	t.Run("complete N2 family with CPU and RAM", func(t *testing.T) {
		skus := []billingCatalogSKU{
			makeSKU(
				"N2 Instance Core running in Americas",
				"N2", "Preemptible",
				[]string{"us-central1"},
				"0", 7000000, // $0.007 per vCPU-hour
			),
			makeSKU(
				"N2 Instance Ram running in Americas",
				"N2", "Preemptible",
				[]string{"us-central1"},
				"0", 900000, // $0.0009 per GB-hour
			),
		}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write(buildBillingCatalogResponse(skus, ""))
		}))
		defer server.Close()

		rates, err := fetchGCPPreemptibleRates(context.Background(), "us-central1", newRewriteClient(server))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		n2, ok := rates["n2"]
		if !ok {
			t.Fatal("expected 'n2' in rates map, not found")
		}
		if math.Abs(n2.cpuPerHour-0.007) > 0.0001 {
			t.Errorf("n2.cpuPerHour = %v, want 0.007", n2.cpuPerHour)
		}
		if math.Abs(n2.memPerHour-0.0009) > 0.0001 {
			t.Errorf("n2.memPerHour = %v, want 0.0009", n2.memPerHour)
		}
	})

	t.Run("incomplete family filtered out (only CPU, no RAM)", func(t *testing.T) {
		skus := []billingCatalogSKU{
			makeSKU(
				"C3 Instance Core running in Americas",
				"C3", "Preemptible",
				[]string{"us-central1"},
				"0", 8000000,
			),
			// No corresponding RAM SKU for C3.
		}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write(buildBillingCatalogResponse(skus, ""))
		}))
		defer server.Close()

		rates, err := fetchGCPPreemptibleRates(context.Background(), "us-central1", newRewriteClient(server))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if _, ok := rates["c3"]; ok {
			t.Error("c3 should be filtered out because it has no RAM pricing")
		}
		if len(rates) != 0 {
			t.Errorf("expected empty rates map, got %d entries", len(rates))
		}
	})

	t.Run("empty response returns empty map", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write(buildBillingCatalogResponse(nil, ""))
		}))
		defer server.Close()

		rates, err := fetchGCPPreemptibleRates(context.Background(), "us-central1", newRewriteClient(server))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(rates) != 0 {
			t.Errorf("expected empty rates, got %d entries", len(rates))
		}
	})

	t.Run("non-matching region is filtered", func(t *testing.T) {
		skus := []billingCatalogSKU{
			makeSKU(
				"E2 Instance Core running in Europe",
				"E2", "Preemptible",
				[]string{"europe-west1"}, // NOT us-central1
				"0", 5000000,
			),
			makeSKU(
				"E2 Instance Ram running in Europe",
				"E2", "Preemptible",
				[]string{"europe-west1"},
				"0", 700000,
			),
		}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write(buildBillingCatalogResponse(skus, ""))
		}))
		defer server.Close()

		rates, err := fetchGCPPreemptibleRates(context.Background(), "us-central1", newRewriteClient(server))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(rates) != 0 {
			t.Errorf("expected empty rates when region doesn't match, got %d entries", len(rates))
		}
	})

	t.Run("OnDemand usage type is filtered out", func(t *testing.T) {
		skus := []billingCatalogSKU{
			makeSKU(
				"N2 Instance Core running in Americas",
				"N2", "OnDemand", // Not Preemptible
				[]string{"us-central1"},
				"0", 31611000,
			),
			makeSKU(
				"N2 Instance Ram running in Americas",
				"N2", "OnDemand",
				[]string{"us-central1"},
				"0", 4237000,
			),
		}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write(buildBillingCatalogResponse(skus, ""))
		}))
		defer server.Close()

		rates, err := fetchGCPPreemptibleRates(context.Background(), "us-central1", newRewriteClient(server))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(rates) != 0 {
			t.Errorf("expected empty rates for OnDemand SKUs, got %d entries", len(rates))
		}
	})

	t.Run("multiple families only returns complete ones", func(t *testing.T) {
		skus := []billingCatalogSKU{
			// Complete N2 family
			makeSKU("N2 Instance Core running in Americas", "N2", "Preemptible",
				[]string{"us-central1"}, "0", 7000000),
			makeSKU("N2 Instance Ram running in Americas", "N2", "Preemptible",
				[]string{"us-central1"}, "0", 900000),
			// Incomplete E2 family (only CPU)
			makeSKU("E2 Instance Core running in Americas", "E2", "Preemptible",
				[]string{"us-central1"}, "0", 5000000),
			// Complete C3 family
			makeSKU("C3 Instance Core running in Americas", "C3", "Preemptible",
				[]string{"us-central1"}, "0", 8000000),
			makeSKU("C3 Instance Ram running in Americas", "C3", "Preemptible",
				[]string{"us-central1"}, "0", 1100000),
		}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write(buildBillingCatalogResponse(skus, ""))
		}))
		defer server.Close()

		rates, err := fetchGCPPreemptibleRates(context.Background(), "us-central1", newRewriteClient(server))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(rates) != 2 {
			t.Fatalf("expected 2 complete families, got %d", len(rates))
		}
		if _, ok := rates["n2"]; !ok {
			t.Error("expected 'n2' in rates")
		}
		if _, ok := rates["c3"]; !ok {
			t.Error("expected 'c3' in rates")
		}
		if _, ok := rates["e2"]; ok {
			t.Error("e2 should be filtered out (incomplete)")
		}
	})

	t.Run("price with non-zero units field", func(t *testing.T) {
		skus := []billingCatalogSKU{
			makeSKU("N1 Instance Core running in Americas", "N1", "Preemptible",
				[]string{"us-central1"}, "1", 500000000), // $1.50
			makeSKU("N1 Instance Ram running in Americas", "N1", "Preemptible",
				[]string{"us-central1"}, "0", 200000000), // $0.20
		}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write(buildBillingCatalogResponse(skus, ""))
		}))
		defer server.Close()

		rates, err := fetchGCPPreemptibleRates(context.Background(), "us-central1", newRewriteClient(server))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		n1, ok := rates["n1"]
		if !ok {
			t.Fatal("expected 'n1' in rates")
		}
		if math.Abs(n1.cpuPerHour-1.5) > 0.0001 {
			t.Errorf("n1.cpuPerHour = %v, want 1.5", n1.cpuPerHour)
		}
		if math.Abs(n1.memPerHour-0.2) > 0.0001 {
			t.Errorf("n1.memPerHour = %v, want 0.2", n1.memPerHour)
		}
	})

	t.Run("HTTP error returns error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("internal server error"))
		}))
		defer server.Close()

		_, err := fetchGCPPreemptibleRates(context.Background(), "us-central1", newRewriteClient(server))
		if err == nil {
			t.Fatal("expected error for HTTP 500, got nil")
		}
	})

	t.Run("pagination across multiple pages", func(t *testing.T) {
		callCount := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			callCount++

			if r.URL.Query().Get("pageToken") == "" {
				// First page: CPU SKU for N4
				skus := []billingCatalogSKU{
					makeSKU("N4 Instance Core running in Americas", "N4", "Preemptible",
						[]string{"us-central1"}, "0", 6000000),
				}
				w.Write(buildBillingCatalogResponse(skus, "page2"))
			} else {
				// Second page: RAM SKU for N4
				skus := []billingCatalogSKU{
					makeSKU("N4 Instance Ram running in Americas", "N4", "Preemptible",
						[]string{"us-central1"}, "0", 800000),
				}
				w.Write(buildBillingCatalogResponse(skus, ""))
			}
		}))
		defer server.Close()

		rates, err := fetchGCPPreemptibleRates(context.Background(), "us-central1", newRewriteClient(server))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if callCount != 2 {
			t.Errorf("expected 2 HTTP calls for pagination, got %d", callCount)
		}

		n4, ok := rates["n4"]
		if !ok {
			t.Fatal("expected 'n4' in rates after pagination")
		}
		if math.Abs(n4.cpuPerHour-0.006) > 0.0001 {
			t.Errorf("n4.cpuPerHour = %v, want 0.006", n4.cpuPerHour)
		}
		if math.Abs(n4.memPerHour-0.0008) > 0.0001 {
			t.Errorf("n4.memPerHour = %v, want 0.0008", n4.memPerHour)
		}
	})
}

// ---------------------------------------------------------------------------
// 7. Test getRegionZones with httptest
// ---------------------------------------------------------------------------

func TestGetRegionZones(t *testing.T) {
	t.Run("extracts zone names from full resource paths", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Verify the request path contains the expected project and region.
			expectedPath := "/compute/v1/projects/test-project/regions/us-central1"
			if r.URL.Path != expectedPath {
				t.Errorf("unexpected request path: %s, want %s", r.URL.Path, expectedPath)
			}

			resp := map[string]interface{}{
				"zones": []string{
					"projects/test-project/zones/us-central1-a",
					"projects/test-project/zones/us-central1-b",
					"projects/test-project/zones/us-central1-c",
					"projects/test-project/zones/us-central1-f",
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		p := &Provider{
			project:    "test-project",
			region:     "us-central1",
			httpClient: newRewriteClient(server),
		}

		zones, err := p.getRegionZones(context.Background(), "us-central1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		expected := []string{"us-central1-a", "us-central1-b", "us-central1-c", "us-central1-f"}
		if len(zones) != len(expected) {
			t.Fatalf("got %d zones, want %d", len(zones), len(expected))
		}
		for i, z := range zones {
			if z != expected[i] {
				t.Errorf("zone[%d] = %q, want %q", i, z, expected[i])
			}
		}
	})

	t.Run("two zones returned", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resp := map[string]interface{}{
				"zones": []string{
					"projects/p/zones/us-central1-a",
					"projects/p/zones/us-central1-b",
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		p := &Provider{
			project:    "p",
			region:     "us-central1",
			httpClient: newRewriteClient(server),
		}

		zones, err := p.getRegionZones(context.Background(), "us-central1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		expected := []string{"us-central1-a", "us-central1-b"}
		if len(zones) != len(expected) {
			t.Fatalf("got %d zones, want %d", len(zones), len(expected))
		}
		for i, z := range zones {
			if z != expected[i] {
				t.Errorf("zone[%d] = %q, want %q", i, z, expected[i])
			}
		}
	})

	t.Run("HTTP error returns error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte("forbidden"))
		}))
		defer server.Close()

		p := &Provider{
			project:    "test-project",
			region:     "us-central1",
			httpClient: newRewriteClient(server),
		}

		_, err := p.getRegionZones(context.Background(), "us-central1")
		if err == nil {
			t.Fatal("expected error for HTTP 403, got nil")
		}
	})

	t.Run("empty zones list", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resp := map[string]interface{}{
				"zones": []string{},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		p := &Provider{
			project:    "test-project",
			region:     "us-central1",
			httpClient: newRewriteClient(server),
		}

		zones, err := p.getRegionZones(context.Background(), "us-central1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(zones) != 0 {
			t.Errorf("expected 0 zones, got %d", len(zones))
		}
	})
}

// ---------------------------------------------------------------------------
// Additional helper function tests
// ---------------------------------------------------------------------------

func TestFamilyFromResourceGroup(t *testing.T) {
	tests := []struct {
		resourceGroup string
		want          string
	}{
		{"N2Standard", "n2"},
		{"N2DStandard", "n2d"},
		{"E2Standard", "e2"},
		{"C3DHighmem", "c3d"},
		{"T2AStandard", "t2a"},
		{"C2Standard", "c2"},
		{"C2DStandard", "c2d"},
		{"N1Standard", "n1"},
		{"A2Standard", "a2"},
		{"G2Standard", "g2"},
		{"H3Standard", "h3"},
		{"M3Standard", "m3"},
		{"N4Standard", "n4"},
		{"C4Standard", "c4"},
		{"UnknownFamily", ""},
		{"", ""},
	}

	for _, tc := range tests {
		t.Run(tc.resourceGroup, func(t *testing.T) {
			got := familyFromResourceGroup(tc.resourceGroup)
			if got != tc.want {
				t.Errorf("familyFromResourceGroup(%q) = %q, want %q", tc.resourceGroup, got, tc.want)
			}
		})
	}
}

func TestResourceTypeFromDescription(t *testing.T) {
	tests := []struct {
		description string
		want        string
	}{
		{"N2 Instance Core running in Americas", "cpu"},
		{"N2 Instance Ram running in Americas", "ram"},
		{"E2 Instance VCPU running in US", "cpu"},
		{"C3 Instance CPU running in Europe", "cpu"},
		{"Some non-instance SKU", ""},
		{"Instance with no matching resource type", ""},
		{"Licensing Fee for something", ""},
		{"", ""},
	}

	for _, tc := range tests {
		t.Run(tc.description, func(t *testing.T) {
			got := resourceTypeFromDescription(tc.description)
			if got != tc.want {
				t.Errorf("resourceTypeFromDescription(%q) = %q, want %q", tc.description, got, tc.want)
			}
		})
	}
}

func TestExtractSKUPrice(t *testing.T) {
	t.Run("units=0 and nanos", func(t *testing.T) {
		sku := makeSKU("test", "N2", "Preemptible", nil, "0", 7000000)
		price := extractSKUPrice(sku)
		if math.Abs(price-0.007) > 0.0000001 {
			t.Errorf("price = %v, want 0.007", price)
		}
	})

	t.Run("non-zero units and nanos", func(t *testing.T) {
		sku := makeSKU("test", "N2", "Preemptible", nil, "2", 500000000)
		price := extractSKUPrice(sku)
		if math.Abs(price-2.5) > 0.0000001 {
			t.Errorf("price = %v, want 2.5", price)
		}
	})

	t.Run("empty pricing info", func(t *testing.T) {
		sku := billingCatalogSKU{}
		price := extractSKUPrice(sku)
		if price != 0 {
			t.Errorf("price = %v, want 0", price)
		}
	})

	t.Run("non-hourly usage unit", func(t *testing.T) {
		sku := billingCatalogSKU{
			PricingInfo: []skuPricingInfo{
				{
					PricingExpression: skuPricingExpression{
						UsageUnit: "GiBy.mo", // Not hourly
						TieredRates: []skuTieredRate{
							{UnitPrice: skuUnitPrice{Units: "0", Nanos: 1000000}},
						},
					},
				},
			},
		}
		price := extractSKUPrice(sku)
		if price != 0 {
			t.Errorf("price = %v, want 0 for non-hourly unit", price)
		}
	})
}

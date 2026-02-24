package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	intmetrics "github.com/koptimizer/koptimizer/internal/metrics"
	"github.com/koptimizer/koptimizer/internal/store"
	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
	"github.com/koptimizer/koptimizer/pkg/familylock"
)

var (
	pricingCache       map[string]*cloudprovider.PricingInfo // region -> pricing
	pricingMu          sync.RWMutex
	pricingUpdated     map[string]time.Time // region -> last updated

	machineTypeCache   map[string][]*cloudprovider.InstanceType // region -> types
	machineTypeMu      sync.RWMutex
	machineTypeUpdated map[string]time.Time // region -> last updated
)

func init() {
	pricingCache = make(map[string]*cloudprovider.PricingInfo)
	pricingUpdated = make(map[string]time.Time)
	machineTypeCache = make(map[string][]*cloudprovider.InstanceType)
	machineTypeUpdated = make(map[string]time.Time)
}

const (
	computeBaseURL = "https://compute.googleapis.com/compute/v1"
	cacheTTL       = 1 * time.Hour

	// Compute Engine service ID for the Cloud Billing Catalog API.
	computeEngineServiceID = "6F81-5844-456A"
	billingCatalogBaseURL  = "https://cloudbilling.googleapis.com/v1"
)

// componentPricing holds per-vCPU and per-GB-RAM hourly rates for a machine family.
type componentPricing struct {
	cpuPerHour float64 // per vCPU per hour
	memPerHour float64 // per GB RAM per hour
}

// gcpFamilyPricing maps machine family prefixes to their component pricing rates (us-central1 base).
// Used as FALLBACK when the Cloud Billing Catalog API is unavailable.
var gcpFamilyPricing = map[string]componentPricing{
	"n2":  {cpuPerHour: 0.031611, memPerHour: 0.004237},
	"n1":  {cpuPerHour: 0.031611, memPerHour: 0.004237},
	"e2":  {cpuPerHour: 0.021811, memPerHour: 0.002923},
	"n2d": {cpuPerHour: 0.027502, memPerHour: 0.003686},
	"c2":  {cpuPerHour: 0.03398, memPerHour: 0.004554},
	"c2d": {cpuPerHour: 0.02909, memPerHour: 0.003898},
	"c3":  {cpuPerHour: 0.03616, memPerHour: 0.00484},
	"c3d": {cpuPerHour: 0.03245, memPerHour: 0.00435},
	"c4":  {cpuPerHour: 0.03810, memPerHour: 0.00510},
	"h3":  {cpuPerHour: 0.03535, memPerHour: 0.00473},
	"m3":  {cpuPerHour: 0.03710, memPerHour: 0.00890},
	"n4":  {cpuPerHour: 0.02830, memPerHour: 0.00379},
	"t2d": {cpuPerHour: 0.027502, memPerHour: 0.003686},
	"t2a": {cpuPerHour: 0.0245, memPerHour: 0.00328},
	"a2":  {cpuPerHour: 0.031611, memPerHour: 0.004237},
	"a3":  {cpuPerHour: 0.031611, memPerHour: 0.004237},
	"g2":  {cpuPerHour: 0.031611, memPerHour: 0.004237},
}

// gcpRegionMultiplier adjusts base pricing by region. Regions not listed use 1.0.
// NOTE: These are FALLBACK multipliers only — used when the Cloud Billing Catalog
// API is unavailable. They are approximate and may drift from actual GCP pricing.
// When live pricing is available (the normal case), these are NOT used.
var gcpRegionMultiplier = map[string]float64{
	"us-central1":             1.00,
	"us-east1":                1.00,
	"us-east4":                1.10,
	"us-east5":                1.10,
	"us-south1":               1.10,
	"us-west1":                1.00,
	"us-west2":                1.20,
	"us-west3":                1.20,
	"us-west4":                1.10,
	"europe-west1":            1.10,
	"europe-west2":            1.15,
	"europe-west3":            1.15,
	"europe-west4":            1.10,
	"europe-west6":            1.25,
	"europe-west8":            1.12,
	"europe-west9":            1.12,
	"europe-north1":           1.10,
	"europe-central2":         1.15,
	"europe-southwest1":       1.12,
	"asia-east1":              1.10,
	"asia-east2":              1.20,
	"asia-northeast1":         1.15,
	"asia-northeast2":         1.15,
	"asia-northeast3":         1.15,
	"asia-south1":             1.08,
	"asia-south2":             1.08,
	"asia-southeast1":         1.10,
	"asia-southeast2":         1.15,
	"australia-southeast1":    1.20,
	"australia-southeast2":    1.20,
	"northamerica-northeast1": 1.10,
	"northamerica-northeast2": 1.10,
	"southamerica-east1":      1.25,
	"southamerica-west1":      1.25,
	"me-west1":                1.20,
	"me-central1":             1.20,
	"me-central2":             1.20,
	"africa-south1":           1.25,
}

// gpuPricing maps GPU model names to per-GPU hourly rates.
var gpuPricing = map[string]float64{
	"nvidia-tesla-a100": 2.934,
	"nvidia-a100-80gb":  2.934,
	"nvidia-tesla-v100": 2.48,
	"nvidia-tesla-t4":   0.35,
	"nvidia-l4":         0.70,
}

// gpuMachineSpecs defines GPU count and model for known GPU machine type prefixes.
type gpuMachineSpec struct {
	gpuCount int
	gpuModel string
}

var gpuMachineTypes = map[string]gpuMachineSpec{
	"a2-highgpu-1g":  {gpuCount: 1, gpuModel: "nvidia-tesla-a100"},
	"a2-highgpu-2g":  {gpuCount: 2, gpuModel: "nvidia-tesla-a100"},
	"a2-highgpu-4g":  {gpuCount: 4, gpuModel: "nvidia-tesla-a100"},
	"a2-highgpu-8g":  {gpuCount: 8, gpuModel: "nvidia-tesla-a100"},
	"a2-ultragpu-1g": {gpuCount: 1, gpuModel: "nvidia-a100-80gb"},
	"a2-ultragpu-2g": {gpuCount: 2, gpuModel: "nvidia-a100-80gb"},
	"a2-ultragpu-4g": {gpuCount: 4, gpuModel: "nvidia-a100-80gb"},
	"a2-ultragpu-8g": {gpuCount: 8, gpuModel: "nvidia-a100-80gb"},
	"g2-standard-4":  {gpuCount: 1, gpuModel: "nvidia-l4"},
	"g2-standard-8":  {gpuCount: 1, gpuModel: "nvidia-l4"},
	"g2-standard-12": {gpuCount: 1, gpuModel: "nvidia-l4"},
	"g2-standard-16": {gpuCount: 1, gpuModel: "nvidia-l4"},
	"g2-standard-24": {gpuCount: 2, gpuModel: "nvidia-l4"},
	"g2-standard-32": {gpuCount: 1, gpuModel: "nvidia-l4"},
	"g2-standard-48": {gpuCount: 4, gpuModel: "nvidia-l4"},
	"g2-standard-96": {gpuCount: 8, gpuModel: "nvidia-l4"},
}

// gceMachineType represents a machine type from the Compute Engine API.
type gceMachineType struct {
	Name                   string `json:"name"`
	GuestCpus              int    `json:"guestCpus"`
	MemoryMb               int    `json:"memoryMb"`
	Description            string `json:"description"`
	IsSharedCpu            bool   `json:"isSharedCpu"`
	MaximumPersistentDisks int    `json:"maximumPersistentDisks"`
}

// gceMachineTypeListResponse is the paginated response from the machineTypes API.
type gceMachineTypeListResponse struct {
	Items         []gceMachineType `json:"items"`
	NextPageToken string           `json:"nextPageToken"`
}

// billingCatalogSKU represents a SKU from the Cloud Billing Catalog API.
type billingCatalogSKU struct {
	SkuID          string   `json:"skuId"`
	Description    string   `json:"description"`
	Category       skuCategory `json:"category"`
	ServiceRegions []string `json:"serviceRegions"`
	PricingInfo    []skuPricingInfo `json:"pricingInfo"`
}

type skuCategory struct {
	ServiceDisplayName string `json:"serviceDisplayName"`
	ResourceFamily     string `json:"resourceFamily"`
	ResourceGroup      string `json:"resourceGroup"`
	UsageType          string `json:"usageType"`
}

type skuPricingInfo struct {
	PricingExpression skuPricingExpression `json:"pricingExpression"`
}

type skuPricingExpression struct {
	UsageUnit   string          `json:"usageUnit"`
	TieredRates []skuTieredRate `json:"tieredRates"`
}

type skuTieredRate struct {
	StartUsageAmount float64      `json:"startUsageAmount"`
	UnitPrice        skuUnitPrice `json:"unitPrice"`
}

type skuUnitPrice struct {
	CurrencyCode string `json:"currencyCode"`
	Units        string `json:"units"`
	Nanos        int64  `json:"nanos"`
}

type billingCatalogResponse struct {
	Skus          []billingCatalogSKU `json:"skus"`
	NextPageToken string              `json:"nextPageToken"`
}

// getGCPPricing computes pricing for all machine types in a region.
// It tries real API pricing first, then falls back to hardcoded component rates.
func getGCPPricing(ctx context.Context, project, region string, client *http.Client, sqliteCache *store.PricingCache) (*cloudprovider.PricingInfo, error) {
	pricingMu.RLock()
	if cached, ok := pricingCache[region]; ok && time.Since(pricingUpdated[region]) < cacheTTL {
		defer pricingMu.RUnlock()
		return cached, nil
	}
	pricingMu.RUnlock()

	// Check SQLite cache.
	if sqliteCache != nil {
		if prices, ok := sqliteCache.Get("gcp", region); ok && len(prices) > 0 {
			info := &cloudprovider.PricingInfo{
				Region:    region,
				Prices:    prices,
				UpdatedAt: time.Now(),
			}
			pricingMu.Lock()
			pricingCache[region] = info
			pricingUpdated[region] = time.Now()
			pricingMu.Unlock()
			return info, nil
		}
	}

	// Fetch machine types (may return cached pointers — do NOT modify them).
	types, typeErr := getGCPMachineTypes(ctx, project, region, client)
	if typeErr != nil {
		return nil, fmt.Errorf("getting machine types for pricing: %w", typeErr)
	}

	// Try to fetch real component pricing from the Cloud Billing Catalog API.
	realRates, err := fetchGCPPricingFromAPI(ctx, region, client)
	if err == nil && len(realRates) > 0 {
		prices := make(map[string]float64, len(types))
		for _, t := range types {
			price := computePriceWithRates(t, region, realRates)
			if price > 0 {
				prices[t.Name] = price
			}
		}

		// Sanity-check prices.
		if removed := store.SanitizePrices(prices); removed > 0 {
			slog.Warn("gcp: removed invalid prices from API response",
				"region", region, "removed", removed)
		}

		info := &cloudprovider.PricingInfo{
			Region:    region,
			Prices:    prices,
			UpdatedAt: time.Now(),
		}

		pricingMu.Lock()
		pricingCache[region] = info
		pricingUpdated[region] = time.Now()
		pricingMu.Unlock()

		// Persist to SQLite.
		if sqliteCache != nil {
			sqliteCache.Put("gcp", region, prices)
		}

		intmetrics.PricingFallbackActive.WithLabelValues("gcp", region).Set(0)
		intmetrics.PricingLastLiveUpdate.WithLabelValues("gcp", region).Set(float64(time.Now().Unix()))
		return info, nil
	}

	// Fallback: use hardcoded component rates.
	// These are approximate and may drift from actual GCP pricing.
	slog.Warn("gcp: using fallback hardcoded component pricing rates",
		"region", region)
	intmetrics.PricingFallbackTotal.WithLabelValues("gcp", region).Inc()
	intmetrics.PricingFallbackActive.WithLabelValues("gcp", region).Set(1)

	prices := make(map[string]float64, len(types))
	for _, t := range types {
		price := computePriceForRegion(t, region)
		if price > 0 {
			prices[t.Name] = price
		}
	}

	info := &cloudprovider.PricingInfo{
		Region:    region,
		Prices:    prices,
		UpdatedAt: time.Now(),
	}

	pricingMu.Lock()
	pricingCache[region] = info
	pricingUpdated[region] = time.Now()
	pricingMu.Unlock()

	return info, nil
}

// fetchGCPPricingFromAPI calls the Cloud Billing Catalog API to get real
// per-family component rates (CPU/RAM) for the target region.
func fetchGCPPricingFromAPI(ctx context.Context, region string, client *http.Client) (map[string]componentPricing, error) {
	const maxPages = 50 // safety guard against infinite pagination
	rates := make(map[string]componentPricing)
	pageToken := ""

	for page := 0; page < maxPages; page++ {
		if page == maxPages-1 {
			slog.Warn("gcp: billing catalog pagination hit safety limit, pricing data may be incomplete",
				"maxPages", maxPages)
		}
		url := fmt.Sprintf("%s/services/%s/skus?currencyCode=USD&pageSize=5000",
			billingCatalogBaseURL, computeEngineServiceID)
		if pageToken != "" {
			url += "&pageToken=" + pageToken
		}

		body, err := doGCPGet(ctx, client, url)
		if err != nil {
			return nil, fmt.Errorf("fetching billing catalog: %w", err)
		}

		var resp billingCatalogResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("parsing billing catalog response: %w", err)
		}

		for _, sku := range resp.Skus {
			// Only process Compute OnDemand SKUs.
			if sku.Category.ResourceFamily != "Compute" || sku.Category.UsageType != "OnDemand" {
				continue
			}

			// Check if this SKU applies to the target region.
			regionMatch := false
			for _, r := range sku.ServiceRegions {
				if r == region {
					regionMatch = true
					break
				}
			}
			if !regionMatch {
				continue
			}

			// Extract price.
			price := extractSKUPrice(sku)
			if price <= 0 {
				continue
			}

			// Use category.resourceGroup (e.g., "N2Standard", "N2DHighmem") to
			// determine the machine family. This is far more reliable than parsing
			// the free-text description which includes qualifiers like "AMD".
			family := familyFromResourceGroup(sku.Category.ResourceGroup)
			resType := resourceTypeFromDescription(sku.Description)
			if family == "" || resType == "" {
				continue
			}

			cp := rates[family]
			switch resType {
			case "cpu":
				cp.cpuPerHour = price
			case "ram":
				cp.memPerHour = price
			}
			rates[family] = cp
		}

		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}

	// Only return families that have both CPU and RAM rates.
	complete := make(map[string]componentPricing)
	for family, cp := range rates {
		if cp.cpuPerHour > 0 && cp.memPerHour > 0 {
			complete[family] = cp
		}
	}

	return complete, nil
}

// extractSKUPrice extracts the hourly USD price from a billing catalog SKU.
func extractSKUPrice(sku billingCatalogSKU) float64 {
	if len(sku.PricingInfo) == 0 {
		return 0
	}
	pi := sku.PricingInfo[0]
	if pi.PricingExpression.UsageUnit != "h" {
		return 0
	}
	if len(pi.PricingExpression.TieredRates) == 0 {
		return 0
	}
	tr := pi.PricingExpression.TieredRates[0]
	units := 0.0
	if tr.UnitPrice.Units != "" && tr.UnitPrice.Units != "0" {
		if _, err := fmt.Sscanf(tr.UnitPrice.Units, "%f", &units); err != nil {
			return 0
		}
	}
	return units + float64(tr.UnitPrice.Nanos)/1e9
}

// familyFromResourceGroup extracts the machine family from the billing catalog
// SKU's category.resourceGroup field. This is more reliable than parsing the
// free-text description, which includes qualifiers like "AMD" or "Intel".
//
// Examples:
//
//	"N2Standard"  -> "n2"
//	"N2DStandard" -> "n2d"
//	"E2Standard"  -> "e2"
//	"C3DHighmem"  -> "c3d"
//	"T2AStandard" -> "t2a"
func familyFromResourceGroup(rg string) string {
	// Check longest prefixes first so "N2D" matches before "N2".
	knownPrefixes := []struct{ prefix, family string }{
		{"N2D", "n2d"}, {"C2D", "c2d"}, {"C3D", "c3d"},
		{"T2A", "t2a"}, {"T2D", "t2d"},
		{"N1", "n1"}, {"N2", "n2"}, {"N4", "n4"},
		{"E2", "e2"},
		{"C2", "c2"}, {"C3", "c3"}, {"C4", "c4"},
		{"M1", "m1"}, {"M2", "m2"}, {"M3", "m3"},
		{"A2", "a2"}, {"A3", "a3"},
		{"G2", "g2"},
		{"H3", "h3"},
	}
	for _, p := range knownPrefixes {
		if strings.HasPrefix(rg, p.prefix) {
			return p.family
		}
	}
	return ""
}

// resourceTypeFromDescription determines whether a billing catalog SKU
// is a CPU or RAM component from its description text.
func resourceTypeFromDescription(desc string) string {
	lower := strings.ToLower(desc)
	if !strings.Contains(lower, "instance") {
		return ""
	}
	if strings.Contains(lower, "core") || strings.Contains(lower, "cpu") || strings.Contains(lower, "vcpu") {
		return "cpu"
	}
	if strings.Contains(lower, "ram") {
		return "ram"
	}
	return ""
}

// computePriceWithRates calculates the hourly price using real API rates,
// falling back to hardcoded rates for families not in the real data.
func computePriceWithRates(t *cloudprovider.InstanceType, region string, realRates map[string]componentPricing) float64 {
	seriesPrefix := extractSeriesPrefix(t.Name)

	cp, ok := realRates[seriesPrefix]
	if !ok {
		// Fall back to hardcoded rates (with region multiplier) for this family.
		return computePriceForRegion(t, region)
	}

	memGB := float64(t.MemoryMiB) / 1024.0
	price := cp.cpuPerHour*float64(t.CPUCores) + cp.memPerHour*memGB

	if t.GPUs > 0 && t.GPUType != "" {
		if gpuRate, ok := gpuPricing[t.GPUType]; ok {
			price += gpuRate * float64(t.GPUs)
		}
	}

	// Real API rates are already region-specific, no multiplier needed.
	return math.Round(price*10000) / 10000
}

// computePriceForRegion calculates the hourly price for a machine type using
// hardcoded component pricing and region multiplier. Used as fallback.
func computePriceForRegion(t *cloudprovider.InstanceType, region string) float64 {
	seriesPrefix := extractSeriesPrefix(t.Name)

	cp, ok := gcpFamilyPricing[seriesPrefix]
	if !ok {
		cp = gcpFamilyPricing["n2"]
	}

	memGB := float64(t.MemoryMiB) / 1024.0
	basePrice := cp.cpuPerHour*float64(t.CPUCores) + cp.memPerHour*memGB

	if t.GPUs > 0 && t.GPUType != "" {
		if gpuRate, ok := gpuPricing[t.GPUType]; ok {
			basePrice += gpuRate * float64(t.GPUs)
		}
	}

	// Apply region multiplier.
	if mult, ok := gcpRegionMultiplier[region]; ok {
		basePrice *= mult
	}

	return math.Round(basePrice*10000) / 10000
}

// extractSeriesPrefix extracts the series prefix from a GCP machine type name.
// "n2-standard-4" -> "n2", "e2-medium" -> "e2", "a2-highgpu-1g" -> "a2".
func extractSeriesPrefix(machineType string) string {
	parts := strings.SplitN(machineType, "-", 2)
	if len(parts) >= 1 {
		return parts[0]
	}
	return machineType
}

// getGCPMachineTypes fetches real machine type specs from the Compute Engine API.
func getGCPMachineTypes(ctx context.Context, project, region string, client *http.Client) ([]*cloudprovider.InstanceType, error) {
	machineTypeMu.RLock()
	if cached, ok := machineTypeCache[region]; ok && time.Since(machineTypeUpdated[region]) < cacheTTL {
		defer machineTypeMu.RUnlock()
		return cached, nil
	}
	machineTypeMu.RUnlock()

	// Discover actual zones in the region instead of assuming "-a" exists.
	zone, err := discoverZone(ctx, project, region, client)
	if err != nil {
		return nil, fmt.Errorf("discovering zone for %s: %w", region, err)
	}

	allTypes, err := fetchMachineTypesFromAPI(ctx, project, zone, region, client)
	if err != nil {
		return nil, err
	}

	machineTypeMu.Lock()
	machineTypeCache[region] = allTypes
	machineTypeUpdated[region] = time.Now()
	machineTypeMu.Unlock()

	return allTypes, nil
}

// discoverZone finds an actual zone in the region by calling the Regions API.
// Falls back to trying "-b", "-c", "-a" if the API call fails.
func discoverZone(ctx context.Context, project, region string, client *http.Client) (string, error) {
	url := fmt.Sprintf("https://compute.googleapis.com/compute/v1/projects/%s/regions/%s", project, region)
	body, err := doGCPGet(ctx, client, url)
	if err == nil {
		var result struct {
			Zones []string `json:"zones"`
		}
		if jsonErr := json.Unmarshal(body, &result); jsonErr == nil && len(result.Zones) > 0 {
			// Zone URLs are full resource paths; extract zone name.
			for _, z := range result.Zones {
				parts := strings.Split(z, "/")
				zoneName := parts[len(parts)-1]
				return zoneName, nil
			}
		}
	}

	// Fallback: try common zone suffixes and log a warning.
	suffixes := []string{"-a", "-b", "-c", "-d", "-f"}
	for _, suffix := range suffixes {
		zone := region + suffix
		// Try a lightweight API call to verify the zone exists
		testURL := fmt.Sprintf("https://compute.googleapis.com/compute/v1/projects/%s/zones/%s/machineTypes?maxResults=1", project, zone)
		resp, err := client.Get(testURL)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return zone, nil
			}
		}
	}

	return "", fmt.Errorf("could not discover any valid zone for region %s", region)
}

// fetchMachineTypesFromAPI fetches all machine types from Compute Engine, handling pagination.
func fetchMachineTypesFromAPI(ctx context.Context, project, zone, region string, client *http.Client) ([]*cloudprovider.InstanceType, error) {
	const maxPages = 20 // safety guard against infinite pagination
	var allTypes []*cloudprovider.InstanceType
	pageToken := ""

	for page := 0; page < maxPages; page++ {
		if page == maxPages-1 {
			slog.Warn("gcp: machine types pagination hit safety limit, data may be incomplete",
				"maxPages", maxPages, "zone", zone)
		}
		url := fmt.Sprintf("%s/projects/%s/zones/%s/machineTypes", computeBaseURL, project, zone)
		if pageToken != "" {
			url += "?pageToken=" + pageToken
		}

		body, err := doGCPGet(ctx, client, url)
		if err != nil {
			return nil, fmt.Errorf("listing machine types in %s: %w", zone, err)
		}

		var resp gceMachineTypeListResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("parsing machine types response: %w", err)
		}

		for _, mt := range resp.Items {
			it := convertMachineType(mt, region)
			if it != nil {
				allTypes = append(allTypes, it)
			}
		}

		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}

	return allTypes, nil
}

// convertMachineType converts a GCE machine type to a cloudprovider.InstanceType.
func convertMachineType(mt gceMachineType, region string) *cloudprovider.InstanceType {
	// Skip bare "custom-" machine types (no family prefix).
	if strings.HasPrefix(mt.Name, "custom-") {
		return nil
	}

	// Estimate pricing for family-prefixed custom machine types (e.g., n2-custom-4-8192).
	if strings.Contains(mt.Name, "custom") {
		family := extractSeriesPrefix(mt.Name)
		if rates, ok := gcpFamilyPricing[family]; ok {
			estimatedPrice := (rates.cpuPerHour * float64(mt.GuestCpus)) + (rates.memPerHour * float64(mt.MemoryMb) / 1024.0)
			itFamily, _ := familylock.ExtractFamily(mt.Name)
			arch := "amd64"
			if family == "t2a" {
				arch = "arm64"
			}
			it := &cloudprovider.InstanceType{
				Name:         mt.Name,
				Family:       itFamily,
				CPUCores:     mt.GuestCpus,
				MemoryMiB:    mt.MemoryMb,
				Architecture: arch,
				PricePerHour: math.Round(estimatedPrice*10000) / 10000,
			}
			return it
		}
		return nil
	}

	family, _ := familylock.ExtractFamily(mt.Name)

	arch := "amd64"
	seriesPrefix := extractSeriesPrefix(mt.Name)
	if seriesPrefix == "t2a" {
		arch = "arm64"
	}

	it := &cloudprovider.InstanceType{
		Name:         mt.Name,
		Family:       family,
		CPUCores:     mt.GuestCpus,
		MemoryMiB:    mt.MemoryMb,
		Architecture: arch,
	}

	// Check if this is a known GPU machine type.
	if spec, ok := gpuMachineTypes[mt.Name]; ok {
		it.GPUs = spec.gpuCount
		it.GPUType = spec.gpuModel
	}

	// Compute price using component pricing.
	price := computePriceForRegion(it, region)
	if price > 0 {
		it.PricePerHour = price
	}

	return it
}

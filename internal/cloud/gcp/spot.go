package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
)

// GetSpotPricing returns GCP Spot VM pricing for given machine types.
// First attempts to fetch real preemptible rates from the Cloud Billing Catalog
// API, then falls back to published discount percentages.
func (p *Provider) GetSpotPricing(ctx context.Context, region string, instanceTypes []string) ([]*cloudprovider.SpotInstanceInfo, error) {
	// Get on-demand pricing for comparison
	pricing, err := p.GetCurrentPricing(ctx, region)
	if err != nil {
		return nil, fmt.Errorf("getting on-demand pricing: %w", err)
	}

	// Try to get real preemptible component rates from the Billing Catalog API.
	preemptibleRates, _ := fetchGCPPreemptibleRates(ctx, region, p.httpClient)

	// Get zones in this region
	zones, err := p.getRegionZones(ctx, region)
	if err != nil {
		zones = []string{region + "-b", region + "-c", region + "-a"}
	}

	var result []*cloudprovider.SpotInstanceInfo

	for _, it := range instanceTypes {
		odPrice, ok := pricing.Prices[it]
		if !ok {
			continue
		}

		// Try real preemptible pricing first.
		spotPrice := 0.0
		family := extractGCPFamily(it)
		if cp, ok := preemptibleRates[family]; ok && cp.cpuPerHour > 0 {
			// Get machine type specs from on-demand list to compute preemptible price
			types, _ := getGCPMachineTypes(ctx, p.project, region, p.httpClient)
			for _, t := range types {
				if t.Name == it {
					memGB := float64(t.MemoryMiB) / 1024.0
					spotPrice = cp.cpuPerHour*float64(t.CPUCores) + cp.memPerHour*memGB
					break
				}
			}
		}

		// Fall back to estimated discount if no real rates.
		if spotPrice <= 0 {
			discount := estimateSpotDiscount(family)
			spotPrice = odPrice * (1 - discount)
		}

		savingsPct := 0.0
		if odPrice > 0 {
			savingsPct = (odPrice - spotPrice) / odPrice * 100
		}

		for _, zone := range zones {
			result = append(result, &cloudprovider.SpotInstanceInfo{
				InstanceType:     it,
				AvailabilityZone: zone,
				SpotPrice:        spotPrice,
				OnDemandPrice:    odPrice,
				SavingsPercent:   savingsPct,
			})
		}
	}

	return result, nil
}

// fetchGCPPreemptibleRates fetches preemptible/spot component pricing from the
// Cloud Billing Catalog API. Returns per-family rates keyed by family prefix.
func fetchGCPPreemptibleRates(ctx context.Context, region string, client *http.Client) (map[string]componentPricing, error) {
	const maxPages = 50 // safety guard against infinite pagination
	rates := make(map[string]componentPricing)
	pageToken := ""

	for page := 0; page < maxPages; page++ {
		url := fmt.Sprintf("%s/services/%s/skus?currencyCode=USD&pageSize=5000",
			billingCatalogBaseURL, computeEngineServiceID)
		if pageToken != "" {
			url += "&pageToken=" + pageToken
		}

		body, err := doGCPGet(ctx, client, url)
		if err != nil {
			return nil, fmt.Errorf("fetching billing catalog for preemptible: %w", err)
		}

		var resp billingCatalogResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("parsing billing catalog response: %w", err)
		}

		for _, sku := range resp.Skus {
			// Only process Preemptible Compute SKUs.
			if sku.Category.ResourceFamily != "Compute" || sku.Category.UsageType != "Preemptible" {
				continue
			}

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

			price := extractSKUPrice(sku)
			if price <= 0 {
				continue
			}

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

	// Only return families with complete pricing.
	complete := make(map[string]componentPricing)
	for family, cp := range rates {
		if cp.cpuPerHour > 0 && cp.memPerHour > 0 {
			complete[family] = cp
		}
	}
	return complete, nil
}

// estimateSpotDiscount returns an estimated spot discount for a GCP machine family.
// Used as fallback when the Billing Catalog API is unavailable.
func estimateSpotDiscount(family string) float64 {
	discountByFamily := map[string]float64{
		"e2":  0.69,
		"n2":  0.69,
		"n2d": 0.69,
		"n1":  0.80,
		"c2":  0.69,
		"c2d": 0.69,
		"c3":  0.65,
		"c3d": 0.65,
		"c4":  0.63,
		"m1":  0.69,
		"m2":  0.69,
		"m3":  0.65,
		"n4":  0.67,
		"a2":  0.60,
		"a3":  0.60,
		"g2":  0.60,
		"t2a": 0.69,
		"t2d": 0.69,
		"h3":  0.65,
	}
	if d, ok := discountByFamily[family]; ok {
		return d
	}
	return 0.69
}

// GetSpotInterruptionRate returns estimated preemption rates for GCP Spot VMs.
func (p *Provider) GetSpotInterruptionRate(ctx context.Context, region string, instanceTypes []string) (map[string]float64, error) {
	rates := make(map[string]float64)
	for _, it := range instanceTypes {
		family := extractGCPFamily(it)
		rates[it] = estimateGCPPreemptionRate(family)
	}
	return rates, nil
}

func estimateGCPPreemptionRate(family string) float64 {
	if strings.HasPrefix(family, "a2") || strings.HasPrefix(family, "g2") || strings.HasPrefix(family, "a3") {
		return 15.0
	}
	if family == "n1" {
		return 12.0
	}
	if strings.HasPrefix(family, "e2") {
		return 10.0
	}
	return 7.0
}

func extractGCPFamily(instanceType string) string {
	parts := strings.Split(instanceType, "-")
	if len(parts) >= 2 {
		return parts[0]
	}
	return instanceType
}

// getRegionZones returns zones in a GCP region.
func (p *Provider) getRegionZones(ctx context.Context, region string) ([]string, error) {
	url := fmt.Sprintf("https://compute.googleapis.com/compute/v1/projects/%s/regions/%s", p.project, region)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("regions API returned %d", resp.StatusCode)
	}

	var result struct {
		Zones []string `json:"zones"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	var zones []string
	for _, z := range result.Zones {
		parts := strings.Split(z, "/")
		zones = append(zones, parts[len(parts)-1])
	}
	return zones, nil
}

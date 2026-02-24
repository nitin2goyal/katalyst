package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
)

// GetSpotPricing returns Azure Spot VM pricing for given VM sizes.
// Uses the public Azure Retail Prices API to get real spot prices.
func (p *Provider) GetSpotPricing(ctx context.Context, region string, instanceTypes []string) ([]*cloudprovider.SpotInstanceInfo, error) {
	// Get on-demand pricing for comparison
	odPricing, err := p.GetCurrentPricing(ctx, region)
	if err != nil {
		return nil, fmt.Errorf("getting on-demand pricing: %w", err)
	}

	// Fetch spot prices from Azure Retail Prices API (public, no auth)
	spotPrices, err := fetchAzureSpotPrices(ctx, region, instanceTypes)
	if err != nil {
		return nil, fmt.Errorf("fetching spot prices: %w", err)
	}

	var result []*cloudprovider.SpotInstanceInfo
	for _, it := range instanceTypes {
		spotPrice, hasSpot := spotPrices[it]
		if !hasSpot {
			continue
		}
		odPrice := odPricing.Prices[it]
		if odPrice == 0 {
			continue
		}

		savingsPct := 0.0
		if odPrice > 0 {
			savingsPct = (odPrice - spotPrice) / odPrice * 100
		}

		result = append(result, &cloudprovider.SpotInstanceInfo{
			InstanceType:     it,
			AvailabilityZone: region, // Azure spot prices are regional
			SpotPrice:        spotPrice,
			OnDemandPrice:    odPrice,
			SavingsPercent:   savingsPct,
		})
	}

	return result, nil
}

// GetSpotInterruptionRate returns estimated eviction rates for Azure Spot VMs.
// Azure publishes eviction rate ranges (0-5%, 5-10%, 10-15%, 15-20%, 20+%) per
// VM size per region. We use the published ranges as estimates.
func (p *Provider) GetSpotInterruptionRate(ctx context.Context, region string, instanceTypes []string) (map[string]float64, error) {
	rates := make(map[string]float64)
	for _, it := range instanceTypes {
		rates[it] = estimateAzureEvictionRate(it)
	}
	return rates, nil
}

// fetchAzureSpotPrices gets spot prices from the Azure Retail Prices API.
func fetchAzureSpotPrices(ctx context.Context, region string, instanceTypes []string) (map[string]float64, error) {
	prices := make(map[string]float64)

	// Build OData filter for spot pricing
	filter := fmt.Sprintf("serviceName eq 'Virtual Machines' and armRegionName eq '%s' and priceType eq 'Consumption' and currencyCode eq 'USD'", region)

	client := &http.Client{Timeout: 30 * time.Second}
	nextURL := fmt.Sprintf("%s?$filter=%s", retailPricesURL, url.QueryEscape(filter))

	// Build a set of requested types for quick lookup
	requested := make(map[string]bool, len(instanceTypes))
	for _, it := range instanceTypes {
		requested[strings.ToLower(it)] = true
	}

	const maxPages = 50 // safety limit to prevent unbounded pagination
	for page := 0; nextURL != "" && page < maxPages; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, nextURL, nil)
		if err != nil {
			return nil, err
		}

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetching Azure spot prices: %w", err)
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("Azure Retail Prices API returned %d", resp.StatusCode)
		}

		var result retailPriceResponse
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("parsing pricing response: %w", err)
		}

		for _, item := range result.Items {
			// Only spot prices
			if item.UnitOfMeasure != "1 Hour" {
				continue
			}
			// Check if this is a Spot price (meterName contains "Spot")
			if !strings.Contains(item.MeterName, "Spot") {
				continue
			}
			// Skip Windows
			if strings.Contains(item.ProductName, "Windows") {
				continue
			}

			skuLower := strings.ToLower(item.ArmSkuName)
			if len(requested) > 0 && !requested[skuLower] {
				// Only collect prices for requested types
				continue
			}

			// Keep lowest spot price per SKU
			if existing, ok := prices[item.ArmSkuName]; !ok || item.RetailPrice < existing {
				prices[item.ArmSkuName] = item.RetailPrice
			}
		}

		nextURL = result.NextPageLink

		// If we have all requested types, stop early
		if len(requested) > 0 {
			found := 0
			for _, it := range instanceTypes {
				if _, ok := prices[it]; ok {
					found++
				}
			}
			if found >= len(instanceTypes) {
				break
			}
		}
	}

	return prices, nil
}

// estimateAzureSpotDiscount returns an estimated spot discount fraction (0-1)
// for an Azure VM size, used when real spot prices are unavailable.
func estimateAzureSpotDiscount(vmSize string) float64 {
	vmLower := strings.ToLower(vmSize)

	// GPU VMs typically have smaller spot discounts (high demand).
	if strings.Contains(vmLower, "standard_nc") || strings.Contains(vmLower, "standard_nd") || strings.Contains(vmLower, "standard_nv") {
		return 0.40
	}
	// Burstable B-series.
	if strings.Contains(vmLower, "standard_b") {
		return 0.55
	}
	// General purpose — typical 60-80% discount.
	return 0.60
}

// estimateAzureEvictionRate provides estimated monthly eviction rates.
// Based on Azure's published eviction rate ranges per VM series.
func estimateAzureEvictionRate(vmSize string) float64 {
	vmLower := strings.ToLower(vmSize)

	// GPU VMs tend to have higher eviction rates (high demand)
	if strings.Contains(vmLower, "standard_nc") || strings.Contains(vmLower, "standard_nd") {
		return 15.0
	}
	if strings.Contains(vmLower, "standard_nv") {
		return 12.0
	}

	// B-series (burstable, very popular) — higher eviction
	if strings.Contains(vmLower, "standard_b") {
		return 12.0
	}

	// D-series v2/v3 (very popular) — moderate eviction
	if strings.Contains(vmLower, "standard_d") && (strings.Contains(vmLower, "v2") || strings.Contains(vmLower, "v3")) {
		return 10.0
	}

	// Newer generations (v5, v6) — lower eviction
	if strings.Contains(vmLower, "v5") || strings.Contains(vmLower, "v6") {
		return 5.0
	}

	// Default moderate rate
	return 8.0
}

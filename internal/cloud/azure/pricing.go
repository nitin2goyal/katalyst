package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"log/slog"

	"github.com/koptimizer/koptimizer/internal/store"
	intmetrics "github.com/koptimizer/koptimizer/internal/metrics"
	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
	"github.com/koptimizer/koptimizer/pkg/familylock"
)

var (
	azurePricingCacheMap   map[string]*cloudprovider.PricingInfo // region -> pricing
	azurePricingMu         sync.RWMutex
	azurePricingUpdatedMap map[string]time.Time // region -> last updated

	azureVMSizeCacheMap   map[string][]*cloudprovider.InstanceType // region -> types
	azureVMSizeMu         sync.RWMutex
	azureVMSizeUpdatedMap map[string]time.Time // region -> last updated
)

func init() {
	azurePricingCacheMap = make(map[string]*cloudprovider.PricingInfo)
	azurePricingUpdatedMap = make(map[string]time.Time)
	azureVMSizeCacheMap = make(map[string][]*cloudprovider.InstanceType)
	azureVMSizeUpdatedMap = make(map[string]time.Time)
}

const (
	retailPricesURL = "https://prices.azure.com/api/retail/prices"
	pricingCacheTTL = 1 * time.Hour
	vmSizeCacheTTL  = 1 * time.Hour
)

// retailPriceResponse is the response from the Azure Retail Prices API.
type retailPriceResponse struct {
	Items        []retailPriceItem `json:"Items"`
	NextPageLink string            `json:"NextPageLink"`
	Count        int               `json:"Count"`
}

// retailPriceItem represents a single price item from the Retail Prices API.
type retailPriceItem struct {
	CurrencyCode    string  `json:"currencyCode"`
	TierMinimumUnit float64 `json:"tierMinimumUnits"`
	RetailPrice     float64 `json:"retailPrice"`
	UnitPrice       float64 `json:"unitPrice"`
	ArmRegionName   string  `json:"armRegionName"`
	Location        string  `json:"location"`
	EffectiveDate   string  `json:"effectiveStartDate"`
	MeterID         string  `json:"meterId"`
	MeterName       string  `json:"meterName"`
	ProductID       string  `json:"productId"`
	ProductName     string  `json:"productName"`
	SkuID           string  `json:"skuId"`
	SkuName         string  `json:"skuName"`
	ServiceName     string  `json:"serviceName"`
	ServiceID       string  `json:"serviceId"`
	ServiceFamily   string  `json:"serviceFamily"`
	UnitOfMeasure   string  `json:"unitOfMeasure"`
	Type            string  `json:"type"`
	IsPrimaryRegion bool    `json:"isPrimaryMeterRegion"`
	ArmSkuName      string  `json:"armSkuName"`
}

// vmSizeListResponse is the ARM response for listing VM sizes.
type vmSizeListResponse struct {
	Value []vmSizeResource `json:"value"`
}

// vmSizeResource represents a VM size from the Compute REST API.
type vmSizeResource struct {
	Name                 string `json:"name"`
	NumberOfCores        int    `json:"numberOfCores"`
	OsDiskSizeInMB       int    `json:"osDiskSizeInMB"`
	ResourceDiskSizeInMB int    `json:"resourceDiskSizeInMB"`
	MemoryInMB           int    `json:"memoryInMB"`
	MaxDataDiskCount     int    `json:"maxDataDiskCount"`
}

func getAzurePricing(ctx context.Context, region string, sqliteCache *store.PricingCache) (*cloudprovider.PricingInfo, error) {
	azurePricingMu.RLock()
	if cached, ok := azurePricingCacheMap[region]; ok && time.Since(azurePricingUpdatedMap[region]) < pricingCacheTTL {
		defer azurePricingMu.RUnlock()
		return cached, nil
	}
	azurePricingMu.RUnlock()

	// Acquire write lock and double-check to prevent duplicate API calls
	azurePricingMu.Lock()
	if cached, ok := azurePricingCacheMap[region]; ok && time.Since(azurePricingUpdatedMap[region]) < pricingCacheTTL {
		azurePricingMu.Unlock()
		return cached, nil
	}
	azurePricingMu.Unlock()

	// Check SQLite cache.
	if sqliteCache != nil {
		if prices, ok := sqliteCache.Get("azure", region); ok && len(prices) > 0 {
			info := &cloudprovider.PricingInfo{
				Region:    region,
				Prices:    prices,
				UpdatedAt: time.Now(),
			}
			azurePricingMu.Lock()
			azurePricingCacheMap[region] = info
			azurePricingUpdatedMap[region] = time.Now()
			azurePricingMu.Unlock()
			return info, nil
		}
	}

	prices, err := fetchRetailPrices(ctx, region)
	if err != nil {
		return nil, fmt.Errorf("fetching Azure retail prices: %w", err)
	}

	info := &cloudprovider.PricingInfo{
		Region:    region,
		Prices:    prices,
		UpdatedAt: time.Now(),
	}

	azurePricingMu.Lock()
	azurePricingCacheMap[region] = info
	azurePricingUpdatedMap[region] = time.Now()
	azurePricingMu.Unlock()

	// Persist to SQLite.
	if sqliteCache != nil {
		sqliteCache.Put("azure", region, prices)
	}

	return info, nil
}

// fetchRetailPrices fetches VM pricing from the public Azure Retail Prices API.
// This API requires no authentication.
func fetchRetailPrices(ctx context.Context, region string) (map[string]float64, error) {
	prices := make(map[string]float64)
	client := &http.Client{Timeout: 30 * time.Second}

	// Build the OData filter for Linux VM consumption pricing.
	filter := fmt.Sprintf(
		"serviceName eq 'Virtual Machines' and armRegionName eq '%s' and priceType eq 'Consumption' and currencyCode eq 'USD'",
		region,
	)

	const maxPages = 100 // safety guard against infinite pagination
	nextURL := fmt.Sprintf("%s?$filter=%s", retailPricesURL, url.QueryEscape(filter))

	for page := 0; nextURL != "" && page < maxPages; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, nextURL, nil)
		if err != nil {
			return nil, fmt.Errorf("creating retail prices request: %w", err)
		}

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("retail prices request failed: %w", err)
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("reading retail prices response: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("retail prices returned status %d: %s", resp.StatusCode, string(body))
		}

		var priceResp retailPriceResponse
		if err := json.Unmarshal(body, &priceResp); err != nil {
			return nil, fmt.Errorf("decoding retail prices response: %w", err)
		}

		for _, item := range priceResp.Items {
			// Only include hourly consumption prices.
			if item.UnitOfMeasure != "1 Hour" {
				continue
			}

			// Skip Windows pricing -- we want Linux prices.
			if strings.Contains(item.ProductName, "Windows") {
				continue
			}

			// Skip Spot/Low Priority pricing items.
			if strings.Contains(item.MeterName, "Spot") || strings.Contains(item.MeterName, "Low Priority") {
				continue
			}
			if strings.Contains(item.SkuName, "Spot") || strings.Contains(item.SkuName, "Low Priority") {
				continue
			}

			armSku := item.ArmSkuName
			if armSku == "" {
				continue
			}

			// Keep the lowest price for each SKU (deduplicate across product variants).
			if existing, ok := prices[armSku]; !ok || item.RetailPrice < existing {
				prices[armSku] = item.RetailPrice
			}
		}

		nextURL = priceResp.NextPageLink
	}

	return prices, nil
}

func getAzureVMSizes(ctx context.Context, p *Provider, region string) ([]*cloudprovider.InstanceType, error) {
	azureVMSizeMu.RLock()
	if cached, ok := azureVMSizeCacheMap[region]; ok && time.Since(azureVMSizeUpdatedMap[region]) < vmSizeCacheTTL {
		defer azureVMSizeMu.RUnlock()
		return cached, nil
	}
	azureVMSizeMu.RUnlock()

	// Try the authenticated Compute REST API first for real specs.
	types, err := fetchVMSizesFromAPI(ctx, p, region)
	if err != nil || len(types) == 0 {
		// Fall back to comprehensive hardcoded map.
		slog.Warn("azure: using fallback hardcoded VM sizes", "region", region, "error", err)
		intmetrics.PricingFallbackTotal.WithLabelValues("azure", region).Inc()
		intmetrics.PricingFallbackActive.WithLabelValues("azure", region).Set(1)
		types = getHardcodedVMSizes()
	} else {
		intmetrics.PricingFallbackActive.WithLabelValues("azure", region).Set(0)
		intmetrics.PricingLastLiveUpdate.WithLabelValues("azure", region).Set(float64(time.Now().Unix()))
	}

	// Enrich with pricing data.
	pricing, pricingErr := getAzurePricing(ctx, region, p.pricingCache)
	if pricingErr == nil {
		for _, t := range types {
			if price, ok := pricing.Prices[t.Name]; ok {
				t.PricePerHour = price
			}
		}
	}

	azureVMSizeMu.Lock()
	azureVMSizeCacheMap[region] = types
	azureVMSizeUpdatedMap[region] = time.Now()
	azureVMSizeMu.Unlock()

	return types, nil
}

// fetchVMSizesFromAPI fetches VM sizes from the Azure Compute REST API (requires auth).
func fetchVMSizesFromAPI(ctx context.Context, p *Provider, region string) ([]*cloudprovider.InstanceType, error) {
	apiURL := fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.Compute/locations/%s/vmSizes?api-version=%s",
		armBaseURL, p.subscriptionID, region, computeAPIVersion)

	resp, err := p.doARMRequest(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("fetching VM sizes: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading VM sizes response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("VM sizes returned status %d: %s", resp.StatusCode, string(body))
	}

	var sizeResp vmSizeListResponse
	if err := json.Unmarshal(body, &sizeResp); err != nil {
		return nil, fmt.Errorf("decoding VM sizes response: %w", err)
	}

	var types []*cloudprovider.InstanceType
	for _, s := range sizeResp.Value {
		family, _ := familylock.ExtractFamily(s.Name)

		gpus := 0
		gpuType := ""
		arch := "amd64"

		// Detect GPU VMs by family prefix.
		nameLower := strings.ToLower(s.Name)
		if strings.Contains(nameLower, "standard_n") {
			gpus, gpuType = detectGPU(s.Name, s.NumberOfCores)
		}

		// Detect ARM64 VMs (Dpsv5, Epsv5, etc. with 'p' suffix indicating ARM).
		if strings.Contains(nameLower, "pbs") || strings.Contains(nameLower, "pds") ||
			strings.Contains(nameLower, "pls") || strings.Contains(nameLower, "ps") {
			// Azure ARM64 VMs typically have 'p' in the family designator.
			if isARM64VM(s.Name) {
				arch = "arm64"
			}
		}

		types = append(types, &cloudprovider.InstanceType{
			Name:         s.Name,
			Family:       family,
			CPUCores:     s.NumberOfCores,
			MemoryMiB:    s.MemoryInMB,
			GPUs:         gpus,
			GPUType:      gpuType,
			Architecture: arch,
		})
	}

	return types, nil
}

// detectGPU determines GPU count and type based on Azure VM name patterns.
func detectGPU(vmName string, cores int) (int, string) {
	nameLower := strings.ToLower(vmName)

	// NC-series: NVIDIA Tesla K80, V100, T4, A100
	if strings.Contains(nameLower, "standard_nc") {
		if strings.Contains(nameLower, "v3") {
			// NC v3: Tesla V100
			switch {
			case strings.Contains(nameLower, "nc6"):
				return 1, "NVIDIA Tesla V100"
			case strings.Contains(nameLower, "nc12"):
				return 2, "NVIDIA Tesla V100"
			case strings.Contains(nameLower, "nc24"):
				return 4, "NVIDIA Tesla V100"
			}
		}
		if strings.Contains(nameLower, "a100") {
			// NC A100: A100
			if strings.Contains(nameLower, "nc24") {
				return 1, "NVIDIA A100"
			}
			if strings.Contains(nameLower, "nc48") {
				return 2, "NVIDIA A100"
			}
			if strings.Contains(nameLower, "nc96") {
				return 4, "NVIDIA A100"
			}
		}
		if strings.Contains(nameLower, "as_t4") || strings.Contains(nameLower, "t4") {
			return 1, "NVIDIA T4"
		}
		// Default NC series
		switch {
		case strings.Contains(nameLower, "nc6"):
			return 1, "NVIDIA Tesla K80"
		case strings.Contains(nameLower, "nc12"):
			return 2, "NVIDIA Tesla K80"
		case strings.Contains(nameLower, "nc24"):
			return 4, "NVIDIA Tesla K80"
		default:
			return 1, "NVIDIA GPU"
		}
	}

	// ND-series: NVIDIA Tesla P40, A100
	if strings.Contains(nameLower, "standard_nd") {
		if strings.Contains(nameLower, "a100") || strings.Contains(nameLower, "v4") {
			switch {
			case strings.Contains(nameLower, "nd96"):
				return 8, "NVIDIA A100"
			default:
				return 1, "NVIDIA A100"
			}
		}
		if strings.Contains(nameLower, "h100") || strings.Contains(nameLower, "v5") {
			switch {
			case strings.Contains(nameLower, "nd96"):
				return 8, "NVIDIA H100"
			default:
				return 1, "NVIDIA H100"
			}
		}
		switch {
		case strings.Contains(nameLower, "nd6"):
			return 1, "NVIDIA Tesla P40"
		case strings.Contains(nameLower, "nd12"):
			return 2, "NVIDIA Tesla P40"
		case strings.Contains(nameLower, "nd24"):
			return 4, "NVIDIA Tesla P40"
		default:
			return 1, "NVIDIA GPU"
		}
	}

	// NV-series: NVIDIA Tesla M60, A10
	if strings.Contains(nameLower, "standard_nv") {
		if strings.Contains(nameLower, "v3") {
			switch {
			case strings.Contains(nameLower, "nv12"):
				return 1, "NVIDIA Tesla M60"
			case strings.Contains(nameLower, "nv24"):
				return 2, "NVIDIA Tesla M60"
			case strings.Contains(nameLower, "nv48"):
				return 4, "NVIDIA Tesla M60"
			}
		}
		if strings.Contains(nameLower, "ads_a10") || strings.Contains(nameLower, "v5") {
			return 1, "NVIDIA A10"
		}
		switch {
		case strings.Contains(nameLower, "nv6"):
			return 1, "NVIDIA Tesla M60"
		case strings.Contains(nameLower, "nv12"):
			return 2, "NVIDIA Tesla M60"
		case strings.Contains(nameLower, "nv24"):
			return 4, "NVIDIA Tesla M60"
		default:
			return 1, "NVIDIA GPU"
		}
	}

	// Fallback for any N-series
	return 1, "NVIDIA GPU"
}

// isARM64VM checks if a VM size is ARM64-based.
func isARM64VM(vmName string) bool {
	// Azure ARM64 VMs have 'p' in the series suffix, e.g., Dps_v5, Dpds_v5, Epds_v5
	parts := strings.Split(vmName, "_")
	if len(parts) < 2 {
		return false
	}
	sizePart := parts[1] // e.g., "D4ps" or "E8pds"
	// Strip digits to get the series letters, then check for 'p'
	letters := ""
	for _, ch := range sizePart {
		if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' {
			letters += string(ch)
		}
	}
	// ARM64 series contain 'p' in the suffix letters (e.g., "Dps", "Eps", "Dpds")
	return strings.Contains(strings.ToLower(letters), "p")
}

// getHardcodedVMSizes returns a comprehensive hardcoded list of common Azure VM sizes.
func getHardcodedVMSizes() []*cloudprovider.InstanceType {
	return []*cloudprovider.InstanceType{
		// B-series (burstable)
		{Name: "Standard_B1s", Family: "Standard_B", CPUCores: 1, MemoryMiB: 1024, Architecture: "amd64"},
		{Name: "Standard_B1ms", Family: "Standard_B", CPUCores: 1, MemoryMiB: 2048, Architecture: "amd64"},
		{Name: "Standard_B2s", Family: "Standard_B", CPUCores: 2, MemoryMiB: 4096, Architecture: "amd64"},
		{Name: "Standard_B2ms", Family: "Standard_B", CPUCores: 2, MemoryMiB: 8192, Architecture: "amd64"},
		{Name: "Standard_B4ms", Family: "Standard_B", CPUCores: 4, MemoryMiB: 16384, Architecture: "amd64"},
		{Name: "Standard_B8ms", Family: "Standard_B", CPUCores: 8, MemoryMiB: 32768, Architecture: "amd64"},
		{Name: "Standard_B12ms", Family: "Standard_B", CPUCores: 12, MemoryMiB: 49152, Architecture: "amd64"},
		{Name: "Standard_B16ms", Family: "Standard_B", CPUCores: 16, MemoryMiB: 65536, Architecture: "amd64"},
		{Name: "Standard_B20ms", Family: "Standard_B", CPUCores: 20, MemoryMiB: 81920, Architecture: "amd64"},

		// D-series v3
		{Name: "Standard_D2s_v3", Family: "Standard_D_v3", CPUCores: 2, MemoryMiB: 8192, Architecture: "amd64"},
		{Name: "Standard_D4s_v3", Family: "Standard_D_v3", CPUCores: 4, MemoryMiB: 16384, Architecture: "amd64"},
		{Name: "Standard_D8s_v3", Family: "Standard_D_v3", CPUCores: 8, MemoryMiB: 32768, Architecture: "amd64"},
		{Name: "Standard_D16s_v3", Family: "Standard_D_v3", CPUCores: 16, MemoryMiB: 65536, Architecture: "amd64"},
		{Name: "Standard_D32s_v3", Family: "Standard_D_v3", CPUCores: 32, MemoryMiB: 131072, Architecture: "amd64"},
		{Name: "Standard_D48s_v3", Family: "Standard_D_v3", CPUCores: 48, MemoryMiB: 196608, Architecture: "amd64"},
		{Name: "Standard_D64s_v3", Family: "Standard_D_v3", CPUCores: 64, MemoryMiB: 262144, Architecture: "amd64"},

		// D-series v4
		{Name: "Standard_D2s_v4", Family: "Standard_D_v4", CPUCores: 2, MemoryMiB: 8192, Architecture: "amd64"},
		{Name: "Standard_D4s_v4", Family: "Standard_D_v4", CPUCores: 4, MemoryMiB: 16384, Architecture: "amd64"},
		{Name: "Standard_D8s_v4", Family: "Standard_D_v4", CPUCores: 8, MemoryMiB: 32768, Architecture: "amd64"},
		{Name: "Standard_D16s_v4", Family: "Standard_D_v4", CPUCores: 16, MemoryMiB: 65536, Architecture: "amd64"},
		{Name: "Standard_D32s_v4", Family: "Standard_D_v4", CPUCores: 32, MemoryMiB: 131072, Architecture: "amd64"},
		{Name: "Standard_D48s_v4", Family: "Standard_D_v4", CPUCores: 48, MemoryMiB: 196608, Architecture: "amd64"},
		{Name: "Standard_D64s_v4", Family: "Standard_D_v4", CPUCores: 64, MemoryMiB: 262144, Architecture: "amd64"},

		// D-series v5
		{Name: "Standard_D2s_v5", Family: "Standard_D_v5", CPUCores: 2, MemoryMiB: 8192, Architecture: "amd64"},
		{Name: "Standard_D4s_v5", Family: "Standard_D_v5", CPUCores: 4, MemoryMiB: 16384, Architecture: "amd64"},
		{Name: "Standard_D8s_v5", Family: "Standard_D_v5", CPUCores: 8, MemoryMiB: 32768, Architecture: "amd64"},
		{Name: "Standard_D16s_v5", Family: "Standard_D_v5", CPUCores: 16, MemoryMiB: 65536, Architecture: "amd64"},
		{Name: "Standard_D32s_v5", Family: "Standard_D_v5", CPUCores: 32, MemoryMiB: 131072, Architecture: "amd64"},
		{Name: "Standard_D48s_v5", Family: "Standard_D_v5", CPUCores: 48, MemoryMiB: 196608, Architecture: "amd64"},
		{Name: "Standard_D64s_v5", Family: "Standard_D_v5", CPUCores: 64, MemoryMiB: 262144, Architecture: "amd64"},
		{Name: "Standard_D96s_v5", Family: "Standard_D_v5", CPUCores: 96, MemoryMiB: 393216, Architecture: "amd64"},

		// D-series ARM64 (Dpsv5)
		{Name: "Standard_D2ps_v5", Family: "Standard_D_v5", CPUCores: 2, MemoryMiB: 8192, Architecture: "arm64"},
		{Name: "Standard_D4ps_v5", Family: "Standard_D_v5", CPUCores: 4, MemoryMiB: 16384, Architecture: "arm64"},
		{Name: "Standard_D8ps_v5", Family: "Standard_D_v5", CPUCores: 8, MemoryMiB: 32768, Architecture: "arm64"},
		{Name: "Standard_D16ps_v5", Family: "Standard_D_v5", CPUCores: 16, MemoryMiB: 65536, Architecture: "arm64"},
		{Name: "Standard_D32ps_v5", Family: "Standard_D_v5", CPUCores: 32, MemoryMiB: 131072, Architecture: "arm64"},
		{Name: "Standard_D48ps_v5", Family: "Standard_D_v5", CPUCores: 48, MemoryMiB: 196608, Architecture: "arm64"},
		{Name: "Standard_D64ps_v5", Family: "Standard_D_v5", CPUCores: 64, MemoryMiB: 262144, Architecture: "arm64"},

		// E-series v3 (memory optimized)
		{Name: "Standard_E2s_v3", Family: "Standard_E_v3", CPUCores: 2, MemoryMiB: 16384, Architecture: "amd64"},
		{Name: "Standard_E4s_v3", Family: "Standard_E_v3", CPUCores: 4, MemoryMiB: 32768, Architecture: "amd64"},
		{Name: "Standard_E8s_v3", Family: "Standard_E_v3", CPUCores: 8, MemoryMiB: 65536, Architecture: "amd64"},
		{Name: "Standard_E16s_v3", Family: "Standard_E_v3", CPUCores: 16, MemoryMiB: 131072, Architecture: "amd64"},
		{Name: "Standard_E32s_v3", Family: "Standard_E_v3", CPUCores: 32, MemoryMiB: 262144, Architecture: "amd64"},
		{Name: "Standard_E48s_v3", Family: "Standard_E_v3", CPUCores: 48, MemoryMiB: 393216, Architecture: "amd64"},
		{Name: "Standard_E64s_v3", Family: "Standard_E_v3", CPUCores: 64, MemoryMiB: 442368, Architecture: "amd64"},

		// E-series v4
		{Name: "Standard_E2s_v4", Family: "Standard_E_v4", CPUCores: 2, MemoryMiB: 16384, Architecture: "amd64"},
		{Name: "Standard_E4s_v4", Family: "Standard_E_v4", CPUCores: 4, MemoryMiB: 32768, Architecture: "amd64"},
		{Name: "Standard_E8s_v4", Family: "Standard_E_v4", CPUCores: 8, MemoryMiB: 65536, Architecture: "amd64"},
		{Name: "Standard_E16s_v4", Family: "Standard_E_v4", CPUCores: 16, MemoryMiB: 131072, Architecture: "amd64"},
		{Name: "Standard_E32s_v4", Family: "Standard_E_v4", CPUCores: 32, MemoryMiB: 262144, Architecture: "amd64"},
		{Name: "Standard_E48s_v4", Family: "Standard_E_v4", CPUCores: 48, MemoryMiB: 393216, Architecture: "amd64"},
		{Name: "Standard_E64s_v4", Family: "Standard_E_v4", CPUCores: 64, MemoryMiB: 516096, Architecture: "amd64"},

		// E-series v5
		{Name: "Standard_E2s_v5", Family: "Standard_E_v5", CPUCores: 2, MemoryMiB: 16384, Architecture: "amd64"},
		{Name: "Standard_E4s_v5", Family: "Standard_E_v5", CPUCores: 4, MemoryMiB: 32768, Architecture: "amd64"},
		{Name: "Standard_E8s_v5", Family: "Standard_E_v5", CPUCores: 8, MemoryMiB: 65536, Architecture: "amd64"},
		{Name: "Standard_E16s_v5", Family: "Standard_E_v5", CPUCores: 16, MemoryMiB: 131072, Architecture: "amd64"},
		{Name: "Standard_E32s_v5", Family: "Standard_E_v5", CPUCores: 32, MemoryMiB: 262144, Architecture: "amd64"},
		{Name: "Standard_E48s_v5", Family: "Standard_E_v5", CPUCores: 48, MemoryMiB: 393216, Architecture: "amd64"},
		{Name: "Standard_E64s_v5", Family: "Standard_E_v5", CPUCores: 64, MemoryMiB: 516096, Architecture: "amd64"},
		{Name: "Standard_E96s_v5", Family: "Standard_E_v5", CPUCores: 96, MemoryMiB: 688128, Architecture: "amd64"},

		// E-series ARM64 (Epsv5)
		{Name: "Standard_E2ps_v5", Family: "Standard_E_v5", CPUCores: 2, MemoryMiB: 16384, Architecture: "arm64"},
		{Name: "Standard_E4ps_v5", Family: "Standard_E_v5", CPUCores: 4, MemoryMiB: 32768, Architecture: "arm64"},
		{Name: "Standard_E8ps_v5", Family: "Standard_E_v5", CPUCores: 8, MemoryMiB: 65536, Architecture: "arm64"},
		{Name: "Standard_E16ps_v5", Family: "Standard_E_v5", CPUCores: 16, MemoryMiB: 131072, Architecture: "arm64"},
		{Name: "Standard_E32ps_v5", Family: "Standard_E_v5", CPUCores: 32, MemoryMiB: 262144, Architecture: "arm64"},

		// F-series v2 (compute optimized)
		{Name: "Standard_F2s_v2", Family: "Standard_F_v2", CPUCores: 2, MemoryMiB: 4096, Architecture: "amd64"},
		{Name: "Standard_F4s_v2", Family: "Standard_F_v2", CPUCores: 4, MemoryMiB: 8192, Architecture: "amd64"},
		{Name: "Standard_F8s_v2", Family: "Standard_F_v2", CPUCores: 8, MemoryMiB: 16384, Architecture: "amd64"},
		{Name: "Standard_F16s_v2", Family: "Standard_F_v2", CPUCores: 16, MemoryMiB: 32768, Architecture: "amd64"},
		{Name: "Standard_F32s_v2", Family: "Standard_F_v2", CPUCores: 32, MemoryMiB: 65536, Architecture: "amd64"},
		{Name: "Standard_F48s_v2", Family: "Standard_F_v2", CPUCores: 48, MemoryMiB: 98304, Architecture: "amd64"},
		{Name: "Standard_F64s_v2", Family: "Standard_F_v2", CPUCores: 64, MemoryMiB: 131072, Architecture: "amd64"},
		{Name: "Standard_F72s_v2", Family: "Standard_F_v2", CPUCores: 72, MemoryMiB: 147456, Architecture: "amd64"},

		// L-series v2 (storage optimized)
		{Name: "Standard_L8s_v2", Family: "Standard_L_v2", CPUCores: 8, MemoryMiB: 65536, Architecture: "amd64"},
		{Name: "Standard_L16s_v2", Family: "Standard_L_v2", CPUCores: 16, MemoryMiB: 131072, Architecture: "amd64"},
		{Name: "Standard_L32s_v2", Family: "Standard_L_v2", CPUCores: 32, MemoryMiB: 262144, Architecture: "amd64"},
		{Name: "Standard_L48s_v2", Family: "Standard_L_v2", CPUCores: 48, MemoryMiB: 393216, Architecture: "amd64"},
		{Name: "Standard_L64s_v2", Family: "Standard_L_v2", CPUCores: 64, MemoryMiB: 524288, Architecture: "amd64"},
		{Name: "Standard_L80s_v2", Family: "Standard_L_v2", CPUCores: 80, MemoryMiB: 655360, Architecture: "amd64"},

		// L-series v3
		{Name: "Standard_L8s_v3", Family: "Standard_L_v3", CPUCores: 8, MemoryMiB: 65536, Architecture: "amd64"},
		{Name: "Standard_L16s_v3", Family: "Standard_L_v3", CPUCores: 16, MemoryMiB: 131072, Architecture: "amd64"},
		{Name: "Standard_L32s_v3", Family: "Standard_L_v3", CPUCores: 32, MemoryMiB: 262144, Architecture: "amd64"},
		{Name: "Standard_L48s_v3", Family: "Standard_L_v3", CPUCores: 48, MemoryMiB: 393216, Architecture: "amd64"},
		{Name: "Standard_L64s_v3", Family: "Standard_L_v3", CPUCores: 64, MemoryMiB: 524288, Architecture: "amd64"},
		{Name: "Standard_L80s_v3", Family: "Standard_L_v3", CPUCores: 80, MemoryMiB: 655360, Architecture: "amd64"},

		// M-series (memory-intensive)
		{Name: "Standard_M8ms", Family: "Standard_M", CPUCores: 8, MemoryMiB: 224256, Architecture: "amd64"},
		{Name: "Standard_M16ms", Family: "Standard_M", CPUCores: 16, MemoryMiB: 448512, Architecture: "amd64"},
		{Name: "Standard_M32ms", Family: "Standard_M", CPUCores: 32, MemoryMiB: 901120, Architecture: "amd64"},
		{Name: "Standard_M64ms", Family: "Standard_M", CPUCores: 64, MemoryMiB: 1802240, Architecture: "amd64"},
		{Name: "Standard_M128ms", Family: "Standard_M", CPUCores: 128, MemoryMiB: 3891200, Architecture: "amd64"},

		// NC-series (GPU compute - Tesla K80)
		{Name: "Standard_NC6", Family: "Standard_N", CPUCores: 6, MemoryMiB: 57344, GPUs: 1, GPUType: "NVIDIA Tesla K80", Architecture: "amd64"},
		{Name: "Standard_NC12", Family: "Standard_N", CPUCores: 12, MemoryMiB: 114688, GPUs: 2, GPUType: "NVIDIA Tesla K80", Architecture: "amd64"},
		{Name: "Standard_NC24", Family: "Standard_N", CPUCores: 24, MemoryMiB: 229376, GPUs: 4, GPUType: "NVIDIA Tesla K80", Architecture: "amd64"},

		// NC v3 series (GPU compute - Tesla V100)
		{Name: "Standard_NC6s_v3", Family: "Standard_N_v3", CPUCores: 6, MemoryMiB: 114688, GPUs: 1, GPUType: "NVIDIA Tesla V100", Architecture: "amd64"},
		{Name: "Standard_NC12s_v3", Family: "Standard_N_v3", CPUCores: 12, MemoryMiB: 229376, GPUs: 2, GPUType: "NVIDIA Tesla V100", Architecture: "amd64"},
		{Name: "Standard_NC24s_v3", Family: "Standard_N_v3", CPUCores: 24, MemoryMiB: 458752, GPUs: 4, GPUType: "NVIDIA Tesla V100", Architecture: "amd64"},

		// NC A100 v4 series
		{Name: "Standard_NC24ads_A100_v4", Family: "Standard_N_v4", CPUCores: 24, MemoryMiB: 229376, GPUs: 1, GPUType: "NVIDIA A100 80GB", Architecture: "amd64"},
		{Name: "Standard_NC48ads_A100_v4", Family: "Standard_N_v4", CPUCores: 48, MemoryMiB: 458752, GPUs: 2, GPUType: "NVIDIA A100 80GB", Architecture: "amd64"},
		{Name: "Standard_NC96ads_A100_v4", Family: "Standard_N_v4", CPUCores: 96, MemoryMiB: 917504, GPUs: 4, GPUType: "NVIDIA A100 80GB", Architecture: "amd64"},

		// ND A100 v4 series
		{Name: "Standard_ND96asr_A100_v4", Family: "Standard_N_v4", CPUCores: 96, MemoryMiB: 917504, GPUs: 8, GPUType: "NVIDIA A100 80GB", Architecture: "amd64"},

		// ND H100 v5 series
		{Name: "Standard_ND96isr_H100_v5", Family: "Standard_N_v5", CPUCores: 96, MemoryMiB: 1884160, GPUs: 8, GPUType: "NVIDIA H100 80GB", Architecture: "amd64"},

		// NV-series v3 (GPU visualization)
		{Name: "Standard_NV12s_v3", Family: "Standard_N_v3", CPUCores: 12, MemoryMiB: 114688, GPUs: 1, GPUType: "NVIDIA Tesla M60", Architecture: "amd64"},
		{Name: "Standard_NV24s_v3", Family: "Standard_N_v3", CPUCores: 24, MemoryMiB: 229376, GPUs: 2, GPUType: "NVIDIA Tesla M60", Architecture: "amd64"},
		{Name: "Standard_NV48s_v3", Family: "Standard_N_v3", CPUCores: 48, MemoryMiB: 458752, GPUs: 4, GPUType: "NVIDIA Tesla M60", Architecture: "amd64"},

		// NV A10 v5 series
		{Name: "Standard_NV6ads_A10_v5", Family: "Standard_N_v5", CPUCores: 6, MemoryMiB: 57344, GPUs: 1, GPUType: "NVIDIA A10", Architecture: "amd64"},
		{Name: "Standard_NV12ads_A10_v5", Family: "Standard_N_v5", CPUCores: 12, MemoryMiB: 114688, GPUs: 1, GPUType: "NVIDIA A10", Architecture: "amd64"},
		{Name: "Standard_NV18ads_A10_v5", Family: "Standard_N_v5", CPUCores: 18, MemoryMiB: 229376, GPUs: 1, GPUType: "NVIDIA A10", Architecture: "amd64"},
		{Name: "Standard_NV36ads_A10_v5", Family: "Standard_N_v5", CPUCores: 36, MemoryMiB: 458752, GPUs: 1, GPUType: "NVIDIA A10", Architecture: "amd64"},
		{Name: "Standard_NV36adms_A10_v5", Family: "Standard_N_v5", CPUCores: 36, MemoryMiB: 917504, GPUs: 1, GPUType: "NVIDIA A10", Architecture: "amd64"},
		{Name: "Standard_NV72ads_A10_v5", Family: "Standard_N_v5", CPUCores: 72, MemoryMiB: 917504, GPUs: 2, GPUType: "NVIDIA A10", Architecture: "amd64"},

		// NC T4 series
		{Name: "Standard_NC4as_T4_v3", Family: "Standard_N_v3", CPUCores: 4, MemoryMiB: 28672, GPUs: 1, GPUType: "NVIDIA T4", Architecture: "amd64"},
		{Name: "Standard_NC8as_T4_v3", Family: "Standard_N_v3", CPUCores: 8, MemoryMiB: 57344, GPUs: 1, GPUType: "NVIDIA T4", Architecture: "amd64"},
		{Name: "Standard_NC16as_T4_v3", Family: "Standard_N_v3", CPUCores: 16, MemoryMiB: 114688, GPUs: 1, GPUType: "NVIDIA T4", Architecture: "amd64"},
		{Name: "Standard_NC64as_T4_v3", Family: "Standard_N_v3", CPUCores: 64, MemoryMiB: 458752, GPUs: 4, GPUType: "NVIDIA T4", Architecture: "amd64"},

		// D-series v2 (general purpose, older generation)
		{Name: "Standard_D2_v2", Family: "Standard_D_v2", CPUCores: 2, MemoryMiB: 7168, Architecture: "amd64"},
		{Name: "Standard_D3_v2", Family: "Standard_D_v2", CPUCores: 4, MemoryMiB: 14336, Architecture: "amd64"},
		{Name: "Standard_D4_v2", Family: "Standard_D_v2", CPUCores: 8, MemoryMiB: 28672, Architecture: "amd64"},
		{Name: "Standard_D5_v2", Family: "Standard_D_v2", CPUCores: 16, MemoryMiB: 57344, Architecture: "amd64"},

		// A-series v2 (entry level)
		{Name: "Standard_A1_v2", Family: "Standard_A_v2", CPUCores: 1, MemoryMiB: 2048, Architecture: "amd64"},
		{Name: "Standard_A2_v2", Family: "Standard_A_v2", CPUCores: 2, MemoryMiB: 4096, Architecture: "amd64"},
		{Name: "Standard_A4_v2", Family: "Standard_A_v2", CPUCores: 4, MemoryMiB: 8192, Architecture: "amd64"},
		{Name: "Standard_A8_v2", Family: "Standard_A_v2", CPUCores: 8, MemoryMiB: 16384, Architecture: "amd64"},
		{Name: "Standard_A2m_v2", Family: "Standard_A_v2", CPUCores: 2, MemoryMiB: 16384, Architecture: "amd64"},
		{Name: "Standard_A4m_v2", Family: "Standard_A_v2", CPUCores: 4, MemoryMiB: 32768, Architecture: "amd64"},
		{Name: "Standard_A8m_v2", Family: "Standard_A_v2", CPUCores: 8, MemoryMiB: 65536, Architecture: "amd64"},

		// Dasv5 (AMD-based)
		{Name: "Standard_D2as_v5", Family: "Standard_D_v5", CPUCores: 2, MemoryMiB: 8192, Architecture: "amd64"},
		{Name: "Standard_D4as_v5", Family: "Standard_D_v5", CPUCores: 4, MemoryMiB: 16384, Architecture: "amd64"},
		{Name: "Standard_D8as_v5", Family: "Standard_D_v5", CPUCores: 8, MemoryMiB: 32768, Architecture: "amd64"},
		{Name: "Standard_D16as_v5", Family: "Standard_D_v5", CPUCores: 16, MemoryMiB: 65536, Architecture: "amd64"},
		{Name: "Standard_D32as_v5", Family: "Standard_D_v5", CPUCores: 32, MemoryMiB: 131072, Architecture: "amd64"},
		{Name: "Standard_D48as_v5", Family: "Standard_D_v5", CPUCores: 48, MemoryMiB: 196608, Architecture: "amd64"},
		{Name: "Standard_D64as_v5", Family: "Standard_D_v5", CPUCores: 64, MemoryMiB: 262144, Architecture: "amd64"},
		{Name: "Standard_D96as_v5", Family: "Standard_D_v5", CPUCores: 96, MemoryMiB: 393216, Architecture: "amd64"},

		// Easv5 (AMD memory-optimized)
		{Name: "Standard_E2as_v5", Family: "Standard_E_v5", CPUCores: 2, MemoryMiB: 16384, Architecture: "amd64"},
		{Name: "Standard_E4as_v5", Family: "Standard_E_v5", CPUCores: 4, MemoryMiB: 32768, Architecture: "amd64"},
		{Name: "Standard_E8as_v5", Family: "Standard_E_v5", CPUCores: 8, MemoryMiB: 65536, Architecture: "amd64"},
		{Name: "Standard_E16as_v5", Family: "Standard_E_v5", CPUCores: 16, MemoryMiB: 131072, Architecture: "amd64"},
		{Name: "Standard_E32as_v5", Family: "Standard_E_v5", CPUCores: 32, MemoryMiB: 262144, Architecture: "amd64"},
		{Name: "Standard_E48as_v5", Family: "Standard_E_v5", CPUCores: 48, MemoryMiB: 393216, Architecture: "amd64"},
		{Name: "Standard_E64as_v5", Family: "Standard_E_v5", CPUCores: 64, MemoryMiB: 516096, Architecture: "amd64"},
		{Name: "Standard_E96as_v5", Family: "Standard_E_v5", CPUCores: 96, MemoryMiB: 688128, Architecture: "amd64"},
	}
}

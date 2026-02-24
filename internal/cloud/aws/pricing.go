package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"sync"
	"time"

	awscfg "github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/pricing"
	pricingtypes "github.com/aws/aws-sdk-go-v2/service/pricing/types"

	"github.com/koptimizer/koptimizer/internal/store"
	intmetrics "github.com/koptimizer/koptimizer/internal/metrics"
	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
)

// awsComponentRates maps instance family prefixes to per-vCPU and per-GiB-RAM hourly rates.
// Based on published us-east-1 on-demand pricing (as of 2024-Q4).
// Used as FALLBACK when the AWS Pricing API is unavailable. These rates may
// become stale; when live pricing is available (the normal case), these are NOT used.
var awsComponentRates = map[string]struct{ cpuRate, memRate float64 }{
	"m5":   {0.048, 0.00643},
	"m5a":  {0.0432, 0.00579},
	"m5n":  {0.0594, 0.00796},
	"m5zn": {0.0826, 0.01108},
	"m6i":  {0.048, 0.00643},
	"m6a":  {0.0432, 0.00579},
	"m6g":  {0.0385, 0.00514},
	"m7i":  {0.0504, 0.00675},
	"m7a":  {0.0463, 0.00620},
	"m7g":  {0.0408, 0.00547},
	"c5":   {0.0425, 0.00569},
	"c5a":  {0.0383, 0.00513},
	"c5n":  {0.054, 0.00724},
	"c6i":  {0.0425, 0.00569},
	"c6a":  {0.0383, 0.00513},
	"c6g":  {0.034, 0.00456},
	"c7i":  {0.04462, 0.00598},
	"c7a":  {0.04089, 0.00548},
	"c7g":  {0.0361, 0.00484},
	"r5":   {0.063, 0.00844},
	"r5a":  {0.0567, 0.0076},
	"r5n":  {0.0744, 0.00998},
	"r6i":  {0.063, 0.00844},
	"r6a":  {0.0567, 0.0076},
	"r6g":  {0.0504, 0.00675},
	"r7i":  {0.06615, 0.00886},
	"r7a":  {0.06066, 0.00813},
	"r7g":  {0.0535, 0.00717},
	"t3":   {0.0416, 0.00557},
	"t3a":  {0.0374, 0.00502},
	"t4g":  {0.0336, 0.0045},
	"i3":   {0.156, 0.0209},
	"d3":   {0.1499, 0.02009},
}

// awsGPURates maps GPU types to per-GPU hourly rates.
var awsGPURates = map[string]float64{
	"V100":       2.448,
	"A100":       3.40,
	"A10G":       1.006,
	"T4":         0.526,
	"K80":        0.90,
	"H100":       6.98,
	"L4":         0.726,
	"L40S":       2.754,
	"Inferentia": 0.228,
	"Trainium":   1.343,
}

// PricingService handles AWS instance pricing lookups.
type PricingService struct {
	ec2Client      *ec2.Client
	pricingClient  *pricing.Client
	pricingCache   *store.PricingCache
	region         string
	mu             sync.RWMutex
	cache          map[string]*cloudprovider.PricingInfo
	typeCache      map[string][]*cloudprovider.InstanceType
	typeCacheTime  map[string]time.Time // region -> last refresh time
}

func NewPricingService(cfg awscfg.Config, pricingCache *store.PricingCache) *PricingService {
	// The AWS Pricing API is only available in us-east-1.
	pricingCfg, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion("us-east-1"))
	var pClient *pricing.Client
	if err == nil {
		pClient = pricing.NewFromConfig(pricingCfg)
	}

	return &PricingService{
		ec2Client:     ec2.NewFromConfig(cfg),
		pricingClient: pClient,
		pricingCache:  pricingCache,
		region:        cfg.Region,
		cache:         make(map[string]*cloudprovider.PricingInfo),
		typeCache:     make(map[string][]*cloudprovider.InstanceType),
		typeCacheTime: make(map[string]time.Time),
	}
}

// StartBackgroundRefresh starts a goroutine that proactively refreshes the
// pricing cache every 45 minutes so the first request after expiry doesn't
// incur the full API call latency (5-10s for the AWS Pricing API).
// The goroutine stops when ctx is cancelled.
func (s *PricingService) StartBackgroundRefresh(ctx context.Context) {
	const refreshInterval = 45 * time.Minute
	go func() {
		ticker := time.NewTicker(refreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.mu.RLock()
				regions := make([]string, 0, len(s.cache))
				for region := range s.cache {
					regions = append(regions, region)
				}
				s.mu.RUnlock()

				for _, region := range regions {
					if _, err := s.GetCurrentPricing(ctx, region); err != nil {
						slog.Warn("aws: background pricing refresh failed",
							"region", region, "error", err)
					}
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (s *PricingService) GetPrice(ctx context.Context, region, instanceType string) (float64, error) {
	pricing, err := s.GetCurrentPricing(ctx, region)
	if err != nil {
		return 0, err
	}
	price, ok := pricing.Prices[instanceType]
	if !ok {
		return 0, fmt.Errorf("no pricing found for %s in %s", instanceType, region)
	}
	return price, nil
}

func (s *PricingService) GetCurrentPricing(ctx context.Context, region string) (*cloudprovider.PricingInfo, error) {
	// Check in-memory cache (1h TTL).
	s.mu.RLock()
	if cached, ok := s.cache[region]; ok {
		if time.Since(cached.UpdatedAt) < 1*time.Hour {
			s.mu.RUnlock()
			return cached, nil
		}
	}
	s.mu.RUnlock()

	// Check SQLite cache (24h TTL).
	if s.pricingCache != nil {
		if prices, ok := s.pricingCache.Get("aws", region); ok && len(prices) > 0 {
			info := &cloudprovider.PricingInfo{
				Region:    region,
				Prices:    prices,
				UpdatedAt: time.Now(),
			}
			s.mu.Lock()
			s.cache[region] = info
			s.mu.Unlock()
			return info, nil
		}
	}

	// Try the real AWS Pricing API.
	if s.pricingClient != nil {
		realPrices, err := s.fetchRealPricing(ctx, region)
		if err != nil {
			slog.Warn("aws: pricing API failed, falling back to component-based estimates",
				"region", region, "error", err)
		}
		if err == nil && len(realPrices) > 0 {
			// Sanity-check prices: remove $0.00 or absurdly high values.
			if removed := store.SanitizePrices(realPrices); removed > 0 {
				slog.Warn("aws: removed invalid prices from API response",
					"region", region, "removed", removed)
			}

			info := &cloudprovider.PricingInfo{
				Region:    region,
				Prices:    realPrices,
				UpdatedAt: time.Now(),
			}

			s.mu.Lock()
			s.cache[region] = info
			s.mu.Unlock()

			// Persist to SQLite.
			if s.pricingCache != nil {
				s.pricingCache.Put("aws", region, realPrices)
			}
			intmetrics.PricingFallbackActive.WithLabelValues("aws", region).Set(0)
			intmetrics.PricingLastLiveUpdate.WithLabelValues("aws", region).Set(float64(time.Now().Unix()))
			return info, nil
		}
		// Fall through to component-based pricing on error.
	}

	// Fallback: build pricing from instance type descriptions using component rates.
	// These are hardcoded estimates (2024-Q4) and may be stale.
	slog.Warn("aws: using fallback component-based pricing (hardcoded 2024-Q4 rates)",
		"region", region)
	intmetrics.PricingFallbackTotal.WithLabelValues("aws", region).Inc()
	intmetrics.PricingFallbackActive.WithLabelValues("aws", region).Set(1)

	types, err := s.GetInstanceTypes(ctx, region)
	if err != nil {
		return nil, fmt.Errorf("getting instance types for pricing: %w", err)
	}

	prices := make(map[string]float64, len(types))
	for _, t := range types {
		prices[t.Name] = t.PricePerHour
	}

	info := &cloudprovider.PricingInfo{
		Region:    region,
		Prices:    prices,
		UpdatedAt: time.Now(),
	}

	s.mu.Lock()
	s.cache[region] = info
	s.mu.Unlock()

	return info, nil
}

// fetchRealPricing calls the AWS Pricing API GetProducts to get real per-instance-type
// hourly on-demand prices for the given region.
func (s *PricingService) fetchRealPricing(ctx context.Context, region string) (map[string]float64, error) {
	prices := make(map[string]float64)

	filters := []pricingtypes.Filter{
		{Type: pricingtypes.FilterTypeTermMatch, Field: awscfg.String("ServiceCode"), Value: awscfg.String("AmazonEC2")},
		{Type: pricingtypes.FilterTypeTermMatch, Field: awscfg.String("regionCode"), Value: awscfg.String(region)},
		{Type: pricingtypes.FilterTypeTermMatch, Field: awscfg.String("operatingSystem"), Value: awscfg.String("Linux")},
		{Type: pricingtypes.FilterTypeTermMatch, Field: awscfg.String("tenancy"), Value: awscfg.String("Shared")},
		{Type: pricingtypes.FilterTypeTermMatch, Field: awscfg.String("preInstalledSw"), Value: awscfg.String("NA")},
		{Type: pricingtypes.FilterTypeTermMatch, Field: awscfg.String("capacitystatus"), Value: awscfg.String("Used")},
	}

	input := &pricing.GetProductsInput{
		ServiceCode: awscfg.String("AmazonEC2"),
		Filters:     filters,
		MaxResults:  awscfg.Int32(100),
	}

	const maxPages = 200 // safety limit to prevent unbounded pagination
	paginator := pricing.NewGetProductsPaginator(s.pricingClient, input)
	for page := 0; paginator.HasMorePages() && page < maxPages; page++ {
		if page == maxPages-1 {
			slog.Warn("aws: pricing API pagination hit safety limit, data may be incomplete",
				"maxPages", maxPages, "region", region)
		}
		pageResult, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("getting pricing products: %w", err)
		}

		for _, priceListJSON := range pageResult.PriceList {
			instanceType, hourlyPrice, ok := parseAWSPriceListItem(priceListJSON)
			if !ok {
				continue
			}
			// Keep the first (or lowest) price per instance type.
			if existing, found := prices[instanceType]; !found || hourlyPrice < existing {
				prices[instanceType] = hourlyPrice
			}
		}
	}

	return prices, nil
}

// parseAWSPriceListItem parses a single PriceList JSON string from the AWS Pricing API
// and extracts the instance type and hourly on-demand USD price.
func parseAWSPriceListItem(priceJSON string) (instanceType string, price float64, ok bool) {
	var item struct {
		Product struct {
			Attributes struct {
				InstanceType string `json:"instanceType"`
			} `json:"attributes"`
		} `json:"product"`
		Terms struct {
			OnDemand map[string]struct {
				PriceDimensions map[string]struct {
					Unit         string            `json:"unit"`
					PricePerUnit map[string]string `json:"pricePerUnit"`
				} `json:"priceDimensions"`
			} `json:"OnDemand"`
		} `json:"terms"`
	}

	if err := json.Unmarshal([]byte(priceJSON), &item); err != nil {
		return "", 0, false
	}

	instanceType = item.Product.Attributes.InstanceType
	if instanceType == "" {
		return "", 0, false
	}

	for _, offer := range item.Terms.OnDemand {
		for _, dim := range offer.PriceDimensions {
			if dim.Unit != "Hrs" {
				continue
			}
			usdStr, exists := dim.PricePerUnit["USD"]
			if !exists {
				continue
			}
			p, err := strconv.ParseFloat(usdStr, 64)
			if err != nil || p <= 0 {
				continue
			}
			return instanceType, p, true
		}
	}

	return "", 0, false
}

func (s *PricingService) GetInstanceTypes(ctx context.Context, region string) ([]*cloudprovider.InstanceType, error) {
	s.mu.RLock()
	if cached, ok := s.typeCache[region]; ok {
		if t, tok := s.typeCacheTime[region]; tok && time.Since(t) < 1*time.Hour {
			s.mu.RUnlock()
			return cached, nil
		}
	}
	s.mu.RUnlock()

	// Try to get real prices to enrich instance types.
	var realPrices map[string]float64
	if s.pricingCache != nil {
		realPrices, _ = s.pricingCache.Get("aws", region)
	}
	if len(realPrices) == 0 && s.pricingClient != nil {
		rp, err := s.fetchRealPricing(ctx, region)
		if err == nil {
			realPrices = rp
		}
	}

	var types []*cloudprovider.InstanceType
	paginator := ec2.NewDescribeInstanceTypesPaginator(s.ec2Client, &ec2.DescribeInstanceTypesInput{})

	const itMaxPages = 50 // safety limit for instance type pagination
	for pageNum := 0; paginator.HasMorePages() && pageNum < itMaxPages; pageNum++ {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("describing instance types: %w", err)
		}

		for _, it := range page.InstanceTypes {
			name := string(it.InstanceType)
			family := extractAWSFamily(name)
			gpus := 0
			gpuType := ""
			if it.GpuInfo != nil && len(it.GpuInfo.Gpus) > 0 {
				for _, g := range it.GpuInfo.Gpus {
					if g.Count != nil {
						gpus += int(*g.Count)
					}
					if g.Name != nil {
						gpuType = *g.Name
					}
				}
			}

			arch := "amd64"
			for _, a := range it.ProcessorInfo.SupportedArchitectures {
				if a == ec2types.ArchitectureTypeArm64 {
					arch = "arm64"
					break
				}
			}

			cpuCores := 0
			if it.VCpuInfo != nil && it.VCpuInfo.DefaultVCpus != nil {
				cpuCores = int(*it.VCpuInfo.DefaultVCpus)
			}
			memMiB := 0
			if it.MemoryInfo != nil && it.MemoryInfo.SizeInMiB != nil {
				memMiB = int(*it.MemoryInfo.SizeInMiB)
			}

			// Prefer real API pricing; fall back to component-based estimate.
			var estimatedPrice float64
			if rp, ok := realPrices[name]; ok {
				estimatedPrice = rp
			} else {
				estimatedPrice = computeAWSPrice(family, cpuCores, memMiB, gpus, gpuType)
			}

			types = append(types, &cloudprovider.InstanceType{
				Name:         name,
				Family:       family,
				CPUCores:     cpuCores,
				MemoryMiB:    memMiB,
				GPUs:         gpus,
				GPUType:      gpuType,
				Architecture: arch,
				PricePerHour: estimatedPrice,
			})
		}
	}

	s.mu.Lock()
	s.typeCache[region] = types
	s.typeCacheTime[region] = time.Now()
	s.mu.Unlock()

	return types, nil
}

func extractAWSFamily(instanceType string) string {
	for i, c := range instanceType {
		if c == '.' {
			return instanceType[:i]
		}
	}
	return instanceType
}

func computeAWSPrice(family string, cpuCores, memMiB, gpus int, gpuType string) float64 {
	rates, ok := awsComponentRates[family]
	if !ok {
		// Fallback for unknown families
		rates = struct{ cpuRate, memRate float64 }{0.048, 0.00643} // m5 rates as default
	}
	price := float64(cpuCores)*rates.cpuRate + float64(memMiB)/1024*rates.memRate
	if gpus > 0 {
		gpuRate, ok := awsGPURates[gpuType]
		if !ok {
			gpuRate = 1.0 // conservative default for unknown GPU types
		}
		price += float64(gpus) * gpuRate
	}
	// Round to 4 decimal places
	return math.Round(price*10000) / 10000
}

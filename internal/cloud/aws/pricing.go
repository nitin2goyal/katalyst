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
// Based on approximate 3-year convertible reserved pricing (~50% of on-demand, us-east-1).
// Used as FALLBACK when the AWS Pricing API is unavailable. These rates may
// become stale; when live pricing is available (the normal case), these are NOT used.
var awsComponentRates = map[string]struct{ cpuRate, memRate float64 }{
	"m5":   {0.024, 0.00322},
	"m5a":  {0.0216, 0.0029},
	"m5n":  {0.0297, 0.00398},
	"m5zn": {0.0413, 0.00554},
	"m6i":  {0.024, 0.00322},
	"m6a":  {0.0216, 0.0029},
	"m6g":  {0.0193, 0.00257},
	"m7i":  {0.0252, 0.00338},
	"m7a":  {0.0232, 0.0031},
	"m7g":  {0.0204, 0.00274},
	"c5":   {0.0213, 0.00285},
	"c5a":  {0.0192, 0.00257},
	"c5n":  {0.027, 0.00362},
	"c6i":  {0.0213, 0.00285},
	"c6a":  {0.0192, 0.00257},
	"c6g":  {0.017, 0.00228},
	"c7i":  {0.0223, 0.00299},
	"c7a":  {0.0204, 0.00274},
	"c7g":  {0.0181, 0.00242},
	"r5":   {0.0315, 0.00422},
	"r5a":  {0.0284, 0.0038},
	"r5n":  {0.0372, 0.00499},
	"r6i":  {0.0315, 0.00422},
	"r6a":  {0.0284, 0.0038},
	"r6g":  {0.0252, 0.00338},
	"r7i":  {0.0331, 0.00443},
	"r7a":  {0.0303, 0.00407},
	"r7g":  {0.0268, 0.00359},
	"t3":   {0.0208, 0.00279},
	"t3a":  {0.0187, 0.00251},
	"t4g":  {0.0168, 0.00225},
	"i3":   {0.078, 0.01045},
	"d3":   {0.075, 0.01005},
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

// fetchRealPricing calls the AWS Pricing API GetProducts to get per-instance-type
// hourly prices for the given region. It prefers 3-year convertible no-upfront
// reserved pricing; if unavailable for a type, it halves the on-demand price.
func (s *PricingService) fetchRealPricing(ctx context.Context, region string) (map[string]float64, error) {
	onDemandPrices := make(map[string]float64)
	reservedPrices := make(map[string]float64) // 3yr convertible

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
			instanceType, onDemand, reserved, ok := parseAWSPriceListItemFull(priceListJSON)
			if !ok {
				continue
			}
			if onDemand > 0 {
				if existing, found := onDemandPrices[instanceType]; !found || onDemand < existing {
					onDemandPrices[instanceType] = onDemand
				}
			}
			if reserved > 0 {
				if existing, found := reservedPrices[instanceType]; !found || reserved < existing {
					reservedPrices[instanceType] = reserved
				}
			}
		}
	}

	// Build effective prices: prefer 3yr convertible reserved, else on-demand / 2
	prices := make(map[string]float64, len(onDemandPrices))
	for it, od := range onDemandPrices {
		if rp, ok := reservedPrices[it]; ok {
			prices[it] = rp
		} else {
			prices[it] = od / 2
		}
	}
	// Include any reserved-only entries (unlikely but safe)
	for it, rp := range reservedPrices {
		if _, ok := prices[it]; !ok {
			prices[it] = rp
		}
	}

	return prices, nil
}

// parseAWSPriceListItemFull parses a single PriceList JSON string from the AWS Pricing API
// and extracts the instance type, hourly on-demand price, and 3-year convertible
// no-upfront reserved hourly price (if available).
func parseAWSPriceListItemFull(priceJSON string) (instanceType string, onDemandPrice, reservedPrice float64, ok bool) {
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
			Reserved map[string]struct {
				TermAttributes struct {
					LeaseContractLength string `json:"LeaseContractLength"`
					OfferingClass       string `json:"OfferingClass"`
					PurchaseOption      string `json:"PurchaseOption"`
				} `json:"termAttributes"`
				PriceDimensions map[string]struct {
					Unit         string            `json:"unit"`
					PricePerUnit map[string]string `json:"pricePerUnit"`
				} `json:"priceDimensions"`
			} `json:"Reserved"`
		} `json:"terms"`
	}

	if err := json.Unmarshal([]byte(priceJSON), &item); err != nil {
		return "", 0, 0, false
	}

	instanceType = item.Product.Attributes.InstanceType
	if instanceType == "" {
		return "", 0, 0, false
	}

	// Extract on-demand hourly price
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
			onDemandPrice = p
			break
		}
		if onDemandPrice > 0 {
			break
		}
	}

	// Extract 3-year convertible no-upfront reserved hourly price
	for _, offer := range item.Terms.Reserved {
		attrs := offer.TermAttributes
		if attrs.LeaseContractLength != "3yr" ||
			attrs.OfferingClass != "convertible" ||
			attrs.PurchaseOption != "No Upfront" {
			continue
		}
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
			if reservedPrice == 0 || p < reservedPrice {
				reservedPrice = p
			}
		}
	}

	if onDemandPrice == 0 && reservedPrice == 0 {
		return "", 0, 0, false
	}
	return instanceType, onDemandPrice, reservedPrice, true
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
		// Fallback for unknown families (m5 3yr convertible reserved rates)
		rates = struct{ cpuRate, memRate float64 }{0.024, 0.00322}
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

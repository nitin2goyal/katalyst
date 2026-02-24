package aws

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
)

// GetSpotPricing returns current spot prices for the given instance types.
func (p *Provider) GetSpotPricing(ctx context.Context, region string, instanceTypes []string) ([]*cloudprovider.SpotInstanceInfo, error) {
	var itFilters []ec2types.InstanceType
	for _, it := range instanceTypes {
		itFilters = append(itFilters, ec2types.InstanceType(it))
	}

	// Get on-demand prices for comparison
	onDemandPrices := make(map[string]float64)
	for _, it := range instanceTypes {
		if price, err := p.pricing.GetPrice(ctx, region, it); err == nil {
			onDemandPrices[it] = price
		}
	}

	// Use paginator to handle large result sets
	type key struct {
		instanceType string
		az           string
	}
	latest := make(map[key]*ec2types.SpotPrice)

	const maxPages = 50 // safety limit to prevent unbounded pagination
	paginator := ec2.NewDescribeSpotPriceHistoryPaginator(p.ec2Client, &ec2.DescribeSpotPriceHistoryInput{
		InstanceTypes:       itFilters,
		ProductDescriptions: []string{"Linux/UNIX"},
		StartTime:           aws.Time(time.Now().Add(-1 * time.Hour)),
	})
	for pageNum := 0; paginator.HasMorePages() && pageNum < maxPages; pageNum++ {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("describing spot price history: %w", err)
		}

		// Deduplicate: keep latest price per instance-type + AZ
		for i := range page.SpotPriceHistory {
			sp := &page.SpotPriceHistory[i]
			k := key{instanceType: string(sp.InstanceType), az: *sp.AvailabilityZone}
			existing, ok := latest[k]
			if !ok || sp.Timestamp.After(*existing.Timestamp) {
				latest[k] = sp
			}
		}
	}

	var result []*cloudprovider.SpotInstanceInfo
	for k, sp := range latest {
		var spotPrice float64
		if sp.SpotPrice != nil {
			if _, err := fmt.Sscanf(*sp.SpotPrice, "%f", &spotPrice); err != nil {
				spotPrice = 0
			}
		}

		odPrice := onDemandPrices[k.instanceType]
		savingsPct := 0.0
		if odPrice > 0 {
			savingsPct = (odPrice - spotPrice) / odPrice * 100
		}

		result = append(result, &cloudprovider.SpotInstanceInfo{
			InstanceType:     k.instanceType,
			AvailabilityZone: k.az,
			SpotPrice:        spotPrice,
			OnDemandPrice:    odPrice,
			SavingsPercent:   savingsPct,
		})
	}

	return result, nil
}

// GetSpotInterruptionRate returns estimated interruption rates for instance types.
// AWS doesn't provide an API for this directly; we use the Spot Placement Score
// as a proxy indicator. Higher placement scores indicate lower interruption risk.
func (p *Provider) GetSpotInterruptionRate(ctx context.Context, region string, instanceTypes []string) (map[string]float64, error) {
	rates := make(map[string]float64)

	// Fallback: estimate interruption rates based on instance family popularity
	// Larger, less common instance types tend to have lower interruption rates
	for _, it := range instanceTypes {
		family := extractAWSFamily(it)
		rate := estimateInterruptionRate(family)
		rates[it] = rate
	}

	return rates, nil
}

// estimateSpotDiscount returns an estimated spot discount fraction for an AWS
// instance family.  Used as a fallback when GetPrice returns on-demand rates
// and there is no live spot price available.
func estimateSpotDiscount(family string) float64 {
	discountByFamily := map[string]float64{
		"m5": 0.70, "m5a": 0.70, "m5n": 0.65, "m5zn": 0.60,
		"m6i": 0.70, "m6a": 0.70, "m6g": 0.72,
		"m7i": 0.68, "m7a": 0.68, "m7g": 0.70,
		"c5": 0.70, "c5a": 0.70, "c5n": 0.65,
		"c6i": 0.70, "c6a": 0.70, "c6g": 0.72,
		"c7i": 0.68, "c7a": 0.68, "c7g": 0.70,
		"r5": 0.70, "r5a": 0.70, "r5n": 0.65,
		"r6i": 0.70, "r6a": 0.70, "r6g": 0.72,
		"r7i": 0.68, "r7a": 0.68, "r7g": 0.70,
		"t3": 0.70, "t3a": 0.70,
		"p3": 0.60, "p4d": 0.60, "p5": 0.55,
		"g4dn": 0.60, "g5": 0.60, "g6": 0.58,
		"i3": 0.65, "i3en": 0.65, "i4i": 0.63,
		"d2": 0.65, "d3": 0.63,
	}
	if d, ok := discountByFamily[family]; ok {
		return d
	}
	return 0.70 // default ~70% discount
}

// estimateInterruptionRate provides estimated interruption frequency based on
// historical data about instance family popularity. More popular families
// (m5, c5, r5) tend to have higher interruption rates due to demand spikes.
func estimateInterruptionRate(family string) float64 {
	// Based on public analyses of AWS Spot Advisor data
	highInterrupt := map[string]bool{
		"m5": true, "m5a": true, "c5": true, "c5a": true,
		"r5": true, "r5a": true, "t3": true, "t3a": true,
	}
	medInterrupt := map[string]bool{
		"m6i": true, "m6a": true, "c6i": true, "c6a": true,
		"r6i": true, "r6a": true, "m5zn": true,
	}
	lowInterrupt := map[string]bool{
		"m7i": true, "m7a": true, "m7g": true,
		"c7i": true, "c7a": true, "c7g": true,
		"r7i": true, "r7a": true, "r7g": true,
		"m6g": true, "c6g": true, "r6g": true,
	}

	if highInterrupt[family] {
		return 15.0 // ~15% monthly interruption rate
	}
	if medInterrupt[family] {
		return 8.0 // ~8%
	}
	if lowInterrupt[family] {
		return 3.0 // ~3%
	}
	return 10.0 // default moderate rate
}

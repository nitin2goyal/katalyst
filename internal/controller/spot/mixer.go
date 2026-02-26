package spot

import (
	"context"
	"fmt"

	intmetrics "github.com/koptimizer/koptimizer/internal/metrics"
	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
	"github.com/koptimizer/koptimizer/pkg/cost"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// Mixer analyzes node groups and recommends optimal spot/on-demand mixes.
// It considers current spot pricing, interruption rates, and configured
// constraints to maximize savings while maintaining reliability.
type Mixer struct {
	provider     cloudprovider.CloudProvider
	spotProvider cloudprovider.SpotProvider // may be nil if provider doesn't support spot
	config       *config.Config
}

func NewMixer(provider cloudprovider.CloudProvider, cfg *config.Config) *Mixer {
	var sp cloudprovider.SpotProvider
	if p, ok := provider.(cloudprovider.SpotProvider); ok {
		sp = p
	}
	return &Mixer{provider: provider, spotProvider: sp, config: cfg}
}

func (m *Mixer) Analyze(ctx context.Context, snapshot *optimizer.ClusterSnapshot) ([]optimizer.Recommendation, error) {
	var recs []optimizer.Recommendation

	spotCount := 0
	onDemandCount := 0
	var spotSavingsMonthly float64

	// Get real spot pricing if provider supports it
	var spotPricingMap map[string]float64    // instanceType -> spotPrice
	var onDemandPricingMap map[string]float64 // instanceType -> onDemandPrice
	if m.spotProvider != nil {
		var instanceTypes []string
		seen := make(map[string]bool)
		for _, node := range snapshot.Nodes {
			if !node.IsGPUNode && node.Node.Labels != nil {
				it, _ := m.provider.GetNodeInstanceType(ctx, node.Node)
				if it != "" && !seen[it] {
					instanceTypes = append(instanceTypes, it)
					seen[it] = true
				}
			}
		}
		if len(instanceTypes) > 0 {
			spotInfos, err := m.spotProvider.GetSpotPricing(ctx, m.config.Region, instanceTypes)
			if err == nil {
				spotPricingMap = make(map[string]float64)
				onDemandPricingMap = make(map[string]float64)
				for _, si := range spotInfos {
					// Keep the best (lowest) spot price per instance type
					if existing, ok := spotPricingMap[si.InstanceType]; !ok || si.SpotPrice < existing {
						spotPricingMap[si.InstanceType] = si.SpotPrice
					}
					if si.OnDemandPrice > 0 {
						onDemandPricingMap[si.InstanceType] = si.OnDemandPrice
					}
				}
			}
		}
	}

	for _, node := range snapshot.Nodes {
		if node.IsGPUNode {
			continue // GPU nodes stay on-demand for reliability
		}

		isSpot := cloudprovider.IsSpotNode(node.Node)

		if isSpot {
			spotCount++
			// Calculate savings: on-demand price - spot price.
			// node.HourlyCostUSD already reflects spot-discounted cost,
			// so use on-demand pricing for the savings comparison.
			it, _ := m.provider.GetNodeInstanceType(ctx, node.Node)
			if onDemandPricingMap != nil {
				if odPrice, ok := onDemandPricingMap[it]; ok {
					spotSavingsMonthly += (odPrice - node.HourlyCostUSD) * cost.HoursPerMonth
				} else {
					// No on-demand price — reverse-engineer from spot cost using
					// per-provider, per-family discount estimate.
					discount := m.estimateDiscount(it)
					if discount > 0 && discount < 1 {
						odEquiv := node.HourlyCostUSD / (1 - discount)
						spotSavingsMonthly += (odEquiv - node.HourlyCostUSD) * cost.HoursPerMonth
					}
				}
			} else {
				// No spot pricing data — reverse-engineer savings using
				// per-provider, per-family discount estimate.
				discount := m.estimateDiscount(it)
				if discount > 0 && discount < 1 {
					odEquiv := node.HourlyCostUSD / (1 - discount)
					spotSavingsMonthly += (odEquiv - node.HourlyCostUSD) * cost.HoursPerMonth
				}
			}
		} else {
			onDemandCount++
		}
	}

	totalNodes := spotCount + onDemandCount
	if totalNodes == 0 {
		return nil, nil
	}

	intmetrics.SpotNodesTotal.Set(float64(spotCount))
	intmetrics.SpotSavingsUSD.Set(spotSavingsMonthly)

	currentSpotPct := float64(spotCount) / float64(totalNodes) * 100
	maxSpotPct := float64(m.config.Spot.MaxSpotPercentage)

	// Recommend converting on-demand to spot if below target
	if currentSpotPct < maxSpotPct && onDemandCount > 0 {
		// How many more nodes could be spot?
		targetSpotCount := int(float64(totalNodes) * maxSpotPct / 100)
		additionalSpot := targetSpotCount - spotCount
		if additionalSpot > onDemandCount {
			additionalSpot = onDemandCount
		}
		if additionalSpot > 0 {
			// Estimate savings for the recommended conversion
			var avgHourlyCost float64
			for _, node := range snapshot.Nodes {
				if !node.IsGPUNode {
					avgHourlyCost += node.HourlyCostUSD
				}
			}
			avgHourlyCost /= float64(totalNodes)

			// Calculate average hourly savings per node using absolute dollars.
			// Compare on-demand price to spot price to get real savings.
			var avgHourlySavingsPerNode float64
			if spotPricingMap != nil && onDemandPricingMap != nil && len(spotPricingMap) > 0 {
				var totalSavingsDollars float64
				count := 0
				for _, node := range snapshot.Nodes {
					if node.IsGPUNode {
						continue
					}
					it, _ := m.provider.GetNodeInstanceType(ctx, node.Node)
					spotPrice, hasSpot := spotPricingMap[it]
					odPrice, hasOD := onDemandPricingMap[it]
					if hasSpot && hasOD && odPrice > 0 {
						totalSavingsDollars += odPrice - spotPrice
						count++
					}
				}
				if count > 0 {
					avgHourlySavingsPerNode = totalSavingsDollars / float64(count)
				} else {
					avgHourlySavingsPerNode = avgHourlyCost * m.estimateDiscount("")
				}
			} else {
				avgHourlySavingsPerNode = avgHourlyCost * m.estimateDiscount("")
			}
			monthlySavings := float64(additionalSpot) * avgHourlySavingsPerNode * cost.HoursPerMonth

			recs = append(recs, optimizer.Recommendation{
				ID:             "spot-mix-increase",
				Type:           optimizer.RecommendationSpotOptimize,
				Priority:       optimizer.PriorityMedium,
				AutoExecutable: false, // Requires manual review for initial setup
				TargetKind:     "Cluster",
				TargetName:     m.config.ClusterName,
				Summary:        fmt.Sprintf("Increase spot instances from %d%% to %d%% (%d additional nodes)", int(currentSpotPct), int(maxSpotPct), additionalSpot),
				ActionSteps: []string{
					fmt.Sprintf("Convert %d on-demand nodes to spot instances", additionalSpot),
					"Ensure node groups have mixed instance policies with spot diversity",
					"Configure interruption handling for graceful workload migration",
				},
				EstimatedSaving: optimizer.SavingEstimate{
					MonthlySavingsUSD: monthlySavings,
					AnnualSavingsUSD:  monthlySavings * 12,
					Currency:          "USD",
				},
				EstimatedImpact: optimizer.ImpactEstimate{
					MonthlyCostChangeUSD: -monthlySavings,
					NodesAffected:        additionalSpot,
					RiskLevel:            "medium",
				},
				Details: map[string]string{
					"action":          "adjust-spot-mix",
					"currentSpotPct":  fmt.Sprintf("%d", int(currentSpotPct)),
					"targetSpotPct":   fmt.Sprintf("%d", int(maxSpotPct)),
					"additionalNodes": fmt.Sprintf("%d", additionalSpot),
				},
			})
		}
	}

	// Check for node groups that are 100% on-demand but could use spot
	for _, ng := range snapshot.NodeGroups {
		if ng.Lifecycle == "on-demand" && ng.CurrentCount > 1 {
			recs = append(recs, optimizer.Recommendation{
				ID:             fmt.Sprintf("spot-convert-%s", ng.ID),
				Type:           optimizer.RecommendationSpotOptimize,
				Priority:       optimizer.PriorityLow,
				AutoExecutable: false,
				TargetKind:     "NodeGroup",
				TargetName:     ng.Name,
				Summary:        fmt.Sprintf("Node group %s (%d nodes) is fully on-demand — consider mixed spot/OD", ng.Name, ng.CurrentCount),
				ActionSteps: []string{
					fmt.Sprintf("Enable mixed instances policy on node group %s", ng.Name),
					"Set spot allocation strategy to capacity-optimized-prioritized",
					fmt.Sprintf("Configure at least %d diverse instance types", m.config.Spot.DiversityMinTypes),
				},
				Details: map[string]string{
					"action":      "convert-to-spot",
					"nodeGroupID": ng.ID,
				},
			})
		}
	}

	return recs, nil
}

// estimateDiscount returns the spot discount fraction for an instance type using
// the provider's per-family estimates. Falls back to 0.65 if the provider does
// not implement SpotDiscountEstimator.
func (m *Mixer) estimateDiscount(instanceType string) float64 {
	if sde, ok := m.provider.(cloudprovider.SpotDiscountEstimator); ok {
		return sde.EstimateSpotDiscount(instanceType)
	}
	return 0.65 // conservative fallback
}

func (m *Mixer) Execute(ctx context.Context, rec optimizer.Recommendation) error {
	// Spot mix changes require cloud provider API calls to modify ASG/MIG/VMSS
	// launch templates. This is a significant infrastructure change so we
	// generate recommendations but don't auto-execute.
	return nil
}

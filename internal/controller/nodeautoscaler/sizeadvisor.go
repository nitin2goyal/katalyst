package nodeautoscaler

import (
	"context"
	"fmt"
	"time"

	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
	"github.com/koptimizer/koptimizer/pkg/cost"
	"github.com/koptimizer/koptimizer/pkg/familylock"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// SizeAdvisor recommends instance size changes within the same family.
type SizeAdvisor struct {
	provider cloudprovider.CloudProvider
	guard    *familylock.FamilyLockGuard
}

func NewSizeAdvisor(provider cloudprovider.CloudProvider, guard *familylock.FamilyLockGuard) *SizeAdvisor {
	return &SizeAdvisor{
		provider: provider,
		guard:    guard,
	}
}

func (s *SizeAdvisor) Analyze(ctx context.Context, snapshot *optimizer.ClusterSnapshot) ([]optimizer.Recommendation, error) {
	var recs []optimizer.Recommendation

	// Group nodes by node group
	nodesByGroup := make(map[string][]optimizer.NodeInfo)
	for _, n := range snapshot.Nodes {
		if n.NodeGroup != "" {
			nodesByGroup[n.NodeGroup] = append(nodesByGroup[n.NodeGroup], n)
		}
	}

	for _, ng := range snapshot.NodeGroups {
		nodes := nodesByGroup[ng.ID]
		if len(nodes) == 0 {
			continue
		}

		// Calculate average utilization
		avgCPUUtil := 0.0
		avgMemUtil := 0.0
		for _, n := range nodes {
			if n.CPUCapacity > 0 {
				avgCPUUtil += float64(n.CPUUsed) / float64(n.CPUCapacity)
			}
			if n.MemoryCapacity > 0 {
				avgMemUtil += float64(n.MemoryUsed) / float64(n.MemoryCapacity)
			}
		}
		avgCPUUtil = avgCPUUtil / float64(len(nodes)) * 100
		avgMemUtil = avgMemUtil / float64(len(nodes)) * 100

		// Get available sizes within family
		familySizes, err := s.guard.GetAllowedSizes(ctx, ng.InstanceType)
		if err != nil || len(familySizes) == 0 {
			continue
		}

		// If consistently high utilization (>85%), suggest sizing up
		if avgCPUUtil > 85 || avgMemUtil > 85 {
			nextSize := findNextLargerSize(ng.InstanceType, familySizes)
			if nextSize != "" {
				recs = append(recs, optimizer.Recommendation{
					ID:             fmt.Sprintf("sizeup-%s-%d", ng.ID, time.Now().Unix()),
					Type:           optimizer.RecommendationNodeGroupAdjust,
					Priority:       optimizer.PriorityMedium,
					AutoExecutable: false, // Size changes require human action
					TargetKind:     "NodeGroup",
					TargetName:     ng.Name,
					Summary:        fmt.Sprintf("Consider upsizing %s from %s to %s (avg CPU: %.0f%%, avg mem: %.0f%%)", ng.Name, ng.InstanceType, nextSize, avgCPUUtil, avgMemUtil),
					ActionSteps: []string{
						fmt.Sprintf("Update launch template for %s to use %s", ng.ID, nextSize),
						"Perform rolling update of node group",
					},
					Details: map[string]string{
						"nodeGroupID":   ng.ID,
						"currentType":   ng.InstanceType,
						"suggestedType": nextSize,
						"avgCPUUtil":    fmt.Sprintf("%.1f", avgCPUUtil),
						"avgMemUtil":    fmt.Sprintf("%.1f", avgMemUtil),
					},
					CreatedAt: time.Now(),
				})
			}
		}

		// If consistently low utilization (<30%), suggest sizing down
		if avgCPUUtil < 30 && avgMemUtil < 30 {
			prevSize := findNextSmallerSize(ng.InstanceType, familySizes)
			if prevSize != "" {
				currentCost := nodes[0].HourlyCostUSD * cost.HoursPerMonth * float64(len(nodes))
				// Estimate cost of smaller size (approximate)
				savingsEst := currentCost * 0.3

				recs = append(recs, optimizer.Recommendation{
					ID:             fmt.Sprintf("sizedown-%s-%d", ng.ID, time.Now().Unix()),
					Type:           optimizer.RecommendationNodeGroupAdjust,
					Priority:       optimizer.PriorityMedium,
					AutoExecutable: false,
					TargetKind:     "NodeGroup",
					TargetName:     ng.Name,
					Summary:        fmt.Sprintf("Consider downsizing %s from %s to %s (avg CPU: %.0f%%, avg mem: %.0f%%)", ng.Name, ng.InstanceType, prevSize, avgCPUUtil, avgMemUtil),
					ActionSteps: []string{
						fmt.Sprintf("Update launch template for %s to use %s", ng.ID, prevSize),
						"Perform rolling update of node group",
					},
					EstimatedSaving: optimizer.SavingEstimate{
						MonthlySavingsUSD: savingsEst,
						AnnualSavingsUSD:  savingsEst * 12,
						Currency:          "USD",
					},
					Details: map[string]string{
						"nodeGroupID":   ng.ID,
						"currentType":   ng.InstanceType,
						"suggestedType": prevSize,
						"avgCPUUtil":    fmt.Sprintf("%.1f", avgCPUUtil),
						"avgMemUtil":    fmt.Sprintf("%.1f", avgMemUtil),
					},
					CreatedAt: time.Now(),
				})
			}
		}
	}

	return recs, nil
}

func findNextLargerSize(current string, sizes []*cloudprovider.InstanceType) string {
	// Find current in list and return next larger
	currentCPU := 0
	for _, s := range sizes {
		if s.Name == current {
			currentCPU = s.CPUCores
			break
		}
	}
	if currentCPU == 0 {
		return ""
	}

	bestName := ""
	bestCPU := int(^uint(0) >> 1) // MaxInt
	for _, s := range sizes {
		if s.CPUCores > currentCPU && s.CPUCores < bestCPU {
			bestCPU = s.CPUCores
			bestName = s.Name
		}
	}
	return bestName
}

func findNextSmallerSize(current string, sizes []*cloudprovider.InstanceType) string {
	currentCPU := 0
	for _, s := range sizes {
		if s.Name == current {
			currentCPU = s.CPUCores
			break
		}
	}
	if currentCPU == 0 {
		return ""
	}

	bestName := ""
	bestCPU := 0
	for _, s := range sizes {
		if s.CPUCores < currentCPU && s.CPUCores > bestCPU {
			bestCPU = s.CPUCores
			bestName = s.Name
		}
	}
	return bestName
}

package nodeautoscaler

import (
	"context"
	"fmt"
	"time"

	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/pkg/aigate"
	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
	"github.com/koptimizer/koptimizer/pkg/cost"
	"github.com/koptimizer/koptimizer/pkg/familylock"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// Downscaler handles scaling node groups down when nodes are underutilized.
type Downscaler struct {
	provider cloudprovider.CloudProvider
	guard    *familylock.FamilyLockGuard
	gate     *aigate.AIGate
	config   *config.Config
}

func NewDownscaler(provider cloudprovider.CloudProvider, guard *familylock.FamilyLockGuard, gate *aigate.AIGate, cfg *config.Config) *Downscaler {
	return &Downscaler{
		provider: provider,
		guard:    guard,
		gate:     gate,
		config:   cfg,
	}
}

func (d *Downscaler) Analyze(ctx context.Context, snapshot *optimizer.ClusterSnapshot) ([]optimizer.Recommendation, error) {
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

		// Count underutilized nodes
		underutilizedCount := 0
		for _, n := range nodes {
			cpuUtil := float64(0)
			if n.CPUCapacity > 0 {
				cpuUtil = float64(n.CPUUsed) / float64(n.CPUCapacity) * 100
			}
			memUtil := float64(0)
			if n.MemoryCapacity > 0 {
				memUtil = float64(n.MemoryUsed) / float64(n.MemoryCapacity) * 100
			}
			if cpuUtil < d.config.NodeAutoscaler.ScaleDownThreshold &&
				memUtil < d.config.NodeAutoscaler.ScaleDownThreshold {
				underutilizedCount++
			}
		}

		if underutilizedCount == 0 {
			continue
		}

		// Don't scale below min
		nodesToRemove := underutilizedCount
		if nodesToRemove > d.config.NodeAutoscaler.MaxScaleDownNodes {
			nodesToRemove = d.config.NodeAutoscaler.MaxScaleDownNodes
		}
		newDesired := ng.DesiredCount - nodesToRemove
		if newDesired < ng.MinCount {
			newDesired = ng.MinCount
		}
		if newDesired >= ng.DesiredCount {
			continue
		}

		scalePct := float64(ng.DesiredCount-newDesired) / float64(ng.DesiredCount) * 100
		requiresAIGate := scalePct > d.config.AIGate.ScaleThresholdPct

		// Estimate savings
		hourlySavingsPerNode := float64(0)
		if len(nodes) > 0 {
			hourlySavingsPerNode = nodes[0].HourlyCostUSD
		}
		monthlySavings := hourlySavingsPerNode * cost.HoursPerMonth * float64(ng.DesiredCount-newDesired)

		recs = append(recs, optimizer.Recommendation{
			ID:             fmt.Sprintf("scaledown-%s-%d", ng.ID, time.Now().Unix()),
			Type:           optimizer.RecommendationNodeScale,
			Priority:       optimizer.PriorityMedium,
			AutoExecutable: true,
			RequiresAIGate: requiresAIGate,
			TargetKind:     "NodeGroup",
			TargetName:     ng.Name,
			Summary:        fmt.Sprintf("Scale down node group %s from %d to %d nodes (%d underutilized)", ng.Name, ng.DesiredCount, newDesired, underutilizedCount),
			ActionSteps: []string{
				fmt.Sprintf("Cordon underutilized nodes in %s", ng.Name),
				"Drain pods respecting PDBs",
				fmt.Sprintf("Decrease desired count from %d to %d", ng.DesiredCount, newDesired),
			},
			EstimatedSaving: optimizer.SavingEstimate{
				MonthlySavingsUSD: monthlySavings,
				AnnualSavingsUSD:  monthlySavings * 12,
				Currency:          "USD",
			},
			EstimatedImpact: optimizer.ImpactEstimate{
				MonthlyCostChangeUSD: monthlySavings,
				NodesAffected:        ng.DesiredCount - newDesired,
				RiskLevel:            "medium",
			},
			Details: map[string]string{
				"nodeGroupID":  ng.ID,
				"desiredCount": fmt.Sprintf("%d", newDesired),
				"direction":    "down",
			},
			CreatedAt: time.Now(),
		})
	}

	return recs, nil
}

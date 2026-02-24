package nodeautoscaler

import (
	"context"
	"fmt"
	"time"

	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/internal/scheduler"
	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
	"github.com/koptimizer/koptimizer/pkg/familylock"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// Upscaler handles scaling node groups up when pods are unschedulable.
type Upscaler struct {
	provider  cloudprovider.CloudProvider
	guard     *familylock.FamilyLockGuard
	config    *config.Config
	simulator *scheduler.Simulator
}

func NewUpscaler(provider cloudprovider.CloudProvider, guard *familylock.FamilyLockGuard, cfg *config.Config) *Upscaler {
	return &Upscaler{
		provider:  provider,
		guard:     guard,
		config:    cfg,
		simulator: scheduler.NewSimulator(),
	}
}

func (u *Upscaler) Analyze(ctx context.Context, snapshot *optimizer.ClusterSnapshot) ([]optimizer.Recommendation, error) {
	var recs []optimizer.Recommendation

	// Find pending pods
	var pendingPods []optimizer.PodInfo
	for _, p := range snapshot.Pods {
		if p.Pod != nil && p.Pod.Status.Phase == "Pending" {
			pendingPods = append(pendingPods, p)
		}
	}

	if len(pendingPods) == 0 {
		return nil, nil
	}

	// For each node group, check if adding a node would help
	for _, ng := range snapshot.NodeGroups {
		if ng.DesiredCount >= ng.MaxCount {
			continue // Already at max
		}

		// Calculate how many nodes to add (capped by MaxScaleUpNodes)
		maxUp := u.config.NodeAutoscaler.MaxScaleUpNodes
		if maxUp <= 0 {
			maxUp = 1 // safety: at least cap to 1 if misconfigured
		}
		nodesToAdd := min(len(pendingPods), maxUp)
		newDesired := ng.DesiredCount + nodesToAdd
		if newDesired > ng.MaxCount {
			newDesired = ng.MaxCount
		}

		if newDesired <= ng.DesiredCount {
			continue
		}

		var scalePct float64
		if ng.DesiredCount > 0 {
			scalePct = float64(newDesired-ng.DesiredCount) / float64(ng.DesiredCount) * 100
		} else {
			scalePct = 100 // Scaling from 0 is always a 100%+ change
		}
		requiresAIGate := scalePct > u.config.AIGate.ScaleThresholdPct

		recs = append(recs, optimizer.Recommendation{
			ID:             fmt.Sprintf("scaleup-%s-%d", ng.ID, time.Now().Unix()),
			Type:           optimizer.RecommendationNodeScale,
			Priority:       optimizer.PriorityHigh,
			AutoExecutable: true,
			RequiresAIGate: requiresAIGate,
			TargetKind:     "NodeGroup",
			TargetName:     ng.Name,
			Summary:        fmt.Sprintf("Scale up node group %s from %d to %d nodes (%d pending pods)", ng.Name, ng.DesiredCount, newDesired, len(pendingPods)),
			ActionSteps: []string{
				fmt.Sprintf("Increase desired count of ASG %s from %d to %d", ng.ID, ng.DesiredCount, newDesired),
			},
			EstimatedImpact: optimizer.ImpactEstimate{
				NodesAffected: newDesired - ng.DesiredCount,
				PodsAffected:  len(pendingPods),
				RiskLevel:     "low",
			},
			Details: map[string]string{
				"nodeGroupID":  ng.ID,
				"desiredCount": fmt.Sprintf("%d", newDesired),
				"direction":    "up",
			},
			CreatedAt: time.Now(),
		})
	}

	return recs, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

package nodegroupmgr

import (
	"context"
	"fmt"
	"time"

	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/internal/state"
	"github.com/koptimizer/koptimizer/pkg/aigate"
	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
	"github.com/koptimizer/koptimizer/pkg/familylock"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// MinAdjuster adjusts node group minimum counts based on utilization.
type MinAdjuster struct {
	provider cloudprovider.CloudProvider
	guard    *familylock.FamilyLockGuard
	gate     *aigate.AIGate
	config   *config.Config
	// lowSince tracks when each node group first went below the utilization
	// threshold. A recommendation is only generated after the node group has
	// been below threshold for at least ObservationPeriod.
	lowSince map[string]time.Time
}

func NewMinAdjuster(provider cloudprovider.CloudProvider, guard *familylock.FamilyLockGuard, gate *aigate.AIGate, cfg *config.Config) *MinAdjuster {
	return &MinAdjuster{
		provider: provider,
		guard:    guard,
		gate:     gate,
		config:   cfg,
		lowSince: make(map[string]time.Time),
	}
}

func (m *MinAdjuster) Analyze(ctx context.Context, nodeGroupState *state.NodeGroupState) ([]optimizer.Recommendation, error) {
	var recs []optimizer.Recommendation
	threshold := m.config.NodeGroupMgr.MinAdjustment.MinUtilizationPct
	observationPeriod := m.config.NodeGroupMgr.MinAdjustment.ObservationPeriod

	for _, ng := range nodeGroupState.GetAll() {
		if ng.MinCount == 0 {
			continue // Already at 0
		}

		cpuUtil := ng.CPUUtilization()
		memUtil := ng.MemoryUtilization()

		if cpuUtil < threshold && memUtil < threshold {
			// Track when the node group first dropped below threshold
			if _, tracked := m.lowSince[ng.ID]; !tracked {
				m.lowSince[ng.ID] = time.Now()
			}

			// Only generate a recommendation after the node group has been
			// continuously below threshold for the configured ObservationPeriod
			belowSince := m.lowSince[ng.ID]
			if time.Since(belowSince) < observationPeriod {
				continue
			}

			// Suggest reducing min (integer division floors toward zero)
			newMin := ng.MinCount / 2
			if newMin > ng.MaxCount {
				newMin = ng.MaxCount
			}

			// Setting min to 0 always requires AI Gate
			requiresAIGate := newMin == 0

			recs = append(recs, optimizer.Recommendation{
				ID:             fmt.Sprintf("minadj-%s-%d", ng.ID, time.Now().Unix()),
				Type:           optimizer.RecommendationNodeGroupAdjust,
				Priority:       optimizer.PriorityMedium,
				AutoExecutable: true,
				RequiresAIGate: requiresAIGate,
				TargetKind:     "NodeGroup",
				TargetName:     ng.Name,
				Summary:        fmt.Sprintf("Reduce min count of %s from %d to %d (CPU: %.0f%%, Mem: %.0f%%, below threshold for %s)", ng.Name, ng.MinCount, newMin, cpuUtil, memUtil, time.Since(belowSince).Round(time.Minute)),
				ActionSteps: []string{
					fmt.Sprintf("Set minimum count of %s from %d to %d", ng.ID, ng.MinCount, newMin),
				},
				EstimatedImpact: optimizer.ImpactEstimate{
					NodesAffected: ng.MinCount - newMin,
					RiskLevel:     "medium",
				},
				Details: map[string]string{
					"nodeGroupID": ng.ID,
					"action":      "set-min",
					"newMin":      fmt.Sprintf("%d", newMin),
					"lowSince":    belowSince.Format(time.RFC3339),
				},
				CreatedAt: time.Now(),
			})
		} else {
			// Utilization is back above threshold, clear tracking
			delete(m.lowSince, ng.ID)
		}
	}

	return recs, nil
}

func (m *MinAdjuster) Execute(ctx context.Context, rec optimizer.Recommendation) error {
	nodeGroupID := rec.Details["nodeGroupID"]
	var newMin int
	fmt.Sscanf(rec.Details["newMin"], "%d", &newMin)

	if err := m.guard.ValidateNodeGroupAction(familylock.NodeGroupModifyMin); err != nil {
		return err
	}

	return m.provider.SetNodeGroupMinCount(ctx, nodeGroupID, newMin)
}

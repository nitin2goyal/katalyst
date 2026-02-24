package nodegroupmgr

import (
	"context"
	"fmt"
	"time"

	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/internal/state"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// EmptyChecker detects node groups with no running pods.
type EmptyChecker struct {
	config *config.Config
}

func NewEmptyChecker(cfg *config.Config) *EmptyChecker {
	return &EmptyChecker{config: cfg}
}

func (e *EmptyChecker) Analyze(ctx context.Context, nodeGroupState *state.NodeGroupState) ([]optimizer.Recommendation, error) {
	var recs []optimizer.Recommendation
	emptyPeriod := e.config.NodeGroupMgr.EmptyGroupDetection.EmptyPeriod

	for _, ng := range nodeGroupState.GetAll() {
		if !ng.IsEmpty() {
			continue
		}

		if ng.EmptySince == nil {
			continue
		}

		emptySince := time.Unix(*ng.EmptySince, 0)
		emptyDuration := time.Since(emptySince)

		if emptyDuration < emptyPeriod {
			continue
		}

		monthlyCost := ng.MonthlyCostUSD

		recs = append(recs, optimizer.Recommendation{
			ID:             fmt.Sprintf("emptygroup-%s-%d", ng.ID, time.Now().Unix()),
			Type:           optimizer.RecommendationNodeGroupDelete,
			Priority:       optimizer.PriorityLow,
			AutoExecutable: false, // Never auto-delete node groups
			TargetKind:     "NodeGroup",
			TargetName:     ng.Name,
			Summary:        fmt.Sprintf("Node group %s has been empty for %s, consider deleting", ng.Name, emptyDuration.Round(time.Hour)),
			ActionSteps: []string{
				fmt.Sprintf("Verify no workloads need node group %s", ng.Name),
				fmt.Sprintf("Delete node group %s via cloud console or CLI", ng.ID),
			},
			EstimatedSaving: optimizer.SavingEstimate{
				MonthlySavingsUSD: monthlyCost,
				AnnualSavingsUSD:  monthlyCost * 12,
				Currency:          "USD",
			},
			Details: map[string]string{
				"nodeGroupID":   ng.ID,
				"emptySince":    emptySince.Format(time.RFC3339),
				"emptyDuration": emptyDuration.String(),
			},
			CreatedAt: time.Now(),
		})
	}

	return recs, nil
}

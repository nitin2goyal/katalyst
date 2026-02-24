package spot

import (
	"context"
	"fmt"

	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// DiversityManager ensures spot node groups use multiple instance types to
// reduce correlated interruption risk. When all spot nodes use the same
// instance type, a single capacity event can terminate the entire fleet.
type DiversityManager struct {
	provider cloudprovider.CloudProvider
	config   *config.Config
}

func NewDiversityManager(provider cloudprovider.CloudProvider, cfg *config.Config) *DiversityManager {
	return &DiversityManager{provider: provider, config: cfg}
}

func (d *DiversityManager) Analyze(ctx context.Context, snapshot *optimizer.ClusterSnapshot) ([]optimizer.Recommendation, error) {
	var recs []optimizer.Recommendation
	minTypes := d.config.Spot.DiversityMinTypes

	for _, ng := range snapshot.NodeGroups {
		if ng.Lifecycle != "spot" && ng.Lifecycle != "mixed" {
			continue
		}
		if ng.CurrentCount < 2 {
			continue
		}

		typeCount := len(ng.InstanceTypes)
		if typeCount == 0 {
			typeCount = 1 // single instance type if not specified
		}

		if typeCount < minTypes {
			recs = append(recs, optimizer.Recommendation{
				ID:             fmt.Sprintf("spot-diversity-%s", ng.ID),
				Type:           optimizer.RecommendationSpotOptimize,
				Priority:       optimizer.PriorityMedium,
				AutoExecutable: false,
				TargetKind:     "NodeGroup",
				TargetName:     ng.Name,
				Summary:        fmt.Sprintf("Spot node group %s uses only %d instance type(s) — recommend %d+ for interruption resilience", ng.Name, typeCount, minTypes),
				ActionSteps: []string{
					fmt.Sprintf("Add at least %d compatible instance types to node group %s", minTypes-typeCount, ng.Name),
					"Select types with similar CPU/memory ratios from different instance families",
					"Use capacity-optimized-prioritized allocation strategy",
				},
				Details: map[string]string{
					"action":       "diversify-spot-types",
					"nodeGroupID":  ng.ID,
					"currentTypes": fmt.Sprintf("%d", typeCount),
					"targetTypes":  fmt.Sprintf("%d", minTypes),
				},
			})
		}
	}

	return recs, nil
}

func (d *DiversityManager) Execute(ctx context.Context, rec optimizer.Recommendation) error {
	// Diversity changes require modifying ASG/MIG launch templates.
	// Generated as recommendations for manual implementation.
	return nil
}

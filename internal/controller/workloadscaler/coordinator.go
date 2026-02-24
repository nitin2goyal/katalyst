package workloadscaler

import (
	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// Coordinator resolves conflicts between horizontal and vertical scaling.
type Coordinator struct {
	config *config.Config
}

func NewCoordinator(cfg *config.Config) *Coordinator {
	return &Coordinator{config: cfg}
}

// Resolve filters and adjusts recommendations to prevent HPA/VPA conflicts.
func (c *Coordinator) Resolve(recs []optimizer.Recommendation) []optimizer.Recommendation {
	// Group by target workload
	byTarget := make(map[string][]optimizer.Recommendation)
	for _, r := range recs {
		key := r.TargetNamespace + "/" + r.TargetKind + "/" + r.TargetName
		byTarget[key] = append(byTarget[key], r)
	}

	var resolved []optimizer.Recommendation
	for _, targetRecs := range byTarget {
		hasHorizontal := false
		hasVertical := false
		for _, r := range targetRecs {
			if r.Details["scalingType"] == "horizontal" {
				hasHorizontal = true
			}
			if r.Details["scalingType"] == "vertical" {
				hasVertical = true
			}
		}

		if hasHorizontal && hasVertical {
			// Staging approach: when both vertical and horizontal recs exist for the
			// same workload, apply vertical first. Horizontal recs are deferred (marked
			// as non-auto-executable) so they appear as recommendations but are not
			// auto-executed. On the next reconciliation cycle, once the vertical change
			// has taken effect, the horizontal rec can be re-evaluated and acted upon.
			for _, r := range targetRecs {
				if r.Details["scalingType"] == "vertical" {
					resolved = append(resolved, r)
				} else if r.Details["scalingType"] == "horizontal" {
					r.Details["deferred"] = "true"
					r.AutoExecutable = false
					resolved = append(resolved, r)
				}
			}
		} else {
			resolved = append(resolved, targetRecs...)
		}
	}

	return resolved
}

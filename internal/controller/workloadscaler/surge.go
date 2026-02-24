package workloadscaler

import (
	"context"
	"fmt"
	"time"

	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// SurgeDetector detects anomalous usage spikes and triggers immediate scaling.
type SurgeDetector struct {
	config    *config.Config
	baselines map[string]float64 // workloadKey -> baseline CPU usage
}

func NewSurgeDetector(cfg *config.Config) *SurgeDetector {
	return &SurgeDetector{
		config:    cfg,
		baselines: make(map[string]float64),
	}
}

// Detect checks for usage surges across workloads.
func (s *SurgeDetector) Detect(ctx context.Context, snapshot *optimizer.ClusterSnapshot) ([]optimizer.Recommendation, error) {
	var recs []optimizer.Recommendation

	// Group pod usage by owner
	type workloadUsage struct {
		namespace string
		kind      string
		name      string
		totalCPU  int64
		pods      int
	}

	workloads := make(map[string]*workloadUsage)
	for _, pod := range snapshot.Pods {
		if pod.OwnerName == "" {
			continue
		}
		key := pod.Pod.Namespace + "/" + pod.OwnerKind + "/" + pod.OwnerName
		if _, ok := workloads[key]; !ok {
			workloads[key] = &workloadUsage{
				namespace: pod.Pod.Namespace,
				kind:      pod.OwnerKind,
				name:      pod.OwnerName,
			}
		}
		workloads[key].totalCPU += pod.CPUUsage
		workloads[key].pods++
	}

	threshold := s.config.WorkloadScaler.SurgeThreshold

	for key, wl := range workloads {
		currentUsage := float64(wl.totalCPU)

		isSurge := false
		if baseline, ok := s.baselines[key]; ok && baseline > 0 {
			ratio := currentUsage / baseline
			if ratio >= threshold {
				isSurge = true
				recs = append(recs, optimizer.Recommendation{
					ID:              fmt.Sprintf("surge-%s-%d", key, time.Now().Unix()),
					Type:            optimizer.RecommendationWorkloadScale,
					Priority:        optimizer.PriorityCritical,
					AutoExecutable:  true,
					TargetKind:      wl.kind,
					TargetName:      wl.name,
					TargetNamespace: wl.namespace,
					Summary:         fmt.Sprintf("Usage surge detected for %s/%s: %.1fx normal (current: %dm, baseline: %dm)", wl.namespace, wl.name, ratio, wl.totalCPU, int64(baseline)),
					ActionSteps: []string{
						fmt.Sprintf("Immediately scale up %s/%s replicas", wl.namespace, wl.name),
					},
					Details: map[string]string{
						"scalingType": "horizontal",
						"reason":      "surge",
						"ratio":       fmt.Sprintf("%.1f", ratio),
						"baseline":    fmt.Sprintf("%d", int64(baseline)),
						"current":     fmt.Sprintf("%d", wl.totalCPU),
					},
					CreatedAt: time.Now(),
				})
			}
		}

		// Update baseline using exponential moving average, but only during
		// normal (non-surge) conditions. Updating during a surge would
		// pollute the baseline and make future detection less sensitive.
		if !isSurge {
			if existing, ok := s.baselines[key]; ok {
				s.baselines[key] = existing*0.9 + currentUsage*0.1
			} else {
				s.baselines[key] = currentUsage
			}
		}
	}

	return recs, nil
}

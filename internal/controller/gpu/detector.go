package gpu

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/koptimizer/koptimizer/internal/config"
	intmetrics "github.com/koptimizer/koptimizer/internal/metrics"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// Detector identifies idle and underutilized GPU nodes.
type Detector struct {
	config    *config.Config
	idleSince map[string]time.Time // nodeName -> when it became idle
}

func NewDetector(cfg *config.Config) *Detector {
	return &Detector{
		config:    cfg,
		idleSince: make(map[string]time.Time),
	}
}

// DetectIdle finds GPU nodes that have been idle beyond the configured threshold.
func (d *Detector) DetectIdle(ctx context.Context, snapshot *optimizer.ClusterSnapshot) ([]optimizer.Recommendation, error) {
	var recs []optimizer.Recommendation
	idleCount := 0

	for _, node := range snapshot.Nodes {
		if !node.IsGPUNode {
			continue
		}

		// Check if GPU is idle
		gpuIdle := node.GPUsUsed == 0
		gpuUnderutilized := false
		if node.GPUs > 0 {
			gpuUtilPct := float64(node.GPUsUsed) / float64(node.GPUs) * 100
			gpuUnderutilized = gpuUtilPct < d.config.GPU.IdleThresholdPct
		}

		if gpuIdle || gpuUnderutilized {
			// Track how long it's been idle
			if _, ok := d.idleSince[node.Node.Name]; !ok {
				d.idleSince[node.Node.Name] = time.Now()
			}

			idleDuration := time.Since(d.idleSince[node.Node.Name])
			if idleDuration >= d.config.GPU.IdleDuration {
				idleCount++
				monthlyCost := node.HourlyCostUSD * 730

				// Compute available CPU headroom for scavenger pods.
				// Reserve CPUHeadroomReservePct for GPU pod data-loading CPU bursts.
				headroom := ComputeCPUHeadroom(&node)
				allocCPU := node.Node.Status.Allocatable[corev1.ResourceCPU]

				recs = append(recs, optimizer.Recommendation{
					ID:             fmt.Sprintf("gpu-idle-%s-%d", node.Node.Name, time.Now().Unix()),
					Type:           optimizer.RecommendationGPUOptimize,
					Priority:       optimizer.PriorityHigh,
					AutoExecutable: true,
					TargetKind:     "Node",
					TargetName:     node.Node.Name,
					Summary:        fmt.Sprintf("GPU node %s idle for %s (GPUs: %d, used: %d), enable CPU fallback (CPU headroom: %s of %s)", node.Node.Name, idleDuration.Round(time.Minute), node.GPUs, node.GPUsUsed, headroom.String(), allocCPU.String()),
					ActionSteps: []string{
						fmt.Sprintf("Remove GPU NoSchedule taint from %s to allow CPU workloads", node.Node.Name),
						"Add gpu-fallback annotation to mark node as serving CPU workloads",
						"Set low priority for CPU pods on this node so GPU pods take precedence",
						fmt.Sprintf("CPU pods should set conservative requests (headroom: %s, %d%% reserved for GPU data-loading)", headroom.String(), CPUHeadroomReservePct),
						"Use ResourceQuotas per namespace to cap scavenger pod resource claims",
						"NEVER let CPU pods request nvidia.com/gpu — even requests: nvidia.com/gpu: 0 causes scheduler issues",
					},
					EstimatedSaving: optimizer.SavingEstimate{
						MonthlySavingsUSD: monthlyCost * 0.5,
						Currency:          "USD",
					},
					EstimatedImpact: optimizer.ImpactEstimate{
						NodesAffected: 1,
						RiskLevel:     "low",
					},
					Details: map[string]string{
						"nodeName":           node.Node.Name,
						"gpuCount":           fmt.Sprintf("%d", node.GPUs),
						"gpuUsed":            fmt.Sprintf("%d", node.GPUsUsed),
						"idleDuration":       idleDuration.String(),
						"action":             "enable-cpu-fallback",
						"monthlyCostUSD":     fmt.Sprintf("%.2f", monthlyCost),
						"cpuHeadroom":        headroom.String(),
						"cpuReservePct":      fmt.Sprintf("%d", CPUHeadroomReservePct),
						"nodeAllocatableCPU": allocCPU.String(),
					},
					CreatedAt: time.Now(),
				})
			}
		} else {
			// GPU is active, reset idle timer
			delete(d.idleSince, node.Node.Name)
		}
	}

	// Update Prometheus metrics
	intmetrics.GPUNodesIdle.Set(float64(idleCount))

	return recs, nil
}

package rebalancer

import (
	"context"
	"fmt"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// BusyRedistributor handles redistribution of workloads from overloaded nodes.
type BusyRedistributor struct {
	client client.Client
	config *config.Config
}

func NewBusyRedistributor(c client.Client, cfg *config.Config) *BusyRedistributor {
	return &BusyRedistributor{client: c, config: cfg}
}

// Analyze detects overloaded nodes and suggests redistribution.
func (b *BusyRedistributor) Analyze(ctx context.Context, snapshot *optimizer.ClusterSnapshot) ([]optimizer.Recommendation, error) {
	var recs []optimizer.Recommendation
	threshold := b.config.Rebalancer.BusyRedistribution.OverloadedThresholdPct
	target := b.config.Rebalancer.BusyRedistribution.TargetUtilizationPct

	for _, node := range snapshot.Nodes {
		cpuUtil := float64(0)
		if node.CPUCapacity > 0 {
			cpuUtil = float64(node.CPUUsed) / float64(node.CPUCapacity) * 100
		}
		memUtil := float64(0)
		if node.MemoryCapacity > 0 {
			memUtil = float64(node.MemoryUsed) / float64(node.MemoryCapacity) * 100
		}

		if cpuUtil > threshold || memUtil > threshold {
			// Find pods that can be moved to bring utilization to target
			podsToMove := 0
			cpuToFree := int64(0)
			if cpuUtil > target {
				cpuToFree = node.CPUUsed - int64(float64(node.CPUCapacity)*target/100)
			}

			// Count movable pods
			for _, pod := range node.Pods {
				isDaemonSet := false
				for _, ref := range pod.OwnerReferences {
					if ref.Kind == "DaemonSet" {
						isDaemonSet = true
						break
					}
				}
				if !isDaemonSet {
					podsToMove++
				}
			}

			recs = append(recs, optimizer.Recommendation{
				ID:              fmt.Sprintf("busyredist-%s-%d", node.Node.Name, time.Now().Unix()),
				Type:            optimizer.RecommendationRebalance,
				Priority:        optimizer.PriorityHigh,
				AutoExecutable:  true,
				TargetKind:      "Node",
				TargetName:      node.Node.Name,
				Summary:         fmt.Sprintf("Node %s overloaded (CPU: %.0f%%, Mem: %.0f%%), redistribute workloads", node.Node.Name, cpuUtil, memUtil),
				ActionSteps: []string{
					fmt.Sprintf("Identify movable pods on %s", node.Node.Name),
					"Evict pods to trigger rescheduling on less loaded nodes",
				},
				EstimatedImpact: optimizer.ImpactEstimate{
					NodesAffected: 1,
					PodsAffected:  podsToMove,
					RiskLevel:     "medium",
				},
				Details: map[string]string{
					"nodeName":  node.Node.Name,
					"cpuUtil":   fmt.Sprintf("%.1f", cpuUtil),
					"memUtil":   fmt.Sprintf("%.1f", memUtil),
					"cpuToFree": fmt.Sprintf("%d", cpuToFree),
				},
				CreatedAt: time.Now(),
			})
		}
	}

	return recs, nil
}

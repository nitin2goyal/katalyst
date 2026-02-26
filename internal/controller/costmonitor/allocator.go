package costmonitor

import (
	"context"
	"sort"

	corev1 "k8s.io/api/core/v1"

	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
	"github.com/koptimizer/koptimizer/pkg/cost"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// Allocator calculates cost attribution per namespace, workload, and label.
type Allocator struct {
	provider cloudprovider.CloudProvider
}

func NewAllocator(provider cloudprovider.CloudProvider) *Allocator {
	return &Allocator{provider: provider}
}

// AllocateByNamespace calculates cost per namespace.
func (a *Allocator) AllocateByNamespace(ctx context.Context, snapshot *optimizer.ClusterSnapshot) (map[string]float64, error) {
	costs := make(map[string]float64)

	for _, node := range snapshot.Nodes {
		nodeCost := node.HourlyCostUSD * cost.HoursPerMonth
		if nodeCost == 0 {
			continue
		}
		if node.CPUCapacity == 0 && node.MemoryCapacity == 0 {
			continue
		}

		// Two-pass: compute weights then distribute full node cost proportionally.
		weights := make([]float64, len(node.Pods))
		totalW := 0.0
		for i, pod := range node.Pods {
			if pod.Status.Phase != "Running" {
				continue
			}
			w := allocPodWeight(pod.Spec.Containers, node.CPUCapacity, node.MemoryCapacity)
			weights[i] = w
			totalW += w
		}
		if totalW == 0 {
			continue
		}
		for i, pod := range node.Pods {
			if weights[i] == 0 {
				continue
			}
			costs[pod.Namespace] += nodeCost * weights[i] / totalW
		}
	}

	return costs, nil
}

// AllocateByNodeGroup calculates cost per node group.
func (a *Allocator) AllocateByNodeGroup(ctx context.Context, snapshot *optimizer.ClusterSnapshot) (map[string]float64, error) {
	costs := make(map[string]float64)
	for _, node := range snapshot.Nodes {
		if node.NodeGroup != "" {
			costs[node.NodeGroup] += node.HourlyCostUSD * cost.HoursPerMonth
		}
	}
	return costs, nil
}

// TopWorkloads returns the top N most expensive workloads.
func (a *Allocator) TopWorkloads(ctx context.Context, snapshot *optimizer.ClusterSnapshot, limit int) ([]cost.WorkloadCost, error) {
	workloadCosts := make(map[string]*cost.WorkloadCost)

	for _, node := range snapshot.Nodes {
		nodeCost := node.HourlyCostUSD * cost.HoursPerMonth
		if nodeCost == 0 {
			continue
		}
		if node.CPUCapacity == 0 && node.MemoryCapacity == 0 {
			continue
		}

		weights := make([]float64, len(node.Pods))
		totalW := 0.0
		for i, pod := range node.Pods {
			w := allocPodWeight(pod.Spec.Containers, node.CPUCapacity, node.MemoryCapacity)
			weights[i] = w
			totalW += w
		}
		if totalW == 0 {
			continue
		}
		for i, pod := range node.Pods {
			ownerKind, ownerName := "", ""
			if len(pod.OwnerReferences) > 0 {
				ownerKind = pod.OwnerReferences[0].Kind
				ownerName = pod.OwnerReferences[0].Name
			}
			if ownerKind == "" {
				ownerKind = "Pod"
				ownerName = pod.Name
			}

			key := pod.Namespace + "/" + ownerKind + "/" + ownerName
			if _, ok := workloadCosts[key]; !ok {
				workloadCosts[key] = &cost.WorkloadCost{
					Namespace: pod.Namespace,
					Kind:      ownerKind,
					Name:      ownerName,
				}
			}
			workloadCosts[key].MonthlyCostUSD += nodeCost * weights[i] / totalW
			workloadCosts[key].Replicas++
		}
	}

	// Sort by cost descending
	var result []cost.WorkloadCost
	for _, wc := range workloadCosts {
		result = append(result, *wc)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].MonthlyCostUSD > result[j].MonthlyCostUSD
	})

	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

// allocPodWeight computes a blended CPU+memory weight for a pod relative to node capacity.
func allocPodWeight(containers []corev1.Container, cpuCap, memCap int64) float64 {
	cpuReq := int64(0)
	memReq := int64(0)
	for _, c := range containers {
		if cpu, ok := c.Resources.Requests["cpu"]; ok {
			cpuReq += cpu.MilliValue()
		}
		if mem, ok := c.Resources.Requests["memory"]; ok {
			memReq += mem.Value()
		}
	}
	w := 0.0
	if cpuCap > 0 {
		w += 0.5 * float64(cpuReq) / float64(cpuCap)
	}
	if memCap > 0 {
		w += 0.5 * float64(memReq) / float64(memCap)
	}
	return w
}

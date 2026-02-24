package costmonitor

import (
	"context"
	"sort"

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
		nodeHourlyCost := node.HourlyCostUSD
		if nodeHourlyCost == 0 {
			continue
		}

		// Distribute node cost proportionally based on CPU + memory requests
		totalCPURequested := node.CPURequested
		totalMemRequested := node.MemoryRequested
		if totalCPURequested == 0 && totalMemRequested == 0 {
			continue
		}

		for _, pod := range node.Pods {
			if pod.Status.Phase != "Running" {
				continue
			}
			ns := pod.Namespace
			podCPUReq := int64(0)
			podMemReq := int64(0)
			for _, c := range pod.Spec.Containers {
				if cpu, ok := c.Resources.Requests["cpu"]; ok {
					podCPUReq += cpu.MilliValue()
				}
				if mem, ok := c.Resources.Requests["memory"]; ok {
					podMemReq += mem.Value()
				}
			}
			// Weighted average: 50% CPU, 50% memory
			fraction := 0.0
			if totalCPURequested > 0 {
				fraction += 0.5 * float64(podCPUReq) / float64(totalCPURequested)
			}
			if totalMemRequested > 0 {
				fraction += 0.5 * float64(podMemReq) / float64(totalMemRequested)
			}
			costs[ns] += nodeHourlyCost * cost.HoursPerMonth * fraction
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
		nodeHourlyCost := node.HourlyCostUSD
		if nodeHourlyCost == 0 {
			continue
		}
		totalCPURequested := node.CPURequested
		totalMemRequested := node.MemoryRequested
		if totalCPURequested == 0 && totalMemRequested == 0 {
			continue
		}

		for _, pod := range node.Pods {
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

			podCPUReq := int64(0)
			podMemReq := int64(0)
			for _, c := range pod.Spec.Containers {
				if cpu, ok := c.Resources.Requests["cpu"]; ok {
					podCPUReq += cpu.MilliValue()
				}
				if mem, ok := c.Resources.Requests["memory"]; ok {
					podMemReq += mem.Value()
				}
			}

			// Weighted average: 50% CPU, 50% memory
			fraction := 0.0
			if totalCPURequested > 0 {
				fraction += 0.5 * float64(podCPUReq) / float64(totalCPURequested)
			}
			if totalMemRequested > 0 {
				fraction += 0.5 * float64(podMemReq) / float64(totalMemRequested)
			}
			workloadCosts[key].MonthlyCostUSD += nodeHourlyCost * cost.HoursPerMonth * fraction
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

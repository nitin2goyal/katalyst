package state

import (
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// Snapshot creates a point-in-time ClusterSnapshot for optimizers.
func (c *ClusterState) Snapshot() *optimizer.ClusterSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()

	nodes := make([]optimizer.NodeInfo, 0, len(c.nodes))
	for _, n := range c.nodes {
		pods := make([]*corev1.Pod, len(n.Pods))
		copy(pods, n.Pods)
		nodes = append(nodes, optimizer.NodeInfo{
			Node:            n.Node.DeepCopy(),
			Pods:            pods,
			InstanceType:    n.InstanceType,
			InstanceFamily:  n.InstanceFamily,
			CPUCapacity:     n.CPUCapacity,
			MemoryCapacity:  n.MemoryCapacity,
			CPURequested:    n.CPURequested,
			MemoryRequested: n.MemoryRequested,
			CPUUsed:         n.CPUUsed,
			MemoryUsed:      n.MemoryUsed,
			GPUs:            n.GPUCapacity,
			GPUsUsed:        n.GPUsUsed,
			HourlyCostUSD:   n.HourlyCostUSD,
			IsGPUNode:       n.IsGPUNode,
			NodeGroup:       n.NodeGroupID,
		})
	}

	allGroups := c.nodeGroups.GetAll()
	groups := make([]*cloudprovider.NodeGroup, 0, len(allGroups))
	for _, g := range allGroups {
		groups = append(groups, g.NodeGroup)
	}

	// Build top-level pod list for controllers that iterate snapshot.Pods
	// Pre-allocate with an estimate of pods per node
	pods := make([]optimizer.PodInfo, 0, len(c.nodes)*10)
	for _, n := range c.nodes {
		for _, pod := range n.Pods {
			var cpuReq, memReq, cpuLim, memLim int64
			var gpuReq int
			for _, container := range pod.Spec.Containers {
				cpuReq += container.Resources.Requests.Cpu().MilliValue()
				memReq += container.Resources.Requests.Memory().Value()
				cpuLim += container.Resources.Limits.Cpu().MilliValue()
				memLim += container.Resources.Limits.Memory().Value()
				if gpuQty, ok := container.Resources.Requests[corev1.ResourceName("nvidia.com/gpu")]; ok {
					gpuReq += int(gpuQty.Value())
				}
			}
			ownerKind, ownerName := "", ""
			if len(pod.OwnerReferences) > 0 {
				ownerKind = pod.OwnerReferences[0].Kind
				ownerName = pod.OwnerReferences[0].Name
			}

			// Look up usage from PodState if available
			var cpuUsage, memUsage int64
			podKey := pod.Namespace + "/" + pod.Name
			if ps, ok := c.pods[podKey]; ok {
				cpuUsage = ps.CPUUsage
				memUsage = ps.MemoryUsage
			}

			pods = append(pods, optimizer.PodInfo{
				Pod:           pod.DeepCopy(),
				CPURequest:    cpuReq,
				MemoryRequest: memReq,
				CPUUsage:      cpuUsage,
				MemoryUsage:   memUsage,
				CPULimit:      cpuLim,
				MemoryLimit:   memLim,
				OwnerKind:     ownerKind,
				OwnerName:     ownerName,
				IsGPUWorkload: gpuReq > 0,
				GPURequest:    gpuReq,
			})
		}
	}

	return &optimizer.ClusterSnapshot{
		Nodes:      nodes,
		Pods:       pods,
		NodeGroups: groups,
		Timestamp:  time.Now(),
	}
}

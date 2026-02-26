package handler

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/koptimizer/koptimizer/internal/state"
	"github.com/koptimizer/koptimizer/pkg/cost"
)

type NodeHandler struct {
	state *state.ClusterState
}

func NewNodeHandler(st *state.ClusterState) *NodeHandler {
	return &NodeHandler{state: st}
}

func (h *NodeHandler) List(w http.ResponseWriter, r *http.Request) {
	nodes := h.state.GetAllNodes()
	var result []map[string]interface{}
	for _, n := range nodes {
		appCount, sysCount := 0, 0
		for _, pod := range n.Pods {
			if IsSystemPod(pod) {
				sysCount++
			} else {
				appCount++
			}
		}
		// Get disk info from the node group
		diskType, diskSizeGB := "", 0
		if ng, ok := h.state.GetNodeGroups().Get(n.NodeGroupID); ok {
			diskType = ng.DiskType
			diskSizeGB = ng.DiskSizeGB
		}
		result = append(result, map[string]interface{}{
			"name":           n.Node.Name,
			"instanceType":   n.InstanceType,
			"instanceFamily": n.InstanceFamily,
			"nodeGroup":      n.NodeGroupID,
			"cpuCapacity":    n.CPUCapacity,
			"memCapacity":    n.MemoryCapacity,
			"cpuRequested":   n.CPURequested,
			"memRequested":   n.MemoryRequested,
			"cpuUsed":        n.CPUUsed,
			"memUsed":        n.MemoryUsed,
			"cpuUtilPct":     n.CPUUtilization(),
			"memUtilPct":     n.MemoryUtilization(),
			"cpuAllocPct":    n.CPURequestUtilization(),
			"memAllocPct":    n.MemoryRequestUtilization(),
			"hourlyCostUSD":  n.HourlyCostUSD,
			"isSpot":         n.IsSpot,
			"isGPU":          n.IsGPUNode,
			"podCount":       len(n.Pods),
			"appPodCount":    appCount,
			"systemPodCount": sysCount,
			"diskType":       diskType,
			"diskSizeGB":     diskSizeGB,
		})
	}
	writePaginatedJSON(w, r, result)
}

func (h *NodeHandler) Get(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	node, ok := h.state.GetNode(name)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "node not found"})
		return
	}

	// Get disk info from the node group
	diskType, diskSizeGB := "", 0
	if ng, ok := h.state.GetNodeGroups().Get(node.NodeGroupID); ok {
		diskType = ng.DiskType
		diskSizeGB = ng.DiskSizeGB
	}

	resp := map[string]interface{}{
		"name":           node.Node.Name,
		"instanceType":   node.InstanceType,
		"instanceFamily": node.InstanceFamily,
		"nodeGroup":      node.NodeGroupID,
		"cpuCapacity":    node.CPUCapacity,
		"memCapacity":    node.MemoryCapacity,
		"cpuRequested":   node.CPURequested,
		"memRequested":   node.MemoryRequested,
		"cpuUsed":        node.CPUUsed,
		"memUsed":        node.MemoryUsed,
		"cpuUtilPct":     node.CPUUtilization(),
		"memUtilPct":     node.MemoryUtilization(),
		"hourlyCostUSD":  node.HourlyCostUSD,
		"monthlyCostUSD": node.HourlyCostUSD * cost.HoursPerMonth,
		"isSpot":         node.IsSpot,
		"isGPU":          node.IsGPUNode,
		"podCount":       len(node.Pods),
		"diskType":       diskType,
		"diskSizeGB":     diskSizeGB,
	}

	// Build pods array for the detail table
	pods := make([]map[string]interface{}, 0, len(node.Pods))
	appCount, sysCount := 0, 0
	for _, pod := range node.Pods {
		cpuMilli, memBytes := state.ExtractPodRequests(pod)
		status := computePodStatus(pod)
		isSys := IsSystemPod(pod)
		if isSys {
			sysCount++
		} else {
			appCount++
		}
		pods = append(pods, map[string]interface{}{
			"name":       pod.Name,
			"namespace":  pod.Namespace,
			"cpuRequest": fmt.Sprintf("%dm", cpuMilli),
			"memRequest": formatMemory(memBytes),
			"status":     status,
			"isSystem":   isSys,
		})
	}
	resp["pods"] = pods
	resp["appPodCount"] = appCount
	resp["systemPodCount"] = sysCount

	writeJSON(w, http.StatusOK, resp)
}

// formatMemory converts bytes to a human-readable string (Mi or Gi).
func formatMemory(bytes int64) string {
	const gi = 1024 * 1024 * 1024
	const mi = 1024 * 1024
	if bytes >= gi && bytes%gi == 0 {
		return fmt.Sprintf("%dGi", bytes/gi)
	}
	return fmt.Sprintf("%dMi", bytes/mi)
}

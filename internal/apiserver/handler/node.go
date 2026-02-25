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
		result = append(result, map[string]interface{}{
			"name":           n.Node.Name,
			"instanceType":   n.InstanceType,
			"instanceFamily": n.InstanceFamily,
			"nodeGroup":      n.NodeGroupID,
			"cpuCapacity":    n.CPUCapacity,
			"memCapacity":    n.MemoryCapacity,
			"cpuUsed":        n.CPUUsed,
			"memUsed":        n.MemoryUsed,
			"cpuUtilPct":     n.CPUUtilization(),
			"memUtilPct":     n.MemoryUtilization(),
			"hourlyCostUSD":  n.HourlyCostUSD,
			"isSpot":         n.IsSpot,
			"isGPU":          n.IsGPUNode,
			"podCount":       len(n.Pods),
		})
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *NodeHandler) Get(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	node, ok := h.state.GetNode(name)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "node not found"})
		return
	}

	type podSummary struct {
		Name       string `json:"name"`
		Namespace  string `json:"namespace"`
		CPURequest int64  `json:"cpuRequest"`
		MemRequest int64  `json:"memRequest"`
		Status     string `json:"status"`
	}
	pods := make([]podSummary, 0, len(node.Pods))
	for _, pod := range node.Pods {
		cpuMilli, memBytes := state.ExtractPodRequests(pod)
		phase := string(pod.Status.Phase)
		if phase == "" {
			phase = "Unknown"
		}
		pods = append(pods, podSummary{
			Name:       pod.Name,
			Namespace:  pod.Namespace,
			CPURequest: cpuMilli,
			MemRequest: memBytes,
			Status:     phase,
		})
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
		"pods":           pods,
	}

	// Build pods array for the detail table
	pods := make([]map[string]interface{}, 0, len(node.Pods))
	for _, pod := range node.Pods {
		cpuMilli, memBytes := state.ExtractPodRequests(pod)
		status := string(pod.Status.Phase)
		pods = append(pods, map[string]interface{}{
			"name":       pod.Name,
			"namespace":  pod.Namespace,
			"cpuRequest": fmt.Sprintf("%dm", cpuMilli),
			"memRequest": formatMemory(memBytes),
			"status":     status,
		})
	}
	resp["pods"] = pods

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

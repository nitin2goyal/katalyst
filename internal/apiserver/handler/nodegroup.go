package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/koptimizer/koptimizer/internal/state"
	"github.com/koptimizer/koptimizer/pkg/familylock"
)

type NodeGroupHandler struct {
	state *state.ClusterState
	guard *familylock.FamilyLockGuard
}

func NewNodeGroupHandler(st *state.ClusterState, guard *familylock.FamilyLockGuard) *NodeGroupHandler {
	return &NodeGroupHandler{state: st, guard: guard}
}

func (h *NodeGroupHandler) List(w http.ResponseWriter, r *http.Request) {
	groups := h.state.GetNodeGroups().GetAll()
	var result []map[string]interface{}
	for _, g := range groups {
		// Use actual node count from K8s (len(g.Nodes)) instead of cloud
		// provider's reported count (g.CurrentCount) which can be stale.
		nodeCount := len(g.Nodes)
		// Format taints for display
		var taints []map[string]string
		for _, t := range g.Taints {
			taints = append(taints, map[string]string{
				"key":    t.Key,
				"value":  t.Value,
				"effect": string(t.Effect),
			})
		}
		result = append(result, map[string]interface{}{
			"id":             g.ID,
			"name":           g.Name,
			"instanceType":   g.InstanceType,
			"instanceFamily": g.InstanceFamily,
			"currentCount":   nodeCount,
			"minCount":       g.MinCount,
			"maxCount":       g.MaxCount,
			"desiredCount":   g.DesiredCount,
			"cpuUtilPct":     g.CPUUtilization(),
			"memUtilPct":     g.MemoryUtilization(),
			"cpuAllocPct":    g.CPUAllocation(),
			"memAllocPct":    g.MemoryAllocation(),
			"totalCPU":       g.TotalCPU,
			"totalMemory":    g.TotalMemory,
			"totalPods":      g.TotalPods,
			"monthlyCostUSD": g.MonthlyCostUSD,
			"isEmpty":        g.IsEmpty(),
			"labels":         g.Labels,
			"taints":         taints,
		})
	}
	if result == nil {
		result = []map[string]interface{}{}
	}
	writePaginatedJSON(w, r, result)
}

func (h *NodeGroupHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	g, ok := h.state.GetNodeGroups().Get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "node group not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id":             g.ID,
		"name":           g.Name,
		"instanceType":   g.InstanceType,
		"instanceFamily": g.InstanceFamily,
		"currentCount":   len(g.Nodes),
		"minCount":       g.MinCount,
		"maxCount":       g.MaxCount,
		"desiredCount":   g.DesiredCount,
		"cpuUtilPct":     g.CPUUtilization(),
		"memUtilPct":     g.MemoryUtilization(),
		"totalCPU":       g.TotalCPU,
		"totalMemory":    g.TotalMemory,
		"monthlyCostUSD": g.MonthlyCostUSD,
	})
}

func (h *NodeGroupHandler) GetNodes(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	g, ok := h.state.GetNodeGroups().Get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "node group not found"})
		return
	}
	var nodes []map[string]interface{}
	for _, n := range g.Nodes {
		nodes = append(nodes, map[string]interface{}{
			"name":          n.Node.Name,
			"cpuUtilPct":    n.CPUUtilization(),
			"memUtilPct":    n.MemoryUtilization(),
			"hourlyCostUSD": n.HourlyCostUSD,
			"podCount":      len(n.Pods),
		})
	}
	writeJSON(w, http.StatusOK, nodes)
}

func (h *NodeGroupHandler) GetEmpty(w http.ResponseWriter, r *http.Request) {
	groups := h.state.GetNodeGroups().GetAll()
	var empty []map[string]interface{}
	for _, g := range groups {
		if g.IsEmpty() {
			empty = append(empty, map[string]interface{}{
				"id":           g.ID,
				"name":         g.Name,
				"instanceType": g.InstanceType,
				"currentCount": len(g.Nodes),
			})
		}
	}
	writeJSON(w, http.StatusOK, empty)
}

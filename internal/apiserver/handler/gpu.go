package handler

import (
	"fmt"
	"net/http"

	"github.com/koptimizer/koptimizer/internal/state"
)

type GPUHandler struct {
	state *state.ClusterState
}

func NewGPUHandler(st *state.ClusterState) *GPUHandler {
	return &GPUHandler{state: st}
}

func (h *GPUHandler) GetNodes(w http.ResponseWriter, r *http.Request) {
	nodes := h.state.GetAllNodes()
	var gpuNodes []map[string]interface{}
	for _, n := range nodes {
		if n.IsGPUNode {
			gpuNodes = append(gpuNodes, map[string]interface{}{
				"name":          n.Node.Name,
				"instanceType":  n.InstanceType,
				"gpuCount":      n.GPUCapacity,
				"gpuUsed":       n.GPUsUsed,
				"cpuUtilPct":    n.CPUUtilization(),
				"memUtilPct":    n.MemoryUtilization(),
				"hourlyCostUSD": n.HourlyCostUSD,
			})
		}
	}
	writeJSON(w, http.StatusOK, gpuNodes)
}

func (h *GPUHandler) GetUtilization(w http.ResponseWriter, r *http.Request) {
	nodes := h.state.GetAllNodes()
	totalGPUs := 0
	usedGPUs := 0
	for _, n := range nodes {
		if n.IsGPUNode {
			totalGPUs += n.GPUCapacity
			usedGPUs += n.GPUsUsed
		}
	}
	utilPct := 0.0
	if totalGPUs > 0 {
		utilPct = float64(usedGPUs) / float64(totalGPUs) * 100
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"totalGPUs":      totalGPUs,
		"usedGPUs":       usedGPUs,
		"utilizationPct": utilPct,
	})
}

func (h *GPUHandler) GetRecommendations(w http.ResponseWriter, r *http.Request) {
	nodes := h.state.GetAllNodes()
	var recommendations []map[string]interface{}

	for _, n := range nodes {
		if !n.IsGPUNode {
			continue
		}

		cpuUtil := n.CPUUtilization()
		gpuAllocated := n.GPUCapacity > 0 && n.GPUsUsed > 0

		// Idle GPU detection
		if n.GPUCapacity > 0 && n.GPUsUsed == 0 {
			// Estimated monthly savings = full node cost if idle GPUs can be released.
			idleSavings := n.HourlyCostUSD * 730
			recommendations = append(recommendations, map[string]interface{}{
				"type":             "gpu-idle",
				"priority":         "high",
				"node":             n.Node.Name,
				"nodeName":         n.Node.Name,
				"target":           n.Node.Name,
				"instanceType":     n.InstanceType,
				"gpuCount":         n.GPUCapacity,
				"description":      fmt.Sprintf("GPU node %s has %d GPUs allocated but 0 in use", n.Node.Name, n.GPUCapacity),
				"summary":          fmt.Sprintf("GPU node %s has %d GPUs allocated but 0 in use", n.Node.Name, n.GPUCapacity),
				"action":           "Consider enabling CPU fallback or scaling down",
				"estimatedSavings": idleSavings,
			})
		}

		// CPU scavenging opportunity
		if gpuAllocated && cpuUtil < 30 && n.CPUCapacity > 2000 {
			spareCPU := n.CPUCapacity - n.CPURequested
			// Estimated savings from reclaiming spare CPU (fraction of node cost).
			cpuFraction := float64(spareCPU) / float64(n.CPUCapacity)
			scavengeSavings := n.HourlyCostUSD * 730 * cpuFraction * 0.5 // conservative estimate
			recommendations = append(recommendations, map[string]interface{}{
				"type":             "cpu-scavenging",
				"priority":         "medium",
				"node":             n.Node.Name,
				"nodeName":         n.Node.Name,
				"target":           n.Node.Name,
				"instanceType":     n.InstanceType,
				"spareCPUMillis":   spareCPU,
				"description":      fmt.Sprintf("GPU node %s has %dm spare CPU available for scavenging", n.Node.Name, spareCPU),
				"summary":          fmt.Sprintf("GPU node %s has %dm spare CPU available for scavenging", n.Node.Name, spareCPU),
				"action":           "Enable CPU scavenging for low-priority workloads",
				"estimatedSavings": scavengeSavings,
			})
		}
	}

	writeJSON(w, http.StatusOK, recommendations)
}

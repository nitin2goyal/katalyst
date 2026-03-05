package handler

import (
	"fmt"
	"net/http"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/internal/state"
	"github.com/koptimizer/koptimizer/pkg/cost"
)

type GPUHandler struct {
	state  *state.ClusterState
	config *config.Config
}

func NewGPUHandler(st *state.ClusterState, cfg *config.Config) *GPUHandler {
	return &GPUHandler{state: st, config: cfg}
}

func (h *GPUHandler) GetNodes(w http.ResponseWriter, r *http.Request) {
	nodes := h.state.GetAllNodes()
	var gpuNodes []map[string]interface{}
	for _, n := range nodes {
		if n.IsGPUNode {
			entry := map[string]interface{}{
				"name":          n.Node.Name,
				"instanceType":  n.InstanceType,
				"gpuCount":      n.GPUCapacity,
				"gpuUsed":       n.GPUsUsed,
				"cpuUtilPct":    n.CPUUtilization(),
				"memUtilPct":    n.MemoryUtilization(),
				"hourlyCostUSD": n.HourlyCostUSD,
				"isFallback":    hasAnnotation(n.Node, "koptimizer.io/gpu-fallback"),
				"isScavenging":  hasAnnotation(n.Node, "koptimizer.io/cpu-scavenger"),
				"hasTaint":      hasNoScheduleTaint(n.Node, "nvidia.com/gpu"),
			}
			if v, ok := n.Node.Annotations["koptimizer.io/scavenger-cpu-millis"]; ok {
				entry["cpuHeadroomMillis"] = v
			}
			if v, ok := n.Node.Annotations["koptimizer.io/cpu-headroom-millis"]; ok {
				entry["cpuHeadroomMillis"] = v
			}
			gpuNodes = append(gpuNodes, entry)
		}
	}
	if gpuNodes == nil {
		gpuNodes = []map[string]interface{}{}
	}

	// Embed config summary
	gpuCfg := h.config.GPU
	result := map[string]interface{}{
		"nodes": gpuNodes,
		"config": map[string]interface{}{
			"enabled":                      gpuCfg.Enabled,
			"cpuFallbackEnabled":           gpuCfg.CPUFallbackEnabled,
			"cpuScavengingEnabled":         gpuCfg.CPUScavengingEnabled,
			"reclaimEnabled":               gpuCfg.ReclaimEnabled,
			"idleThresholdPct":             gpuCfg.IdleThresholdPct,
			"idleDuration":                 gpuCfg.IdleDuration.String(),
			"scavengingCPUThresholdMillis": gpuCfg.ScavengingCPUThresholdMillis,
			"reclaimGracePeriod":           gpuCfg.ReclaimGracePeriod.String(),
		},
		"total":    len(gpuNodes),
		"page":     1,
		"pageSize": len(gpuNodes),
	}
	writeJSON(w, http.StatusOK, result)
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
			idleSavings := n.HourlyCostUSD * cost.HoursPerMonth
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
			// Estimated savings from reclaiming spare CPU. On GPU nodes, CPU is ~5% of
			// total node cost, so use EstimateCPUCostFraction to avoid gross overestimate.
			cpuCostFraction := cost.EstimateCPUCostFraction(n.CPUCapacity, n.IsGPUNode)
			cpuShareFraction := float64(spareCPU) / float64(n.CPUCapacity)
			scavengeSavings := n.HourlyCostUSD * cost.HoursPerMonth * cpuCostFraction * cpuShareFraction * 0.5
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

	if recommendations == nil {
		recommendations = []map[string]interface{}{}
	}
	writePaginatedJSON(w, r, recommendations)
}

// GetActivity returns GPU-related audit events (actions prefixed with "gpu-" or "reclaim-gpu-").
func (h *GPUHandler) GetActivity(w http.ResponseWriter, r *http.Request) {
	allEvents := h.state.AuditLog.GetAll()

	var gpuEvents []state.AuditEvent
	for _, ev := range allEvents {
		if strings.HasPrefix(ev.Action, "gpu-") || strings.HasPrefix(ev.Action, "reclaim-gpu-") {
			gpuEvents = append(gpuEvents, ev)
		}
	}
	if gpuEvents == nil {
		gpuEvents = []state.AuditEvent{}
	}

	page, pageSize := parsePagination(r)
	start, end, resp := paginateSlice(len(gpuEvents), page, pageSize)
	resp.Data = gpuEvents[start:end]
	writeJSON(w, http.StatusOK, resp)
}

func hasAnnotation(node *corev1.Node, key string) bool {
	if node.Annotations == nil {
		return false
	}
	_, ok := node.Annotations[key]
	return ok
}

func hasNoScheduleTaint(node *corev1.Node, key string) bool {
	for _, t := range node.Spec.Taints {
		if t.Key == key && t.Effect == corev1.TaintEffectNoSchedule {
			return true
		}
	}
	return false
}

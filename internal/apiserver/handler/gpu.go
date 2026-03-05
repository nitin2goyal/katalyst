package handler

import (
	"fmt"
	"net/http"
	"strings"
	"time"

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
			"mode":                         h.config.Mode,
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

// GetScavenging returns all CPU (non-GPU, non-system) pods running on GPU nodes,
// along with node headroom info. This powers the dedicated scavenging dashboard tab.
func (h *GPUHandler) GetScavenging(w http.ResponseWriter, r *http.Request) {
	nodes := h.state.GetAllNodes()
	allPods := h.state.GetAllPods()

	// Build pod state lookup for usage metrics
	podStateMap := make(map[string]*state.PodState)
	for _, ps := range allPods {
		podStateMap[ps.Namespace+"/"+ps.Name] = ps
	}

	type cpuPodOnGPU struct {
		PodName       string  `json:"podName"`
		Namespace     string  `json:"namespace"`
		NodeName      string  `json:"nodeName"`
		InstanceType  string  `json:"instanceType"`
		CPURequestM   int64   `json:"cpuRequestMillis"`
		MemRequestMi  int64   `json:"memRequestMi"`
		CPUUsedM      int64   `json:"cpuUsedMillis"`
		MemUsedMi     int64   `json:"memUsedMi"`
		Status        string  `json:"status"`
		Ready         string  `json:"ready"`
		StartTime     string  `json:"startTime"`
		RunningFor    string  `json:"runningFor"`
		Owner         string  `json:"owner"`
		IsScavenging  bool    `json:"isScavenging"`
		NodeHeadroom  int64   `json:"nodeHeadroomMillis"`
		NodeCPUCap    int64   `json:"nodeCPUCapMillis"`
		NodeCPUReq    int64   `json:"nodeCPUReqMillis"`
		NodeCostHr    float64 `json:"nodeCostPerHour"`
	}

	var cpuPods []cpuPodOnGPU
	totalHeadroom := int64(0)
	scavengingNodeCount := 0

	for _, n := range nodes {
		if !n.IsGPUNode {
			continue
		}

		isScav := hasAnnotation(n.Node, "koptimizer.io/cpu-scavenger")
		if isScav {
			scavengingNodeCount++
		}
		headroom := n.CPUCapacity - n.CPURequested
		if headroom < 0 {
			headroom = 0
		}
		totalHeadroom += headroom

		for _, pod := range n.Pods {
			if IsSystemPod(pod) {
				continue
			}
			// Skip GPU pods
			isGPU := false
			for _, c := range pod.Spec.Containers {
				if gpuQty, ok := c.Resources.Requests["nvidia.com/gpu"]; ok && !gpuQty.IsZero() {
					isGPU = true
					break
				}
			}
			if isGPU {
				continue
			}

			cpuMilli, memBytes := state.ExtractPodRequests(pod)
			status := computePodStatus(pod)
			ready := computeContainerReady(pod)

			startTime := ""
			runningFor := ""
			if pod.Status.StartTime != nil {
				startTime = pod.Status.StartTime.Format("2006-01-02T15:04:05Z")
				dur := time.Since(pod.Status.StartTime.Time)
				if dur < time.Hour {
					runningFor = fmt.Sprintf("%dm", int(dur.Minutes()))
				} else if dur < 24*time.Hour {
					runningFor = fmt.Sprintf("%dh%dm", int(dur.Hours()), int(dur.Minutes())%60)
				} else {
					runningFor = fmt.Sprintf("%dd%dh", int(dur.Hours()/24), int(dur.Hours())%24)
				}
			}

			owner := ""
			for _, ref := range pod.OwnerReferences {
				owner = ref.Kind + "/" + ref.Name
				break
			}

			entry := cpuPodOnGPU{
				PodName:      pod.Name,
				Namespace:    pod.Namespace,
				NodeName:     n.Node.Name,
				InstanceType: n.InstanceType,
				CPURequestM:  cpuMilli,
				MemRequestMi: memBytes / (1024 * 1024),
				Status:       status,
				Ready:        ready,
				StartTime:    startTime,
				RunningFor:   runningFor,
				Owner:        owner,
				IsScavenging: isScav,
				NodeHeadroom: headroom,
				NodeCPUCap:   n.CPUCapacity,
				NodeCPUReq:   n.CPURequested,
				NodeCostHr:   n.HourlyCostUSD,
			}

			if ps, ok := podStateMap[pod.Namespace+"/"+pod.Name]; ok {
				entry.CPUUsedM = ps.CPUUsage
				entry.MemUsedMi = ps.MemoryUsage / (1024 * 1024)
			}

			cpuPods = append(cpuPods, entry)
		}
	}

	if cpuPods == nil {
		cpuPods = []cpuPodOnGPU{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"pods":                cpuPods,
		"totalCPUPods":        len(cpuPods),
		"scavengingNodeCount": scavengingNodeCount,
		"totalHeadroomMillis": totalHeadroom,
		"mode":                h.config.Mode,
		"scavengingEnabled":   h.config.GPU.CPUScavengingEnabled,
	})
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

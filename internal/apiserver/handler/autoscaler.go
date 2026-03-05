package handler

import (
	"net/http"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/internal/state"
)

// AutoscalerHandler provides the autoscaler status dashboard endpoints.
type AutoscalerHandler struct {
	state  *state.ClusterState
	config *config.Config
}

// NewAutoscalerHandler creates a new AutoscalerHandler.
func NewAutoscalerHandler(st *state.ClusterState, cfg *config.Config) *AutoscalerHandler {
	return &AutoscalerHandler{state: st, config: cfg}
}

// GetStatus returns autoscaler config summary, node counts, and per-node analysis
// with removal blockers.
func (h *AutoscalerHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	nodes := h.state.GetAllNodes()

	// Config summary
	configSummary := map[string]interface{}{
		"mode":                   h.config.Mode,
		"evictorEnabled":         h.config.Evictor.Enabled,
		"evictorDryRun":          h.config.Evictor.DryRun,
		"evictorAutoApprove":     h.config.Evictor.AutoApprove,
		"nodeAutoscalerEnabled":  h.config.NodeAutoscaler.Enabled,
		"nodeAutoscalerDryRun":   h.config.NodeAutoscaler.DryRun,
		"utilizationThreshold":   h.config.Evictor.UtilizationThreshold,
		"maxConcurrentEvictions": h.config.Evictor.MaxConcurrentEvictions,
		"partialDrainTTL":        h.config.Evictor.PartialDrainTTL.String(),
		"drainTimeout":           h.config.Evictor.DrainTimeout.String(),
		"scaleDownThreshold":     h.config.NodeAutoscaler.ScaleDownThreshold,
		"scaleDownDelay":         h.config.NodeAutoscaler.ScaleDownDelay.String(),
		"maxScaleDownNodes":      h.config.NodeAutoscaler.MaxScaleDownNodes,
	}

	// Count summary and build node analysis
	totalNodes := len(nodes)
	emptyNodes := 0
	cordonedNodes := 0
	cordonedByUs := 0
	cordonedExternal := 0
	underutilizedNodes := 0
	blockedNodes := 0

	// Count nodes currently cordoned by us (for max concurrent check)
	currentCordonedByUs := 0
	for _, n := range nodes {
		if n.Node.Spec.Unschedulable {
			if _, ours := n.Node.Annotations["koptimizer.io/cordoned-by"]; ours {
				currentCordonedByUs++
			}
		}
	}

	var analysis []map[string]interface{}

	for _, n := range nodes {
		isEmpty := n.IsEmpty()
		isCordoned := n.Node.Spec.Unschedulable
		isUnderutilized := n.IsUnderutilized(h.config.Evictor.UtilizationThreshold)

		if isEmpty {
			emptyNodes++
		}
		if isCordoned {
			cordonedNodes++
		}
		if isUnderutilized && !isCordoned {
			underutilizedNodes++
		}

		// Determine cordon source
		cordonedBy := ""
		cordonedAt := ""
		if isCordoned {
			if _, ours := n.Node.Annotations["koptimizer.io/cordoned-by"]; ours {
				cordonedBy = "koptimizer"
				cordonedByUs++
			} else {
				cordonedBy = "external"
				cordonedExternal++
			}
			if ts, ok := n.Node.Annotations["koptimizer.io/cordoned-at"]; ok {
				cordonedAt = ts
			}
		}

		// Only include nodes that are interesting: empty, cordoned, or underutilized
		if !isEmpty && !isCordoned && !isUnderutilized {
			continue
		}

		// Count pod types and check evictability
		appPods, dsPods := 0, 0
		hasNonEvictable := false
		for _, pod := range n.Pods {
			if IsSystemPod(pod) {
				dsPods++
			} else {
				appPods++
				if !isEvictablePod(pod) {
					hasNonEvictable = true
				}
			}
		}

		// Compute removal blockers
		blockers := h.computeBlockers(n, isEmpty, hasNonEvictable, currentCordonedByUs)
		if len(blockers) > 0 {
			blockedNodes++
		}

		entry := map[string]interface{}{
			"name":              n.Node.Name,
			"nodeGroup":         n.NodeGroupID,
			"instanceType":      n.InstanceType,
			"isEmpty":           isEmpty,
			"isCordoned":        isCordoned,
			"cordonedBy":        cordonedBy,
			"cordonedAt":        cordonedAt,
			"isUnderutilized":   isUnderutilized,
			"appPodCount":       appPods,
			"daemonSetPodCount": dsPods,
			"cpuAllocPct":       n.CPURequestUtilization(),
			"memAllocPct":       n.MemoryRequestUtilization(),
			"cpuUtilPct":        n.CPUUtilization(),
			"memUtilPct":        n.MemoryUtilization(),
			"hourlyCostUSD":     n.HourlyCostUSD,
			"removalBlockers":   blockers,
		}
		analysis = append(analysis, entry)
	}

	// Sort analysis: blocked first, then by hourly cost descending
	sort.Slice(analysis, func(i, j int) bool {
		bi := len(analysis[i]["removalBlockers"].([]string))
		bj := len(analysis[j]["removalBlockers"].([]string))
		if bi != bj {
			return bi > bj
		}
		ci := analysis[i]["hourlyCostUSD"].(float64)
		cj := analysis[j]["hourlyCostUSD"].(float64)
		return ci > cj
	})

	if analysis == nil {
		analysis = []map[string]interface{}{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"config": configSummary,
		"summary": map[string]interface{}{
			"totalNodes":         totalNodes,
			"emptyNodes":         emptyNodes,
			"cordonedNodes":      cordonedNodes,
			"cordonedByUs":       cordonedByUs,
			"cordonedExternal":   cordonedExternal,
			"underutilizedNodes": underutilizedNodes,
			"blockedNodes":       blockedNodes,
		},
		"nodes": analysis,
	})
}

// computeBlockers returns human-readable reasons why this node can't be auto-removed.
func (h *AutoscalerHandler) computeBlockers(
	n *state.NodeState,
	isEmpty bool,
	hasNonEvictable bool,
	currentCordonedByUs int,
) []string {
	var blockers []string

	// Config-level blockers
	if !h.config.Evictor.Enabled {
		blockers = append(blockers, "Evictor disabled")
	} else if h.config.Evictor.DryRun {
		blockers = append(blockers, "Evictor in dry-run")
	}

	if !h.config.NodeAutoscaler.Enabled {
		blockers = append(blockers, "Node autoscaler disabled")
	} else if h.config.NodeAutoscaler.DryRun {
		blockers = append(blockers, "Node autoscaler in dry-run")
	}

	if h.config.Mode == "recommend" || h.config.Mode == "monitor" {
		blockers = append(blockers, "Mode is "+h.config.Mode+" (not active)")
	}

	// Operational blockers (only relevant for non-cordoned nodes)
	if !n.Node.Spec.Unschedulable {
		if currentCordonedByUs >= h.config.Evictor.MaxConcurrentEvictions {
			blockers = append(blockers, "Max concurrent evictions reached")
		}

		if !isEmpty && !n.IsUnderutilized(h.config.Evictor.UtilizationThreshold) {
			blockers = append(blockers, "Utilization above threshold")
		}

		if hasNonEvictable {
			blockers = append(blockers, "Has non-evictable pods (local storage/standalone)")
		}
	}

	// Annotation-based exclusions
	if v, ok := n.Node.Annotations["koptimizer.io/exclude"]; ok && v == "true" {
		blockers = append(blockers, "Excluded via annotation")
	}
	if v, ok := n.Node.Annotations["cluster-autoscaler.kubernetes.io/scale-down-disabled"]; ok && v == "true" {
		blockers = append(blockers, "Scale-down disabled via annotation")
	}

	return blockers
}

// isEvictablePod checks if a pod can be safely evicted for node drain.
func isEvictablePod(pod *corev1.Pod) bool {
	// Mirror pods (static pods)
	if _, ok := pod.Annotations["kubernetes.io/config.mirror"]; ok {
		return false
	}
	// Pods with local storage (unless opted in)
	for _, vol := range pod.Spec.Volumes {
		if vol.EmptyDir != nil || vol.HostPath != nil {
			if pod.Annotations["koptimizer.io/safe-to-evict"] != "true" &&
				pod.Annotations["cluster-autoscaler.kubernetes.io/safe-to-evict"] != "true" {
				return false
			}
		}
	}
	// Standalone pods (no owner — won't be recreated)
	if len(pod.OwnerReferences) == 0 {
		return false
	}
	return true
}

// GetEvents returns autoscaler-related events from the audit log, sorted by time desc.
func (h *AutoscalerHandler) GetEvents(w http.ResponseWriter, r *http.Request) {
	autoscalerActions := map[string]bool{
		"scale-nodegroup":       true,
		"scale-up":              true,
		"scale-down":            true,
		"drain-node":            true,
		"drain-failed":          true,
		"drain-complete":        true,
		"cordon-node":           true,
		"uncordon-node":         true,
		"auto-uncordon":         true,
		"auto-uncordon-partial": true,
		"dry-run-drain":         true,
		"dry-run-scale":         true,
		"dry-run-cordon":        true,
		"evictor-recommend":     true,
		"evictor-drain":         true,
		"evictor-dry-run":       true,
		"consolidation-check":   true,
	}

	allAudit := h.state.AuditLog.GetAll()
	var merged []map[string]interface{}

	for _, e := range allAudit {
		if !autoscalerActions[e.Action] {
			continue
		}
		merged = append(merged, map[string]interface{}{
			"timestamp": e.Timestamp.Format(time.RFC3339),
			"source":    "KOptimizer",
			"action":    e.Action,
			"target":    e.Target,
			"user":      e.User,
			"details":   e.Details,
		})
	}

	// Sort by timestamp descending
	sort.Slice(merged, func(i, j int) bool {
		return merged[i]["timestamp"].(string) > merged[j]["timestamp"].(string)
	})

	if merged == nil {
		merged = []map[string]interface{}{}
	}

	page, pageSize := parsePagination(r)
	start, end, resp := paginateSlice(len(merged), page, pageSize)
	resp.Data = merged[start:end]
	writeJSON(w, http.StatusOK, resp)
}

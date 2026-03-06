package handler

import (
	"context"
	"net/http"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/internal/state"
)

// AutoscalerHandler provides the autoscaler status dashboard endpoints.
// Node lifecycle (cordon/drain/scale) is managed by GKE autoscaler;
// this handler provides observability into node state and GKE events.
type AutoscalerHandler struct {
	state  *state.ClusterState
	config *config.Config
	client client.Client
}

// NewAutoscalerHandler creates a new AutoscalerHandler.
func NewAutoscalerHandler(st *state.ClusterState, cfg *config.Config, c client.Client) *AutoscalerHandler {
	return &AutoscalerHandler{state: st, config: cfg, client: c}
}

// GetStatus returns node counts and per-node analysis (observability only).
func (h *AutoscalerHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	nodes := h.state.GetAllNodes()

	// Count summary and build node analysis
	totalNodes := len(nodes)
	emptyNodes := 0
	cordonedNodes := 0
	cordonedByUs := 0
	cordonedExternal := 0

	var analysis []map[string]interface{}

	for _, n := range nodes {
		isEmpty := n.IsEmpty()
		isCordoned := n.Node.Spec.Unschedulable

		if isEmpty {
			emptyNodes++
		}
		if isCordoned {
			cordonedNodes++
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

		// Only include nodes that are interesting: empty or cordoned
		if !isEmpty && !isCordoned {
			continue
		}

		// Count pod types
		appPods, dsPods := 0, 0
		for _, pod := range n.Pods {
			if IsSystemPod(pod) {
				dsPods++
			} else {
				appPods++
			}
		}

		entry := map[string]interface{}{
			"name":              n.Node.Name,
			"nodeGroup":         n.NodeGroupID,
			"instanceType":      n.InstanceType,
			"isEmpty":           isEmpty,
			"isCordoned":        isCordoned,
			"cordonedBy":        cordonedBy,
			"cordonedAt":        cordonedAt,
			"appPodCount":       appPods,
			"daemonSetPodCount": dsPods,
			"cpuAllocPct":       n.CPURequestUtilization(),
			"memAllocPct":       n.MemoryRequestUtilization(),
			"cpuUtilPct":        n.CPUUtilization(),
			"memUtilPct":        n.MemoryUtilization(),
			"hourlyCostUSD":     n.HourlyCostUSD,
		}
		analysis = append(analysis, entry)
	}

	// Sort analysis by hourly cost descending
	sort.Slice(analysis, func(i, j int) bool {
		ci := analysis[i]["hourlyCostUSD"].(float64)
		cj := analysis[j]["hourlyCostUSD"].(float64)
		return ci > cj
	})

	if analysis == nil {
		analysis = []map[string]interface{}{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"config": map[string]interface{}{
			"mode":    h.config.GetMode(),
			"message": "Node lifecycle managed by GKE cluster autoscaler",
		},
		"summary": map[string]interface{}{
			"totalNodes":       totalNodes,
			"emptyNodes":       emptyNodes,
			"cordonedNodes":    cordonedNodes,
			"cordonedByUs":     cordonedByUs,
			"cordonedExternal": cordonedExternal,
		},
		"nodes": analysis,
	})
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

// gkeAutoscalerReasons maps GKE cluster-autoscaler event reasons to readable actions.
var gkeAutoscalerReasons = map[string]string{
	"ScaleDown":            "scale-down",
	"ScaleDownEmpty":       "scale-down-empty",
	"ScaleDownUnneeded":    "scale-down-unneeded",
	"ScaleUp":              "scale-up",
	"ScaledUpGroup":        "scale-up-group",
	"TriggeredScaleUp":     "triggered-scale-up",
	"NotTriggerScaleUp":    "scale-up-not-needed",
	"ScaleDownFailed":      "scale-down-failed",
	"ScaleDownUnready":     "scale-down-unready",
	"DeleteUnneeded":       "delete-unneeded",
	"DeleteUnregistered":   "delete-unregistered",
	"ScaleDownDeferral":    "scale-down-deferred",
}

// GetEvents returns autoscaler-related events: GKE cluster-autoscaler k8s events + KOptimizer audit.
func (h *AutoscalerHandler) GetEvents(w http.ResponseWriter, r *http.Request) {
	var merged []map[string]interface{}

	// Fetch GKE cluster-autoscaler events from the Kubernetes API
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var eventList corev1.EventList
	if err := h.client.List(ctx, &eventList); err == nil {
		for i := range eventList.Items {
			ev := &eventList.Items[i]

			// Filter: only cluster-autoscaler events
			if ev.Source.Component != "cluster-autoscaler" {
				continue
			}

			action := ev.Reason
			if mapped, ok := gkeAutoscalerReasons[ev.Reason]; ok {
				action = mapped
			}

			// Use last timestamp, fall back to first timestamp
			ts := ev.LastTimestamp.Time
			if ts.IsZero() {
				ts = ev.FirstTimestamp.Time
			}
			if ts.IsZero() {
				ts = ev.CreationTimestamp.Time
			}

			target := ""
			if ev.InvolvedObject.Kind == "Node" {
				target = ev.InvolvedObject.Name
			} else if ev.InvolvedObject.Kind == "Pod" {
				target = ev.InvolvedObject.Namespace + "/" + ev.InvolvedObject.Name
			} else {
				target = ev.InvolvedObject.Name
			}

			merged = append(merged, map[string]interface{}{
				"timestamp": ts.Format(time.RFC3339),
				"source":    "GKE",
				"action":    action,
				"target":    target,
				"user":      "cluster-autoscaler",
				"details":   ev.Message,
			})
		}
	}

	// Also include KOptimizer's own autoscaler-related audit events
	autoscalerActions := map[string]bool{
		"scale-nodegroup":       true,
		"drain-node":            true,
		"drain-failed":          true,
		"drain-complete":        true,
		"cordon-node":           true,
		"uncordon-node":         true,
		"auto-uncordon":         true,
		"auto-uncordon-partial": true,
	}

	allAudit := h.state.AuditLog.GetAll()
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

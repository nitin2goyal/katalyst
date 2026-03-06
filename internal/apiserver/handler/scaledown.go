package handler

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/koptimizer/koptimizer/internal/state"
)

// ScaleDownBlockersHandler provides a consolidated view of everything
// preventing the cluster autoscaler from scaling down nodes.
type ScaleDownBlockersHandler struct {
	state  *state.ClusterState
	client client.Client
}

func NewScaleDownBlockersHandler(st *state.ClusterState, c client.Client) *ScaleDownBlockersHandler {
	return &ScaleDownBlockersHandler{state: st, client: c}
}

// GetBlockers returns a consolidated view of all scale-down blockers.
func (h *ScaleDownBlockersHandler) GetBlockers(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	// 1. Blocking PDBs: disruptionsAllowed == 0
	blockingPDBs := h.getBlockingPDBs(ctx)

	// 2. ScaleDownFailed events from cluster-autoscaler
	failedEvents := h.getScaleDownFailedEvents(ctx)

	// 3. Single-replica deployments protected by PDBs
	singleReplicaPDBs := h.getSingleReplicaWithPDB(ctx, blockingPDBs)

	// 4. Problematic pods that block eviction (CrashLoopBackOff, not ready, etc.)
	problematicPods := h.getProblematicPods(ctx)

	// 5. Unevictable pods (local storage, no owner, mirror pods)
	unevictablePods := h.getUnevictablePods()

	// Summary
	summary := map[string]interface{}{
		"blockingPDBs":      len(blockingPDBs),
		"failedEvents":      len(failedEvents),
		"singleReplicaPDBs": len(singleReplicaPDBs),
		"problematicPods":   len(problematicPods),
		"unevictablePods":   len(unevictablePods),
	}

	// Dedupe affected nodes
	affectedNodes := map[string]bool{}
	for _, e := range failedEvents {
		if n, ok := e["nodeName"].(string); ok && n != "" {
			affectedNodes[n] = true
		}
	}
	for _, p := range unevictablePods {
		if n, ok := p["nodeName"].(string); ok && n != "" {
			affectedNodes[n] = true
		}
	}
	summary["affectedNodes"] = len(affectedNodes)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"summary":           summary,
		"blockingPDBs":      blockingPDBs,
		"failedEvents":      failedEvents,
		"singleReplicaPDBs": singleReplicaPDBs,
		"problematicPods":   problematicPods,
		"unevictablePods":   unevictablePods,
	})
}

// getBlockingPDBs returns PDBs where disruptionsAllowed == 0.
func (h *ScaleDownBlockersHandler) getBlockingPDBs(ctx context.Context) []map[string]interface{} {
	var pdbList policyv1.PodDisruptionBudgetList
	if err := h.client.List(ctx, &pdbList); err != nil {
		return []map[string]interface{}{}
	}

	var result []map[string]interface{}
	for i := range pdbList.Items {
		pdb := &pdbList.Items[i]
		if pdb.Status.DisruptionsAllowed != 0 {
			continue
		}

		// Calculate age
		age := time.Since(pdb.CreationTimestamp.Time)
		ageDays := int(age.Hours() / 24)

		// Determine why disruptions are 0
		reason := "unknown"
		if pdb.Status.CurrentHealthy <= pdb.Status.DesiredHealthy && pdb.Status.DesiredHealthy > 0 {
			reason = "at-minimum-healthy"
		}
		if pdb.Status.ExpectedPods == 0 {
			reason = "no-matching-pods"
		}
		if pdb.Spec.MinAvailable != nil {
			reason = fmt.Sprintf("minAvailable=%s", pdb.Spec.MinAvailable.String())
		}
		if pdb.Spec.MaxUnavailable != nil {
			if pdb.Spec.MaxUnavailable.IntValue() == 0 {
				reason = "maxUnavailable=0"
			}
		}

		// Count matched pods
		matchedPods := pdb.Status.ExpectedPods

		entry := map[string]interface{}{
			"name":               pdb.Name,
			"namespace":          pdb.Namespace,
			"disruptionsAllowed": pdb.Status.DisruptionsAllowed,
			"currentHealthy":     pdb.Status.CurrentHealthy,
			"desiredHealthy":     pdb.Status.DesiredHealthy,
			"expectedPods":       matchedPods,
			"reason":             reason,
			"ageDays":            ageDays,
			"createdAt":          pdb.CreationTimestamp.Format(time.RFC3339),
		}

		if pdb.Spec.Selector != nil {
			selectorStr := metav1.FormatLabelSelector(pdb.Spec.Selector)
			entry["selector"] = selectorStr
		}

		result = append(result, entry)
	}

	// Sort by age descending (oldest first — most stale)
	sort.Slice(result, func(i, j int) bool {
		return result[i]["ageDays"].(int) > result[j]["ageDays"].(int)
	})

	if result == nil {
		return []map[string]interface{}{}
	}
	return result
}

// getScaleDownFailedEvents returns recent ScaleDownFailed events from the cluster-autoscaler.
func (h *ScaleDownBlockersHandler) getScaleDownFailedEvents(ctx context.Context) []map[string]interface{} {
	var eventList corev1.EventList
	if err := h.client.List(ctx, &eventList); err != nil {
		return []map[string]interface{}{}
	}

	var result []map[string]interface{}
	for i := range eventList.Items {
		ev := &eventList.Items[i]
		if ev.Source.Component != "cluster-autoscaler" {
			continue
		}
		if ev.Reason != "ScaleDownFailed" {
			continue
		}

		ts := ev.LastTimestamp.Time
		if ts.IsZero() {
			ts = ev.FirstTimestamp.Time
		}
		if ts.IsZero() {
			ts = ev.CreationTimestamp.Time
		}

		nodeName := ""
		if ev.InvolvedObject.Kind == "Node" {
			nodeName = ev.InvolvedObject.Name
		}

		// Parse pod names from the error message
		blockedBy := parseBlockingPods(ev.Message)

		result = append(result, map[string]interface{}{
			"timestamp":  ts.Format(time.RFC3339),
			"nodeName":   nodeName,
			"message":    ev.Message,
			"count":      ev.Count,
			"blockedBy":  blockedBy,
		})
	}

	// Sort by timestamp descending
	sort.Slice(result, func(i, j int) bool {
		return result[i]["timestamp"].(string) > result[j]["timestamp"].(string)
	})

	if result == nil {
		return []map[string]interface{}{}
	}
	return result
}

// parseBlockingPods extracts pod references from ScaleDownFailed messages.
// Example: "failed to evict pod spr-apps/sch-ms-tier1-deployment-88b86cc5f-kpjrg within allowed timeout
// (last error: Cannot evict pod as it would violate the pod's disruption budget.)"
func parseBlockingPods(message string) []map[string]string {
	var pods []map[string]string
	parts := strings.Split(message, "failed to evict pod ")
	for _, part := range parts[1:] { // skip the first part (before any "failed to evict pod")
		// Extract "namespace/podname" up to the next space or " within"
		ref := part
		if idx := strings.Index(ref, " within"); idx > 0 {
			ref = ref[:idx]
		} else if idx := strings.IndexByte(ref, ' '); idx > 0 {
			ref = ref[:idx]
		}
		ref = strings.TrimRight(ref, ",")

		reason := "PDB violation"
		if strings.Contains(part, "eviction subresource does not support") {
			reason = "eviction API not supported"
		} else if strings.Contains(part, "disruption budget") {
			reason = "PDB violation"
		} else if strings.Contains(part, "has more than one PodDisruptionBudget") {
			reason = "multiple PDBs"
		}

		ns, name := "", ref
		if idx := strings.IndexByte(ref, '/'); idx >= 0 {
			ns = ref[:idx]
			name = ref[idx+1:]
		}

		pods = append(pods, map[string]string{
			"namespace": ns,
			"pod":       name,
			"reason":    reason,
		})
	}
	return pods
}

// getSingleReplicaWithPDB finds deployments with 1 replica that are matched by a blocking PDB.
func (h *ScaleDownBlockersHandler) getSingleReplicaWithPDB(ctx context.Context, blockingPDBs []map[string]interface{}) []map[string]interface{} {
	var deployList appsv1.DeploymentList
	if err := h.client.List(ctx, &deployList); err != nil {
		return []map[string]interface{}{}
	}

	// Build PDB lookup: namespace -> []PDB
	pdbsByNS := map[string][]map[string]interface{}{}
	for _, pdb := range blockingPDBs {
		ns := pdb["namespace"].(string)
		pdbsByNS[ns] = append(pdbsByNS[ns], pdb)
	}

	// Also fetch actual PDB objects for selector matching
	var pdbList policyv1.PodDisruptionBudgetList
	_ = h.client.List(ctx, &pdbList)
	pdbObjsByNS := map[string][]*policyv1.PodDisruptionBudget{}
	for i := range pdbList.Items {
		pdb := &pdbList.Items[i]
		if pdb.Status.DisruptionsAllowed == 0 {
			pdbObjsByNS[pdb.Namespace] = append(pdbObjsByNS[pdb.Namespace], pdb)
		}
	}

	var result []map[string]interface{}
	for i := range deployList.Items {
		deploy := &deployList.Items[i]
		replicas := int32(1)
		if deploy.Spec.Replicas != nil {
			replicas = *deploy.Spec.Replicas
		}
		if replicas != 1 {
			continue
		}

		// Check if any blocking PDB in this namespace matches this deployment's pods
		matchingPDB := ""
		for _, pdb := range pdbObjsByNS[deploy.Namespace] {
			if pdb.Spec.Selector == nil {
				continue
			}
			sel, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
			if err != nil {
				continue
			}
			if sel.Matches(labels.Set(deploy.Spec.Template.Labels)) {
				matchingPDB = pdb.Name
				break
			}
		}

		if matchingPDB == "" {
			continue
		}

		readyReplicas := deploy.Status.ReadyReplicas
		result = append(result, map[string]interface{}{
			"name":          deploy.Name,
			"namespace":     deploy.Namespace,
			"replicas":      replicas,
			"readyReplicas": readyReplicas,
			"matchingPDB":   matchingPDB,
		})
	}

	if result == nil {
		return []map[string]interface{}{}
	}
	return result
}

// getProblematicPods returns pods with issues that may block node drain.
func (h *ScaleDownBlockersHandler) getProblematicPods(ctx context.Context) []map[string]interface{} {
	nodes := h.state.GetAllNodes()

	var result []map[string]interface{}
	for _, n := range nodes {
		for _, pod := range n.Pods {
			if IsSystemPod(pod) {
				continue
			}
			status := computePodStatus(pod)
			isBad := badStatusSet[status]

			// Also flag pods with high restart counts
			highRestarts := false
			maxRestarts := int32(0)
			for _, cs := range pod.Status.ContainerStatuses {
				if cs.RestartCount > maxRestarts {
					maxRestarts = cs.RestartCount
				}
				if cs.RestartCount > 10 {
					highRestarts = true
				}
			}

			if !isBad && !highRestarts {
				continue
			}

			readiness := computeContainerReady(pod)

			result = append(result, map[string]interface{}{
				"name":      pod.Name,
				"namespace": pod.Namespace,
				"nodeName":  n.Node.Name,
				"status":    status,
				"readiness": readiness,
				"restarts":  maxRestarts,
				"age":       timeAgoStr(pod.CreationTimestamp.Time),
			})
		}
	}

	// Sort by restarts descending
	sort.Slice(result, func(i, j int) bool {
		return result[i]["restarts"].(int32) > result[j]["restarts"].(int32)
	})

	if result == nil {
		return []map[string]interface{}{}
	}
	return result
}

// getUnevictablePods returns pods that the autoscaler cannot evict:
// local storage, no owner (standalone), mirror pods.
func (h *ScaleDownBlockersHandler) getUnevictablePods() []map[string]interface{} {
	nodes := h.state.GetAllNodes()

	var result []map[string]interface{}
	for _, n := range nodes {
		for _, pod := range n.Pods {
			if IsSystemPod(pod) {
				continue
			}
			if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
				continue
			}

			reason := unevictableReason(pod)
			if reason == "" {
				continue
			}

			result = append(result, map[string]interface{}{
				"name":      pod.Name,
				"namespace": pod.Namespace,
				"nodeName":  n.Node.Name,
				"reason":    reason,
				"age":       timeAgoStr(pod.CreationTimestamp.Time),
			})
		}
	}

	if result == nil {
		return []map[string]interface{}{}
	}
	return result
}

// unevictableReason returns why a pod can't be evicted, or "" if it can.
func unevictableReason(pod *corev1.Pod) string {
	// Mirror pods
	if _, ok := pod.Annotations["kubernetes.io/config.mirror"]; ok {
		return "mirror-pod"
	}
	// Standalone pods (no owner — won't be recreated)
	if len(pod.OwnerReferences) == 0 {
		return "no-owner"
	}
	// Local storage without safe-to-evict annotation
	for _, vol := range pod.Spec.Volumes {
		if vol.EmptyDir != nil || vol.HostPath != nil {
			if pod.Annotations["cluster-autoscaler.kubernetes.io/safe-to-evict"] != "true" &&
				pod.Annotations["koptimizer.io/safe-to-evict"] != "true" {
				return "local-storage"
			}
		}
	}
	return ""
}

func timeAgoStr(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

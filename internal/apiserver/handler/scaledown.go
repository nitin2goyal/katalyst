package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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

	// Autoscaler components that emit scale-down failure events
	autoscalerSources := map[string]bool{
		"cluster-autoscaler": true,
		"karpenter":          true,
	}
	failureReasons := map[string]bool{
		"ScaleDownFailed":   true,
		"DisruptionBlocked": true, // Karpenter equivalent
	}

	var result []map[string]interface{}
	for i := range eventList.Items {
		ev := &eventList.Items[i]
		if !autoscalerSources[ev.Source.Component] {
			continue
		}
		if !failureReasons[ev.Reason] {
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
		// Try to extract node name from the message if not on the InvolvedObject
		if nodeName == "" {
			nodeName = extractNodeFromMessage(ev.Message)
		}

		// Parse pod names from the error message
		blockedBy := parseBlockingPods(ev.Message)

		// Skip events with no actionable info (no node, no blocking pods)
		if nodeName == "" && len(blockedBy) == 0 {
			continue
		}

		result = append(result, map[string]interface{}{
			"timestamp": ts.Format(time.RFC3339),
			"nodeName":  nodeName,
			"message":   ev.Message,
			"count":     ev.Count,
			"blockedBy": blockedBy,
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
// Handles multiple message formats:
//   - "failed to evict pod spr-apps/pod-name within allowed timeout (last error: ...)"
//   - "failed to delete pod for ScaleDown"
//   - "[failed to evict pod ns/pod within allowed timeout (...)]"
func parseBlockingPods(message string) []map[string]string {
	var pods []map[string]string

	// Pattern 1: "failed to evict pod <ns/pod>"
	parts := strings.Split(message, "failed to evict pod ")
	for _, part := range parts[1:] {
		ref := part
		if idx := strings.Index(ref, " within"); idx > 0 {
			ref = ref[:idx]
		} else if idx := strings.IndexByte(ref, ' '); idx > 0 {
			ref = ref[:idx]
		}
		ref = strings.TrimRight(ref, ",.])")

		reason := "PDB violation"
		if strings.Contains(part, "eviction subresource does not support") {
			reason = "eviction API not supported"
		} else if strings.Contains(part, "has more than one PodDisruptionBudget") {
			reason = "multiple PDBs"
		} else if strings.Contains(part, "disruption budget") {
			reason = "PDB violation"
		}

		ns, name := "", ref
		if idx := strings.IndexByte(ref, '/'); idx >= 0 {
			ns = ref[:idx]
			name = ref[idx+1:]
		}
		if name != "" {
			pods = append(pods, map[string]string{
				"namespace": ns,
				"pod":       name,
				"reason":    reason,
			})
		}
	}

	// Pattern 2: "failed to delete pod <ns/pod> for ScaleDown"
	// Only extract if there's an actual ns/pod reference (contains "/")
	deleteParts := strings.Split(message, "failed to delete pod ")
	for _, part := range deleteParts[1:] {
		ref := part
		if idx := strings.Index(ref, " for "); idx > 0 {
			ref = ref[:idx]
		} else if idx := strings.IndexByte(ref, ' '); idx > 0 {
			ref = ref[:idx]
		}
		ref = strings.TrimRight(ref, ",.])")

		// Must contain "/" to be a valid ns/pod reference
		idx := strings.IndexByte(ref, '/')
		if idx < 0 || idx == 0 || idx == len(ref)-1 {
			continue
		}
		ns := ref[:idx]
		name := ref[idx+1:]
		pods = append(pods, map[string]string{
			"namespace": ns,
			"pod":       name,
			"reason":    "delete failed",
		})
	}

	return pods
}

// extractNodeFromMessage tries to extract a node name from autoscaler event messages.
// Common patterns:
//   - "Failed to drain node /gke-apps-gke-apps-gke-generic-node-5-061f26bc-f6d9, ..."
//   - "failed to drain and delete node: Failed to drain node /gke-apps-..."
func extractNodeFromMessage(message string) string {
	// Look for "node /node-name" or "node: /node-name"
	for _, prefix := range []string{"drain node /", "delete node /", "node /", "Node /"} {
		idx := strings.Index(message, prefix)
		if idx < 0 {
			continue
		}
		start := idx + len(prefix)
		rest := message[start:]
		// Node name ends at comma, space, or end of string
		end := len(rest)
		for i, ch := range rest {
			if ch == ',' || ch == ' ' || ch == '.' {
				end = i
				break
			}
		}
		name := rest[:end]
		if name != "" {
			return name
		}
	}
	return ""
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

// DeletePDBs deletes the specified PDBs that are blocking scale-down.
func (h *ScaleDownBlockersHandler) DeletePDBs(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PDBs []struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"pdbs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if len(req.PDBs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no PDBs specified"})
		return
	}
	if len(req.PDBs) > maxDeleteBatch {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("too many PDBs: max %d per request", maxDeleteBatch)})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	deleted := 0
	var errors []deleteError
	for _, ref := range req.PDBs {
		if ref.Name == "" || ref.Namespace == "" {
			errors = append(errors, deleteError{Name: ref.Name, Namespace: ref.Namespace, Error: "name and namespace required"})
			continue
		}
		if protectedNamespaces[ref.Namespace] {
			errors = append(errors, deleteError{Name: ref.Name, Namespace: ref.Namespace, Error: "protected namespace"})
			continue
		}

		pdb := &policyv1.PodDisruptionBudget{}
		key := client.ObjectKey{Namespace: ref.Namespace, Name: ref.Name}
		if err := h.client.Get(ctx, key, pdb); err != nil {
			errors = append(errors, deleteError{Name: ref.Name, Namespace: ref.Namespace, Error: fmt.Sprintf("PDB not found: %v", err)})
			continue
		}

		// Safety check: only delete PDBs with 0 disruptions allowed
		if pdb.Status.DisruptionsAllowed != 0 {
			errors = append(errors, deleteError{Name: ref.Name, Namespace: ref.Namespace, Error: fmt.Sprintf("PDB has %d disruptions allowed, not blocking", pdb.Status.DisruptionsAllowed)})
			continue
		}

		if err := h.client.Delete(ctx, pdb); err != nil {
			slog.Warn("failed to delete PDB", "name", ref.Name, "namespace", ref.Namespace, "error", err)
			errors = append(errors, deleteError{Name: ref.Name, Namespace: ref.Namespace, Error: fmt.Sprintf("delete failed: %v", err)})
		} else {
			deleted++
			slog.Info("deleted blocking PDB", "name", ref.Name, "namespace", ref.Namespace)
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"deleted": deleted,
		"errors":  errors,
	})
}

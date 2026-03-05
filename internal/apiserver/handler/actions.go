package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/koptimizer/koptimizer/internal/state"
)

// badStatusSet contains pod statuses that are considered "bad" and eligible for purge.
var badStatusSet = map[string]bool{
	"Failed":                    true,
	"Succeeded":                 true,
	"Unknown":                   true,
	"CrashLoopBackOff":          true,
	"Error":                     true,
	"OOMKilled":                 true,
	"ImagePullBackOff":          true,
	"ErrImagePull":              true,
	"ContainerStatusUnknown":    true,
	"Evicted":                   true,
	"Completed":                 true,
	"CreateContainerConfigError": true,
	// Init container variants (prefixed by computePodStatus)
	"Init:OOMKilled":                 true,
	"Init:CrashLoopBackOff":          true,
	"Init:Error":                     true,
	"Init:ImagePullBackOff":          true,
	"Init:ErrImagePull":              true,
	"Init:ContainerStatusUnknown":    true,
	"Init:CreateContainerConfigError": true,
}

type ActionsHandler struct {
	state  *state.ClusterState
	client client.Client
}

func NewActionsHandler(st *state.ClusterState, c client.Client) *ActionsHandler {
	return &ActionsHandler{state: st, client: c}
}

type badPodEntry struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Status    string `json:"status"`
	Node      string `json:"node"`
	Age       string `json:"age"`
}

func (h *ActionsHandler) ListBadPods(w http.ResponseWriter, r *http.Request) {
	allPods := h.state.GetAllPods()

	pods := []badPodEntry{}
	byNamespace := map[string]int{}
	byStatus := map[string]int{}

	now := time.Now()
	for _, ps := range allPods {
		status := computePodStatus(ps.Pod)
		if !badStatusSet[status] {
			continue
		}
		age := formatAge(now, ps.Pod.CreationTimestamp.Time)
		pods = append(pods, badPodEntry{
			Name:      ps.Name,
			Namespace: ps.Namespace,
			Status:    status,
			Node:      ps.NodeName,
			Age:       age,
		})
		byNamespace[ps.Namespace]++
		byStatus[status]++
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"pods": pods,
		"summary": map[string]interface{}{
			"byNamespace": byNamespace,
			"byStatus":    byStatus,
		},
	})
}

type deletePodRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type deletePodsRequest struct {
	Pods []deletePodRef `json:"pods"`
}

type deleteError struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Error     string `json:"error"`
}

// protectedNamespaces are namespaces where pod deletion is never allowed.
var protectedNamespaces = map[string]bool{
	"kube-system":     true,
	"kube-public":     true,
	"kube-node-lease": true,
}

const maxDeleteBatch = 1000

func (h *ActionsHandler) DeletePods(w http.ResponseWriter, r *http.Request) {
	var req deletePodsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if len(req.Pods) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no pods specified"})
		return
	}
	if len(req.Pods) > maxDeleteBatch {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("too many pods: max %d per request", maxDeleteBatch)})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	deleted := 0
	errors := []deleteError{}
	for _, ref := range req.Pods {
		if ref.Name == "" || ref.Namespace == "" {
			errors = append(errors, deleteError{
				Name:      ref.Name,
				Namespace: ref.Namespace,
				Error:     "name and namespace are required",
			})
			continue
		}
		if protectedNamespaces[ref.Namespace] {
			errors = append(errors, deleteError{
				Name:      ref.Name,
				Namespace: ref.Namespace,
				Error:     "cannot delete pods in protected namespace",
			})
			continue
		}

		// Fetch the pod first to verify it exists and is in a bad state.
		pod := &corev1.Pod{}
		key := client.ObjectKey{Namespace: ref.Namespace, Name: ref.Name}
		if err := h.client.Get(ctx, key, pod); err != nil {
			errors = append(errors, deleteError{
				Name:      ref.Name,
				Namespace: ref.Namespace,
				Error:     fmt.Sprintf("pod not found: %v", err),
			})
			continue
		}
		status := computePodStatus(pod)
		if !badStatusSet[status] {
			errors = append(errors, deleteError{
				Name:      ref.Name,
				Namespace: ref.Namespace,
				Error:     fmt.Sprintf("pod is not in a bad state (status: %s)", status),
			})
			continue
		}

		if err := h.client.Delete(ctx, pod); err != nil {
			slog.Warn("failed to delete pod", "name", ref.Name, "namespace", ref.Namespace, "error", err)
			errors = append(errors, deleteError{
				Name:      ref.Name,
				Namespace: ref.Namespace,
				Error:     fmt.Sprintf("failed to delete pod: %v", err),
			})
		} else {
			deleted++
			// Remove from in-memory state so the next ListBadPods call
			// reflects the deletion without waiting for a full Refresh cycle.
			h.state.RemovePod(ref.Namespace, ref.Name)
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"deleted": deleted,
		"errors":  errors,
	})
}

func formatAge(now, created time.Time) string {
	d := now.Sub(created)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// --- Bad ReplicaSets ---

type badRSEntry struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Reason    string `json:"reason"`
	Replicas  int32  `json:"replicas"`
	Ready     int32  `json:"ready"`
	Owner     string `json:"owner"`
	Age       string `json:"age"`
}

func (h *ActionsHandler) ListBadReplicaSets(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	var rsList appsv1.ReplicaSetList
	if err := h.client.List(ctx, &rsList); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("failed to list replicasets: %v", err)})
		return
	}

	now := time.Now()
	result := []badRSEntry{}
	byNamespace := map[string]int{}
	byReason := map[string]int{}

	for i := range rsList.Items {
		rs := &rsList.Items[i]
		desired := int32(0)
		if rs.Spec.Replicas != nil {
			desired = *rs.Spec.Replicas
		}
		ready := rs.Status.ReadyReplicas

		// Determine owner
		owner := ""
		for _, ref := range rs.OwnerReferences {
			if ref.Kind == "Deployment" {
				owner = ref.Name
				break
			}
		}

		// Classify "bad" ReplicaSets
		reason := ""
		switch {
		case owner == "":
			// Orphaned — no owning Deployment
			reason = "Orphaned"
		case desired == 0 && rs.Status.Replicas == 0:
			// Stale old revision with 0 replicas
			reason = "Stale"
		case desired > 0 && ready == 0:
			// Stuck — wants replicas but none are ready
			reason = "Stuck"
		default:
			continue
		}

		entry := badRSEntry{
			Name:      rs.Name,
			Namespace: rs.Namespace,
			Reason:    reason,
			Replicas:  desired,
			Ready:     ready,
			Owner:     owner,
			Age:       formatAge(now, rs.CreationTimestamp.Time),
		}
		result = append(result, entry)
		byNamespace[rs.Namespace]++
		byReason[reason]++
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"replicaSets": result,
		"summary": map[string]interface{}{
			"byNamespace": byNamespace,
			"byReason":    byReason,
		},
	})
}

type deleteRSRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type deleteRSRequest struct {
	ReplicaSets []deleteRSRef `json:"replicaSets"`
}

func (h *ActionsHandler) DeleteReplicaSets(w http.ResponseWriter, r *http.Request) {
	var req deleteRSRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if len(req.ReplicaSets) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no replicasets specified"})
		return
	}
	if len(req.ReplicaSets) > maxDeleteBatch {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("too many replicasets: max %d per request", maxDeleteBatch)})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	deleted := 0
	errors := []deleteError{}
	for _, ref := range req.ReplicaSets {
		if ref.Name == "" || ref.Namespace == "" {
			errors = append(errors, deleteError{
				Name:      ref.Name,
				Namespace: ref.Namespace,
				Error:     "name and namespace are required",
			})
			continue
		}
		if protectedNamespaces[ref.Namespace] {
			errors = append(errors, deleteError{
				Name:      ref.Name,
				Namespace: ref.Namespace,
				Error:     "cannot delete replicasets in protected namespace",
			})
			continue
		}

		rs := &appsv1.ReplicaSet{}
		key := client.ObjectKey{Namespace: ref.Namespace, Name: ref.Name}
		if err := h.client.Get(ctx, key, rs); err != nil {
			errors = append(errors, deleteError{
				Name:      ref.Name,
				Namespace: ref.Namespace,
				Error:     fmt.Sprintf("replicaset not found: %v", err),
			})
			continue
		}

		if err := h.client.Delete(ctx, rs); err != nil {
			slog.Warn("failed to delete replicaset", "name", ref.Name, "namespace", ref.Namespace, "error", err)
			errors = append(errors, deleteError{
				Name:      ref.Name,
				Namespace: ref.Namespace,
				Error:     fmt.Sprintf("failed to delete replicaset: %v", err),
			})
		} else {
			deleted++
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"deleted": deleted,
		"errors":  errors,
	})
}

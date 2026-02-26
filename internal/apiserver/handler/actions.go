package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

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
	"Init:OOMKilled":            true,
	"CreateContainerConfigError": true,
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

	var pods []badPodEntry
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

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	deleted := 0
	var errors []deleteError
	for _, ref := range req.Pods {
		pod := &corev1.Pod{}
		pod.Name = ref.Name
		pod.Namespace = ref.Namespace
		if err := h.client.Delete(ctx, pod); err != nil {
			errors = append(errors, deleteError{
				Name:      ref.Name,
				Namespace: ref.Namespace,
				Error:     err.Error(),
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

package handler

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

const defaultPageSize = 1000

// writeJSON is a shared helper for all handlers.
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		slog.Error("writeJSON: failed to encode response", "error", err)
	}
}

// PaginatedResponse wraps list results with pagination metadata.
type PaginatedResponse struct {
	Data       interface{} `json:"data"`
	Total      int         `json:"total"`
	Page       int         `json:"page"`
	PageSize   int         `json:"pageSize"`
	TotalPages int         `json:"totalPages"`
}

// parsePagination extracts page and pageSize from query parameters.
func parsePagination(r *http.Request) (page, pageSize int) {
	page, _ = strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ = strconv.Atoi(r.URL.Query().Get("pageSize"))
	if page < 1 {
		page = 1
	}
	if pageSize < 10 {
		pageSize = 10
	}
	if pageSize > 10000 {
		pageSize = 10000
	}
	return
}

// paginateSlice applies pagination to a generic slice via indices.
// Returns (start, end) indices for slicing and the PaginatedResponse metadata.
func paginateSlice(total, page, pageSize int) (start, end int, resp PaginatedResponse) {
	totalPages := (total + pageSize - 1) / pageSize
	if totalPages == 0 {
		totalPages = 1
	}
	resp = PaginatedResponse{
		Total:      total,
		Page:       page,
		PageSize:   pageSize,
		TotalPages: totalPages,
	}
	start = (page - 1) * pageSize
	if start > total {
		start = total
	}
	end = start + pageSize
	if end > total {
		end = total
	}
	return
}

// writePaginatedJSON writes a paginated JSON response for a slice of maps.
func writePaginatedJSON(w http.ResponseWriter, r *http.Request, items []map[string]interface{}) {
	page, pageSize := parsePagination(r)
	start, end, resp := paginateSlice(len(items), page, pageSize)
	resp.Data = items[start:end]
	writeJSON(w, http.StatusOK, resp)
}

// IsSystemPod returns true if the pod is an infrastructure/system pod rather
// than an application workload. A pod is considered "system" if its namespace
// starts with "kube-" or if it is owned by a DaemonSet.
func IsSystemPod(pod *corev1.Pod) bool {
	if strings.HasPrefix(pod.Namespace, "kube-") {
		return true
	}
	for _, ref := range pod.OwnerReferences {
		if ref.Kind == "DaemonSet" {
			return true
		}
	}
	return false
}

// computePodStatus returns a descriptive status that surfaces container-level
// issues (CrashLoopBackOff, ImagePullBackOff, OOMKilled, etc.) instead of
// just the pod phase which can misleadingly show "Running".
// Init container issues are prefixed with "Init:" to match kubectl display.
// Successfully completed init containers (exit code 0) are skipped — they
// are the normal state for pods that have finished initialization.
func computePodStatus(pod *corev1.Pod) string {
	// Check init containers — only report stuck or failed ones.
	// Completed init containers (Terminated with ExitCode 0) are normal.
	for i, cs := range pod.Status.InitContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
			return "Init:" + cs.State.Waiting.Reason
		}
		if cs.State.Terminated != nil && cs.State.Terminated.ExitCode == 0 {
			continue // successfully completed — normal
		}
		if cs.State.Terminated != nil {
			reason := cs.State.Terminated.Reason
			if reason == "" {
				reason = "Error"
			}
			return "Init:" + reason
		}
		// Init container still running — pod is initializing
		if cs.State.Running != nil {
			return fmt.Sprintf("Init:%d/%d", i, len(pod.Spec.InitContainers))
		}
	}
	// Check regular containers
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
			return cs.State.Waiting.Reason
		}
		if cs.State.Terminated != nil && cs.State.Terminated.Reason != "" {
			return cs.State.Terminated.Reason
		}
	}
	// Fall back to pod phase
	phase := string(pod.Status.Phase)
	if phase == "" {
		return "Unknown"
	}
	return phase
}

// computeContainerReady returns ready/total container counts (e.g., "1/2").
func computeContainerReady(pod *corev1.Pod) string {
	total := len(pod.Spec.Containers)
	ready := 0
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Ready {
			ready++
		}
	}
	return fmt.Sprintf("%d/%d", ready, total)
}

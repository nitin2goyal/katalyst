package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	corev1 "k8s.io/api/core/v1"
)

const defaultPageSize = 1000

// writeJSON is a shared helper for all handlers.
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
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
	if pageSize <= 0 || pageSize > 10000 {
		pageSize = defaultPageSize
	}
	if page <= 0 {
		page = 1
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

// computePodStatus returns a descriptive status that surfaces container-level
// issues (CrashLoopBackOff, ImagePullBackOff, OOMKilled, etc.) instead of
// just the pod phase which can misleadingly show "Running".
func computePodStatus(pod *corev1.Pod) string {
	// Check init containers first
	for _, cs := range pod.Status.InitContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
			return cs.State.Waiting.Reason
		}
		if cs.State.Terminated != nil && cs.State.Terminated.Reason == "OOMKilled" {
			return "Init:OOMKilled"
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

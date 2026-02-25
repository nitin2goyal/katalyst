package handler

import (
	"encoding/json"
	"net/http"

	corev1 "k8s.io/api/core/v1"
)

// writeJSON is a shared helper for all handlers.
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
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

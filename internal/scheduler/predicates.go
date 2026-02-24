package scheduler

import (
	corev1 "k8s.io/api/core/v1"
)

// PodFitsNode checks all scheduling predicates for a pod on a node.
func PodFitsNode(pod *corev1.Pod, node *corev1.Node, existingPods []*corev1.Pod) bool {
	sim := NewSimulator()
	result := sim.CanSchedule(pod, node, existingPods)
	return result.Feasible
}

// HasGPURequest checks if a pod requests GPU resources.
func HasGPURequest(pod *corev1.Pod) bool {
	gpuResource := corev1.ResourceName("nvidia.com/gpu")
	for _, c := range pod.Spec.Containers {
		if _, ok := c.Resources.Requests[gpuResource]; ok {
			return true
		}
	}
	return false
}

// IsNodeReady checks if a node is in Ready condition.
func IsNodeReady(node *corev1.Node) bool {
	for _, cond := range node.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

// IsPodRunning checks if a pod is in Running phase.
func IsPodRunning(pod *corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodRunning
}

// IsPodPending checks if a pod is in Pending phase.
func IsPodPending(pod *corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodPending
}

// GetPodCPURequest returns total CPU request for a pod in millicores.
func GetPodCPURequest(pod *corev1.Pod) int64 {
	total := int64(0)
	for _, c := range pod.Spec.Containers {
		if cpu, ok := c.Resources.Requests[corev1.ResourceCPU]; ok {
			total += cpu.MilliValue()
		}
	}
	return total
}

// GetPodMemoryRequest returns total memory request for a pod in bytes.
func GetPodMemoryRequest(pod *corev1.Pod) int64 {
	total := int64(0)
	for _, c := range pod.Spec.Containers {
		if mem, ok := c.Resources.Requests[corev1.ResourceMemory]; ok {
			total += mem.Value()
		}
	}
	return total
}

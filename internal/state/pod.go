package state

import (
	corev1 "k8s.io/api/core/v1"
)

// PodState represents the current state of a single pod.
type PodState struct {
	Pod       *corev1.Pod
	NodeName  string
	Namespace string
	Name      string
	OwnerKind string
	OwnerName string

	// Requests
	CPURequest    int64 // millicores
	MemoryRequest int64 // bytes
	CPULimit      int64
	MemoryLimit   int64
	GPURequest    int

	// Actual usage
	CPUUsage    int64
	MemoryUsage int64

	// Flags
	IsGPUWorkload bool
	HasPDB        bool
	CanEvict      bool
}

// CPUEfficiency returns the ratio of actual CPU usage to CPU request (0-1+).
func (p *PodState) CPUEfficiency() float64 {
	if p.CPURequest == 0 {
		return 0
	}
	return float64(p.CPUUsage) / float64(p.CPURequest)
}

// MemoryEfficiency returns the ratio of actual memory usage to memory request (0-1+).
func (p *PodState) MemoryEfficiency() float64 {
	if p.MemoryRequest == 0 {
		return 0
	}
	return float64(p.MemoryUsage) / float64(p.MemoryRequest)
}

// IsOverProvisioned returns true if the pod is using less than the threshold
// in at least one dimension (CPU or memory). Using OR logic catches single-dimension
// over-provisioning that AND logic would miss.
func (p *PodState) IsOverProvisioned(threshold float64) bool {
	cpuEff := p.CPUEfficiency()
	memEff := p.MemoryEfficiency()
	// Require both dimensions to have valid data (non-zero request)
	hasCPU := p.CPURequest > 0
	hasMem := p.MemoryRequest > 0
	if hasCPU && hasMem {
		return cpuEff < threshold || memEff < threshold
	}
	if hasCPU {
		return cpuEff < threshold
	}
	if hasMem {
		return memEff < threshold
	}
	return false
}

// IsUnderProvisioned returns true if the pod is using more than the threshold of its requests.
func (p *PodState) IsUnderProvisioned(threshold float64) bool {
	return p.CPUEfficiency() > threshold || p.MemoryEfficiency() > threshold
}

// NewPodState creates a PodState from a corev1.Pod.
func NewPodState(pod *corev1.Pod) *PodState {
	cpuReq, memReq := ExtractPodRequests(pod)
	cpuLim, memLim := extractPodLimits(pod)
	gpuReq := extractGPURequest(pod)

	ownerKind, ownerName := extractOwner(pod)

	return &PodState{
		Pod:           pod,
		NodeName:      pod.Spec.NodeName,
		Namespace:     pod.Namespace,
		Name:          pod.Name,
		OwnerKind:     ownerKind,
		OwnerName:     ownerName,
		CPURequest:    cpuReq,
		MemoryRequest: memReq,
		CPULimit:      cpuLim,
		MemoryLimit:   memLim,
		GPURequest:    gpuReq,
		IsGPUWorkload: gpuReq > 0,
		CanEvict:      canEvictPod(pod),
	}
}

func extractPodLimits(pod *corev1.Pod) (cpuMilli int64, memBytes int64) {
	for _, c := range pod.Spec.Containers {
		if cpu, ok := c.Resources.Limits[corev1.ResourceCPU]; ok {
			cpuMilli += cpu.MilliValue()
		}
		if mem, ok := c.Resources.Limits[corev1.ResourceMemory]; ok {
			memBytes += mem.Value()
		}
	}
	return
}

func extractGPURequest(pod *corev1.Pod) int {
	total := 0
	gpuResource := corev1.ResourceName("nvidia.com/gpu")
	for _, c := range pod.Spec.Containers {
		if gpu, ok := c.Resources.Requests[gpuResource]; ok {
			total += int(gpu.Value())
		}
	}
	return total
}

func extractOwner(pod *corev1.Pod) (kind, name string) {
	if len(pod.OwnerReferences) > 0 {
		return pod.OwnerReferences[0].Kind, pod.OwnerReferences[0].Name
	}
	return "", ""
}

func canEvictPod(pod *corev1.Pod) bool {
	// Don't evict mirror pods (static pods)
	if _, ok := pod.Annotations["kubernetes.io/config.mirror"]; ok {
		return false
	}
	// Don't evict pods with local storage (unless they opted in)
	for _, vol := range pod.Spec.Volumes {
		if vol.EmptyDir != nil || vol.HostPath != nil {
			if pod.Annotations["koptimizer.io/safe-to-evict"] != "true" {
				return false
			}
		}
	}
	return true
}

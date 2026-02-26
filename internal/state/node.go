package state

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// NodeState represents the current state of a single node.
type NodeState struct {
	Node           *corev1.Node
	Pods           []*corev1.Pod
	InstanceType   string
	InstanceFamily string
	NodeGroupID    string
	NodeGroupName  string

	// Capacity
	CPUCapacity    int64 // millicores
	MemoryCapacity int64 // bytes
	GPUCapacity    int

	// Requests (sum of pod requests)
	CPURequested    int64
	MemoryRequested int64

	// Actual usage (from metrics)
	CPUUsed    int64
	MemoryUsed int64
	GPUsUsed   int

	// Cost
	HourlyCostUSD float64
	IsSpot        bool
	IsGPUNode     bool
}

// CPUUtilization returns CPU utilization as a percentage of capacity.
func (n *NodeState) CPUUtilization() float64 {
	if n.CPUCapacity == 0 {
		return 0
	}
	return float64(n.CPUUsed) / float64(n.CPUCapacity) * 100
}

// MemoryUtilization returns memory utilization as a percentage of capacity.
func (n *NodeState) MemoryUtilization() float64 {
	if n.MemoryCapacity == 0 {
		return 0
	}
	return float64(n.MemoryUsed) / float64(n.MemoryCapacity) * 100
}

// CPURequestUtilization returns CPU requests as a percentage of capacity.
func (n *NodeState) CPURequestUtilization() float64 {
	if n.CPUCapacity == 0 {
		return 0
	}
	return float64(n.CPURequested) / float64(n.CPUCapacity) * 100
}

// MemoryRequestUtilization returns memory requests as a percentage of capacity.
func (n *NodeState) MemoryRequestUtilization() float64 {
	if n.MemoryCapacity == 0 {
		return 0
	}
	return float64(n.MemoryRequested) / float64(n.MemoryCapacity) * 100
}

// AvailableCPU returns unrequested CPU in millicores.
func (n *NodeState) AvailableCPU() int64 {
	avail := n.CPUCapacity - n.CPURequested
	if avail < 0 {
		return 0
	}
	return avail
}

// AvailableMemory returns unrequested memory in bytes.
func (n *NodeState) AvailableMemory() int64 {
	avail := n.MemoryCapacity - n.MemoryRequested
	if avail < 0 {
		return 0
	}
	return avail
}

// IsEmpty returns true if no pods are scheduled on this node (excluding daemonsets).
func (n *NodeState) IsEmpty() bool {
	for _, pod := range n.Pods {
		if !isDaemonSetPod(pod) {
			return false
		}
	}
	return true
}

// IsUnderutilized returns true if both CPU and memory utilization are below the threshold.
func (n *NodeState) IsUnderutilized(threshold float64) bool {
	return n.CPUUtilization() < threshold && n.MemoryUtilization() < threshold
}

func isDaemonSetPod(pod *corev1.Pod) bool {
	for _, ref := range pod.OwnerReferences {
		if ref.Kind == "DaemonSet" {
			return true
		}
	}
	return false
}

// ExtractNodeCapacity extracts CPU and memory allocatable resources from a node.
// Uses Allocatable (capacity minus system reservations) for CPU and memory
// to avoid 5-15% inflation in utilization calculations. Falls back to
// Capacity for GPUs since Allocatable may not include extended resources.
func ExtractNodeCapacity(node *corev1.Node) (cpuMilli int64, memBytes int64, gpus int) {
	if cpu, ok := node.Status.Allocatable[corev1.ResourceCPU]; ok {
		cpuMilli = cpu.MilliValue()
	} else if cpu, ok := node.Status.Capacity[corev1.ResourceCPU]; ok {
		cpuMilli = cpu.MilliValue()
	}
	if mem, ok := node.Status.Allocatable[corev1.ResourceMemory]; ok {
		memBytes = mem.Value()
	} else if mem, ok := node.Status.Capacity[corev1.ResourceMemory]; ok {
		memBytes = mem.Value()
	}
	// GPUs: prefer Allocatable but fall back to Capacity since extended
	// resources may only appear in Capacity.
	gpuRes := corev1.ResourceName("nvidia.com/gpu")
	if gpu, ok := node.Status.Allocatable[gpuRes]; ok {
		gpus = int(gpu.Value())
	} else if gpu, ok := node.Status.Capacity[gpuRes]; ok {
		gpus = int(gpu.Value())
	}
	return
}

// ExtractPodRequests calculates total CPU and memory requests for a pod.
// Accounts for init containers: effective request = max(max(initContainers), sum(containers))
// per the Kubernetes resource model.
func ExtractPodRequests(pod *corev1.Pod) (cpuMilli int64, memBytes int64) {
	for _, c := range pod.Spec.Containers {
		if cpu, ok := c.Resources.Requests[corev1.ResourceCPU]; ok {
			cpuMilli += cpu.MilliValue()
		}
		if mem, ok := c.Resources.Requests[corev1.ResourceMemory]; ok {
			memBytes += mem.Value()
		}
	}
	// Init containers run sequentially before regular containers. The effective
	// pod resource requirement is max(max(initContainers), sum(containers)).
	for _, ic := range pod.Spec.InitContainers {
		var icCPU, icMem int64
		if cpu, ok := ic.Resources.Requests[corev1.ResourceCPU]; ok {
			icCPU = cpu.MilliValue()
		}
		if mem, ok := ic.Resources.Requests[corev1.ResourceMemory]; ok {
			icMem = mem.Value()
		}
		if icCPU > cpuMilli {
			cpuMilli = icCPU
		}
		if icMem > memBytes {
			memBytes = icMem
		}
	}
	return
}

// QuantityToMilliCPU converts a resource.Quantity to millicores.
func QuantityToMilliCPU(q resource.Quantity) int64 {
	return q.MilliValue()
}

// QuantityToBytes converts a resource.Quantity to bytes.
func QuantityToBytes(q resource.Quantity) int64 {
	return q.Value()
}

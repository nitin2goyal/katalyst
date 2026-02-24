package nodeautoscaler

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// helper to create a pod with given CPU (millicores) and memory (bytes) requests.
func makePod(name, namespace string, cpuMillis int64, memBytes int64) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "main",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    *resource.NewMilliQuantity(cpuMillis, resource.DecimalSI),
							corev1.ResourceMemory: *resource.NewQuantity(memBytes, resource.BinarySI),
						},
					},
				},
			},
		},
	}
}

// helper to create a DaemonSet-owned pod.
func makeDaemonSetPod(name, namespace string, cpuMillis int64, memBytes int64) *corev1.Pod {
	pod := makePod(name, namespace, cpuMillis, memBytes)
	pod.OwnerReferences = []metav1.OwnerReference{
		{
			Kind: "DaemonSet",
			Name: "kube-proxy",
		},
	}
	return pod
}

// helper to create a node with the given capacity and attached pods.
func makeNode(name string, cpuCapacityMillis, memCapacityBytes int64, pods []*corev1.Pod) optimizer.NodeInfo {
	var cpuReq, memReq int64
	for _, p := range pods {
		for _, c := range p.Spec.Containers {
			if cpu, ok := c.Resources.Requests[corev1.ResourceCPU]; ok {
				cpuReq += cpu.MilliValue()
			}
			if mem, ok := c.Resources.Requests[corev1.ResourceMemory]; ok {
				memReq += mem.Value()
			}
		}
	}

	return optimizer.NodeInfo{
		Node: &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: name},
		},
		Pods:            pods,
		CPUCapacity:     cpuCapacityMillis,
		MemoryCapacity:  memCapacityBytes,
		CPURequested:    cpuReq,
		MemoryRequested: memReq,
	}
}

const (
	gib = 1024 * 1024 * 1024 // 1 GiB in bytes
)

// ---------------------------------------------------------------------------
// BinPacker.Pack tests
// ---------------------------------------------------------------------------

func TestBinPackerPack_EmptyCluster(t *testing.T) {
	bp := NewBinPacker()
	snapshot := &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{},
	}

	result := bp.Pack(snapshot)

	if result.MinNodesNeeded != 0 {
		t.Errorf("MinNodesNeeded = %d, want 0", result.MinNodesNeeded)
	}
	if result.CurrentNodes != 0 {
		t.Errorf("CurrentNodes = %d, want 0", result.CurrentNodes)
	}
	if result.CanConsolidate {
		t.Error("CanConsolidate = true, want false for empty cluster")
	}
	if result.NodesSaved != 0 {
		t.Errorf("NodesSaved = %d, want 0", result.NodesSaved)
	}
}

func TestBinPackerPack_FullyPacked(t *testing.T) {
	// 3 nodes each with 4000m CPU and 16 GiB memory.
	// After 10% system reserve: 3600m CPU, ~14.4 GiB available per node.
	// Place pods that use ~90% of each node's available capacity so
	// they cannot be consolidated into fewer nodes.
	bp := NewBinPacker()

	cpuCap := int64(4000)
	memCap := int64(16 * gib)

	// Each pod requests 3500m CPU (close to 3600m available after overhead).
	// 3 such pods need 3 bins; no consolidation possible.
	snapshot := &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{
			makeNode("node-1", cpuCap, memCap, []*corev1.Pod{
				makePod("heavy-1", "default", 3500, 1*gib),
			}),
			makeNode("node-2", cpuCap, memCap, []*corev1.Pod{
				makePod("heavy-2", "default", 3500, 1*gib),
			}),
			makeNode("node-3", cpuCap, memCap, []*corev1.Pod{
				makePod("heavy-3", "default", 3500, 1*gib),
			}),
		},
	}

	result := bp.Pack(snapshot)

	if result.CurrentNodes != 3 {
		t.Errorf("CurrentNodes = %d, want 3", result.CurrentNodes)
	}
	if result.MinNodesNeeded != 3 {
		t.Errorf("MinNodesNeeded = %d, want 3", result.MinNodesNeeded)
	}
	if result.CanConsolidate {
		t.Error("CanConsolidate = true, want false for fully packed cluster")
	}
	if result.NodesSaved != 0 {
		t.Errorf("NodesSaved = %d, want 0", result.NodesSaved)
	}
}

func TestBinPackerPack_CanConsolidate(t *testing.T) {
	// 3 nodes each with 4000m CPU and 16 GiB memory.
	// After 10% reserve: 3600m CPU, ~14.4 GiB per node.
	// Each node has a small pod (500m CPU, 1 GiB).
	// Total workload: 1500m CPU and 3 GiB -> fits in 1 node.
	bp := NewBinPacker()

	cpuCap := int64(4000)
	memCap := int64(16 * gib)

	snapshot := &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{
			makeNode("node-1", cpuCap, memCap, []*corev1.Pod{
				makePod("small-1", "default", 500, 1*gib),
			}),
			makeNode("node-2", cpuCap, memCap, []*corev1.Pod{
				makePod("small-2", "default", 500, 1*gib),
			}),
			makeNode("node-3", cpuCap, memCap, []*corev1.Pod{
				makePod("small-3", "default", 500, 1*gib),
			}),
		},
	}

	result := bp.Pack(snapshot)

	if result.CurrentNodes != 3 {
		t.Errorf("CurrentNodes = %d, want 3", result.CurrentNodes)
	}
	if !result.CanConsolidate {
		t.Error("CanConsolidate = false, want true")
	}
	if result.MinNodesNeeded >= 3 {
		t.Errorf("MinNodesNeeded = %d, want < 3", result.MinNodesNeeded)
	}
	if result.MinNodesNeeded != 1 {
		t.Errorf("MinNodesNeeded = %d, want 1", result.MinNodesNeeded)
	}
	if result.NodesSaved != 2 {
		t.Errorf("NodesSaved = %d, want 2", result.NodesSaved)
	}
}

func TestBinPackerPack_SingleLargePod(t *testing.T) {
	// One pod that consumes almost the entire node.
	// After 10% reserve on a 4000m node: 3600m available.
	// A pod requesting 3500m should need exactly 1 node.
	bp := NewBinPacker()

	cpuCap := int64(4000)
	memCap := int64(16 * gib)

	snapshot := &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{
			makeNode("node-1", cpuCap, memCap, []*corev1.Pod{
				makePod("big-pod", "default", 3500, 12*gib),
			}),
		},
	}

	result := bp.Pack(snapshot)

	if result.CurrentNodes != 1 {
		t.Errorf("CurrentNodes = %d, want 1", result.CurrentNodes)
	}
	if result.MinNodesNeeded != 1 {
		t.Errorf("MinNodesNeeded = %d, want 1", result.MinNodesNeeded)
	}
	if result.CanConsolidate {
		t.Error("CanConsolidate = true, want false for single node")
	}
	if result.NodesSaved != 0 {
		t.Errorf("NodesSaved = %d, want 0", result.NodesSaved)
	}
}

func TestBinPackerPack_DaemonSetPodsExcluded(t *testing.T) {
	// 2 nodes, each with a DaemonSet pod (100m CPU, 128 Mi) and a small workload pod.
	// The DaemonSet pods should be excluded from packing, so only the workload pods
	// are considered. Both workload pods are small (200m) and should fit in 1 node.
	bp := NewBinPacker()

	cpuCap := int64(4000)
	memCap := int64(16 * gib)

	snapshot := &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{
			makeNode("node-1", cpuCap, memCap, []*corev1.Pod{
				makeDaemonSetPod("ds-pod-1", "kube-system", 100, 128*1024*1024),
				makePod("app-1", "default", 200, 256*1024*1024),
			}),
			makeNode("node-2", cpuCap, memCap, []*corev1.Pod{
				makeDaemonSetPod("ds-pod-2", "kube-system", 100, 128*1024*1024),
				makePod("app-2", "default", 200, 256*1024*1024),
			}),
		},
	}

	result := bp.Pack(snapshot)

	if result.CurrentNodes != 2 {
		t.Errorf("CurrentNodes = %d, want 2", result.CurrentNodes)
	}
	// Only 2 workload pods (200m each = 400m total), should fit in 1 node (3600m available).
	if result.MinNodesNeeded != 1 {
		t.Errorf("MinNodesNeeded = %d, want 1 (DaemonSet pods should be excluded)", result.MinNodesNeeded)
	}
	if !result.CanConsolidate {
		t.Error("CanConsolidate = false, want true")
	}
	if result.NodesSaved != 1 {
		t.Errorf("NodesSaved = %d, want 1", result.NodesSaved)
	}

	// Verify that DaemonSet pod keys are NOT in assignments.
	for key := range result.Assignments {
		if key == "kube-system/ds-pod-1" || key == "kube-system/ds-pod-2" {
			t.Errorf("DaemonSet pod %q should not appear in assignments", key)
		}
	}
	// Verify workload pods ARE in assignments.
	if _, ok := result.Assignments["default/app-1"]; !ok {
		t.Error("workload pod default/app-1 missing from assignments")
	}
	if _, ok := result.Assignments["default/app-2"]; !ok {
		t.Error("workload pod default/app-2 missing from assignments")
	}
}

func TestBinPackerPack_MultiplePodsPerNode(t *testing.T) {
	// 2 nodes with multiple pods each. Total workload should be packable into
	// fewer nodes.
	// Node capacity: 8000m CPU, 32 GiB. After reserve: 7200m, ~28.8 GiB.
	// Node 1: 3 pods = 500+800+400 = 1700m
	// Node 2: 2 pods = 600+300 = 900m
	// Total: 2600m -> fits in 1 node.
	bp := NewBinPacker()

	cpuCap := int64(8000)
	memCap := int64(32 * gib)

	snapshot := &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{
			makeNode("node-1", cpuCap, memCap, []*corev1.Pod{
				makePod("pod-a", "default", 500, 1*gib),
				makePod("pod-b", "default", 800, 2*gib),
				makePod("pod-c", "default", 400, 1*gib),
			}),
			makeNode("node-2", cpuCap, memCap, []*corev1.Pod{
				makePod("pod-d", "default", 600, 1*gib),
				makePod("pod-e", "default", 300, 512*1024*1024),
			}),
		},
	}

	result := bp.Pack(snapshot)

	if result.CurrentNodes != 2 {
		t.Errorf("CurrentNodes = %d, want 2", result.CurrentNodes)
	}
	if result.MinNodesNeeded != 1 {
		t.Errorf("MinNodesNeeded = %d, want 1", result.MinNodesNeeded)
	}
	if !result.CanConsolidate {
		t.Error("CanConsolidate = false, want true")
	}
	if result.NodesSaved != 1 {
		t.Errorf("NodesSaved = %d, want 1", result.NodesSaved)
	}

	// All 5 pods should be assigned.
	if len(result.Assignments) != 5 {
		t.Errorf("len(Assignments) = %d, want 5", len(result.Assignments))
	}
}

func TestBinPackerPack_MemoryBottleneck(t *testing.T) {
	// Even though CPU fits, memory may force more nodes.
	// Node: 8000m CPU, 8 GiB memory. After reserve: 7200m, ~7.2 GiB.
	// 3 pods each requesting 500m CPU but 4 GiB memory.
	// CPU total: 1500m (fits in 1 node).
	// Memory total: 12 GiB (needs at least 2 nodes at 7.2 GiB each).
	bp := NewBinPacker()

	cpuCap := int64(8000)
	memCap := int64(8 * gib)

	snapshot := &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{
			makeNode("node-1", cpuCap, memCap, []*corev1.Pod{
				makePod("mem-pod-1", "default", 500, 4*gib),
			}),
			makeNode("node-2", cpuCap, memCap, []*corev1.Pod{
				makePod("mem-pod-2", "default", 500, 4*gib),
			}),
			makeNode("node-3", cpuCap, memCap, []*corev1.Pod{
				makePod("mem-pod-3", "default", 500, 4*gib),
			}),
		},
	}

	result := bp.Pack(snapshot)

	if result.CurrentNodes != 3 {
		t.Errorf("CurrentNodes = %d, want 3", result.CurrentNodes)
	}
	// Each node has 7.2 GiB available. Each pod needs 4 GiB.
	// Node 0: mem-pod-1 (4 GiB) -> 3.2 GiB free; cannot fit another 4 GiB pod.
	// Node 1: mem-pod-2 (4 GiB) -> 3.2 GiB free.
	// Node 2: mem-pod-3 (4 GiB).
	// Needs 3 nodes due to memory, even though CPU is fine.
	if result.MinNodesNeeded < 2 {
		t.Errorf("MinNodesNeeded = %d, want >= 2 (memory bottleneck)", result.MinNodesNeeded)
	}
}

func TestBinPackerPack_FFDOrdering(t *testing.T) {
	// Verify that first-fit-decreasing (sorted by CPU desc) produces an optimal-ish result.
	// Node: 4000m CPU, 16 GiB. After reserve: 3600m, ~14.4 GiB.
	// Pods: 2000m, 1500m, 1000m, 500m = 5000m total.
	// FFD: 2000 -> bin0 (1600 free), 1500 -> bin1 (2100 free), 1000 -> bin1 (1100 free),
	//       500 -> bin0 (1100 free).
	// Result: 2 nodes needed.
	bp := NewBinPacker()

	cpuCap := int64(4000)
	memCap := int64(16 * gib)

	snapshot := &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{
			makeNode("node-1", cpuCap, memCap, []*corev1.Pod{
				makePod("pod-small", "default", 500, 1*gib),
				makePod("pod-large", "default", 2000, 2*gib),
			}),
			makeNode("node-2", cpuCap, memCap, []*corev1.Pod{
				makePod("pod-medium", "default", 1000, 1*gib),
				makePod("pod-medlarge", "default", 1500, 2*gib),
			}),
			makeNode("node-3", cpuCap, memCap, []*corev1.Pod{}),
		},
	}

	result := bp.Pack(snapshot)

	if result.CurrentNodes != 3 {
		t.Errorf("CurrentNodes = %d, want 3", result.CurrentNodes)
	}
	if result.MinNodesNeeded != 2 {
		t.Errorf("MinNodesNeeded = %d, want 2", result.MinNodesNeeded)
	}
	if !result.CanConsolidate {
		t.Error("CanConsolidate = false, want true (empty node-3 can be removed)")
	}
	if result.NodesSaved != 1 {
		t.Errorf("NodesSaved = %d, want 1", result.NodesSaved)
	}
}

func TestBinPackerPack_AllDaemonSetPods(t *testing.T) {
	// If all pods are DaemonSet pods, there is nothing to pack.
	// MinNodesNeeded should be 0 (no workload bins needed).
	bp := NewBinPacker()

	cpuCap := int64(4000)
	memCap := int64(16 * gib)

	snapshot := &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{
			makeNode("node-1", cpuCap, memCap, []*corev1.Pod{
				makeDaemonSetPod("ds-1", "kube-system", 100, 128*1024*1024),
			}),
			makeNode("node-2", cpuCap, memCap, []*corev1.Pod{
				makeDaemonSetPod("ds-2", "kube-system", 100, 128*1024*1024),
			}),
		},
	}

	result := bp.Pack(snapshot)

	if result.CurrentNodes != 2 {
		t.Errorf("CurrentNodes = %d, want 2", result.CurrentNodes)
	}
	// No workload pods means 0 bins needed.
	if result.MinNodesNeeded != 0 {
		t.Errorf("MinNodesNeeded = %d, want 0 (all pods are DaemonSet)", result.MinNodesNeeded)
	}
	if !result.CanConsolidate {
		t.Error("CanConsolidate = false, want true (0 < 2)")
	}
	if len(result.Assignments) != 0 {
		t.Errorf("len(Assignments) = %d, want 0 (no workload pods)", len(result.Assignments))
	}
}

func TestBinPackerPack_NilSnapshot(t *testing.T) {
	// Edge case: nil snapshot should not panic.
	// The code checks len(snapshot.Nodes)==0 first, but let's verify
	// it handles a snapshot with nil node slice gracefully.
	bp := NewBinPacker()
	snapshot := &optimizer.ClusterSnapshot{}

	result := bp.Pack(snapshot)

	if result.MinNodesNeeded != 0 {
		t.Errorf("MinNodesNeeded = %d, want 0", result.MinNodesNeeded)
	}
}

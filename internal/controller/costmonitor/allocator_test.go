package costmonitor

import (
	"context"
	"math"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func nodeWithPods(name string, cpuCapMilli int64, memCapBytes int64, hourlyCost float64, pods []*corev1.Pod) optimizer.NodeInfo {
	return optimizer.NodeInfo{
		Node: &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: name},
		},
		Pods:           pods,
		CPUCapacity:    cpuCapMilli,
		MemoryCapacity: memCapBytes,
		HourlyCostUSD:  hourlyCost,
	}
}

func pod(name, ns string, cpuMillis int64, memBytes int64, phase corev1.PodPhase, ownerKind, ownerName string) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
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
		Status: corev1.PodStatus{Phase: phase},
	}
	if ownerKind != "" {
		p.OwnerReferences = []metav1.OwnerReference{
			{Kind: ownerKind, Name: ownerName},
		}
	}
	return p
}

const (
	gi = int64(1024 * 1024 * 1024)
	mi = int64(1024 * 1024)
)

// ---------------------------------------------------------------------------
// allocPodWeight Tests
// ---------------------------------------------------------------------------

func TestAllocPodWeight_Balanced(t *testing.T) {
	containers := []corev1.Container{
		{
			Name: "app",
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    *resource.NewMilliQuantity(1000, resource.DecimalSI),
					corev1.ResourceMemory: *resource.NewQuantity(4*gi, resource.BinarySI),
				},
			},
		},
	}
	// Node: 4000m CPU, 16Gi mem
	// Pod weight: 0.5*(1000/4000) + 0.5*(4Gi/16Gi) = 0.5*0.25 + 0.5*0.25 = 0.25
	w := allocPodWeight(containers, 4000, 16*gi)
	expected := 0.25
	if math.Abs(w-expected) > 0.001 {
		t.Errorf("weight = %.4f, want %.4f", w, expected)
	}
}

func TestAllocPodWeight_CPUHeavy(t *testing.T) {
	containers := []corev1.Container{
		{
			Name: "compute",
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    *resource.NewMilliQuantity(4000, resource.DecimalSI),
					corev1.ResourceMemory: *resource.NewQuantity(gi, resource.BinarySI),
				},
			},
		},
	}
	// 0.5*(4000/4000) + 0.5*(1Gi/16Gi) = 0.5 + 0.03125 = 0.53125
	w := allocPodWeight(containers, 4000, 16*gi)
	if w < 0.5 {
		t.Errorf("CPU-heavy pod should have weight >= 0.5, got %.4f", w)
	}
}

func TestAllocPodWeight_NoRequests(t *testing.T) {
	containers := []corev1.Container{
		{Name: "app", Resources: corev1.ResourceRequirements{}},
	}
	w := allocPodWeight(containers, 4000, 16*gi)
	if w != 0 {
		t.Errorf("pod with no requests should have weight 0, got %.4f", w)
	}
}

func TestAllocPodWeight_ZeroCapacity(t *testing.T) {
	containers := []corev1.Container{
		{
			Name: "app",
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU: *resource.NewMilliQuantity(1000, resource.DecimalSI),
				},
			},
		},
	}
	// Zero CPU capacity, nonzero memory capacity
	w := allocPodWeight(containers, 0, 16*gi)
	// Only memory component should contribute
	if w > 0.5 {
		t.Errorf("with zero CPU cap, weight should be <= 0.5, got %.4f", w)
	}
}

func TestAllocPodWeight_MultiContainer(t *testing.T) {
	containers := []corev1.Container{
		{
			Name: "app",
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    *resource.NewMilliQuantity(500, resource.DecimalSI),
					corev1.ResourceMemory: *resource.NewQuantity(2*gi, resource.BinarySI),
				},
			},
		},
		{
			Name: "sidecar",
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    *resource.NewMilliQuantity(100, resource.DecimalSI),
					corev1.ResourceMemory: *resource.NewQuantity(256*mi, resource.BinarySI),
				},
			},
		},
	}
	// Total: 600m CPU, ~2.25Gi mem
	w := allocPodWeight(containers, 4000, 16*gi)
	if w <= 0 {
		t.Error("multi-container pod should have positive weight")
	}
}

// ---------------------------------------------------------------------------
// AllocateByNamespace Tests
// ---------------------------------------------------------------------------

func TestAllocateByNamespace_SinglePodSingleNode(t *testing.T) {
	a := NewAllocator(nil)
	snapshot := &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{
			nodeWithPods("node1", 4000, 16*gi, 0.50, []*corev1.Pod{
				pod("web-1", "prod", 2000, 8*gi, corev1.PodRunning, "Deployment", "web"),
			}),
		},
	}

	costs, err := a.AllocateByNamespace(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := costs["prod"]; !ok {
		t.Fatal("expected cost entry for 'prod' namespace")
	}
	if costs["prod"] <= 0 {
		t.Error("prod cost should be > 0")
	}
}

func TestAllocateByNamespace_MultipleNamespaces(t *testing.T) {
	a := NewAllocator(nil)

	snapshot := &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{
			nodeWithPods("node1", 4000, 16*gi, 0.50, []*corev1.Pod{
				pod("web-1", "prod", 1000, 4*gi, corev1.PodRunning, "Deployment", "web"),
				pod("api-1", "staging", 1000, 4*gi, corev1.PodRunning, "Deployment", "api"),
			}),
		},
	}

	costs, err := a.AllocateByNamespace(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(costs) != 2 {
		t.Fatalf("expected 2 namespaces, got %d", len(costs))
	}
	// Equal resource requests → equal cost split
	diff := math.Abs(costs["prod"] - costs["staging"])
	if diff > 0.01 {
		t.Errorf("equal pods should have equal cost: prod=%.2f, staging=%.2f", costs["prod"], costs["staging"])
	}
}

func TestAllocateByNamespace_NonRunningPodsSkipped(t *testing.T) {
	a := NewAllocator(nil)

	snapshot := &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{
			nodeWithPods("node1", 4000, 16*gi, 0.50, []*corev1.Pod{
				pod("web-1", "prod", 1000, 4*gi, corev1.PodRunning, "Deployment", "web"),
				pod("failed-1", "prod", 1000, 4*gi, corev1.PodFailed, "Job", "failed"),
			}),
		},
	}

	costs, err := a.AllocateByNamespace(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Only running pod should get cost allocation
	if len(costs) != 1 {
		t.Fatalf("expected 1 namespace (non-running filtered), got %d", len(costs))
	}
}

func TestAllocateByNamespace_ZeroCostNodeSkipped(t *testing.T) {
	a := NewAllocator(nil)

	snapshot := &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{
			nodeWithPods("free-node", 4000, 16*gi, 0, []*corev1.Pod{
				pod("web-1", "prod", 1000, 4*gi, corev1.PodRunning, "Deployment", "web"),
			}),
		},
	}

	costs, err := a.AllocateByNamespace(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(costs) != 0 {
		t.Errorf("expected 0 costs for zero-cost node, got %d", len(costs))
	}
}

func TestAllocateByNamespace_ZeroCapacityNodeSkipped(t *testing.T) {
	a := NewAllocator(nil)

	snapshot := &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{
			nodeWithPods("broken-node", 0, 0, 0.50, []*corev1.Pod{
				pod("web-1", "prod", 1000, 4*gi, corev1.PodRunning, "Deployment", "web"),
			}),
		},
	}

	costs, err := a.AllocateByNamespace(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(costs) != 0 {
		t.Errorf("expected 0 costs for zero-capacity node, got %d", len(costs))
	}
}

func TestAllocateByNamespace_EmptyCluster(t *testing.T) {
	a := NewAllocator(nil)
	costs, err := a.AllocateByNamespace(context.Background(), &optimizer.ClusterSnapshot{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(costs) != 0 {
		t.Errorf("expected 0 costs for empty cluster, got %d", len(costs))
	}
}

// ---------------------------------------------------------------------------
// AllocateByNodeGroup Tests
// ---------------------------------------------------------------------------

func TestAllocateByNodeGroup_Basic(t *testing.T) {
	a := NewAllocator(nil)

	snapshot := &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{
			{Node: &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}}, NodeGroup: "workers", HourlyCostUSD: 0.50},
			{Node: &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n2"}}, NodeGroup: "workers", HourlyCostUSD: 0.50},
			{Node: &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n3"}}, NodeGroup: "gpu-pool", HourlyCostUSD: 2.50},
		},
	}

	costs, err := a.AllocateByNodeGroup(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(costs) != 2 {
		t.Fatalf("expected 2 node groups, got %d", len(costs))
	}
	// workers: 2 * 0.50 * 730.5 = 730.5
	if math.Abs(costs["workers"]-730.5) > 0.1 {
		t.Errorf("workers cost = %.2f, want ~730.50", costs["workers"])
	}
}

func TestAllocateByNodeGroup_NoGroupSkipped(t *testing.T) {
	a := NewAllocator(nil)

	snapshot := &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{
			{Node: &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}}, NodeGroup: "", HourlyCostUSD: 0.50},
		},
	}

	costs, err := a.AllocateByNodeGroup(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(costs) != 0 {
		t.Errorf("expected 0 costs for nodes with no group, got %d", len(costs))
	}
}

// ---------------------------------------------------------------------------
// TopWorkloads Tests
// ---------------------------------------------------------------------------

func TestTopWorkloads_SortedByCost(t *testing.T) {
	a := NewAllocator(nil)

	snapshot := &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{
			nodeWithPods("node1", 8000, 32*gi, 1.0, []*corev1.Pod{
				pod("big-1", "prod", 4000, 16*gi, corev1.PodRunning, "Deployment", "big-app"),
				pod("small-1", "prod", 1000, 4*gi, corev1.PodRunning, "Deployment", "small-app"),
			}),
		},
	}

	result, err := a.TopWorkloads(context.Background(), snapshot, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) < 2 {
		t.Fatalf("expected >= 2 workloads, got %d", len(result))
	}
	// First should be the most expensive
	if result[0].MonthlyCostUSD <= result[1].MonthlyCostUSD {
		t.Errorf("workloads not sorted by cost: %.2f <= %.2f", result[0].MonthlyCostUSD, result[1].MonthlyCostUSD)
	}
}

func TestTopWorkloads_LimitRespected(t *testing.T) {
	a := NewAllocator(nil)

	pods := make([]*corev1.Pod, 5)
	for i := 0; i < 5; i++ {
		pods[i] = pod("p"+string(rune('a'+i)), "prod", 500, 2*gi, corev1.PodRunning, "Deployment", "app-"+string(rune('a'+i)))
	}

	snapshot := &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{
			nodeWithPods("node1", 8000, 32*gi, 1.0, pods),
		},
	}

	result, err := a.TopWorkloads(context.Background(), snapshot, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 3 {
		t.Errorf("expected 3 workloads (limit), got %d", len(result))
	}
}

func TestTopWorkloads_OrphanPodHandled(t *testing.T) {
	a := NewAllocator(nil)

	snapshot := &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{
			nodeWithPods("node1", 4000, 16*gi, 0.50, []*corev1.Pod{
				pod("orphan-1", "prod", 1000, 4*gi, corev1.PodRunning, "", ""),
			}),
		},
	}

	result, err := a.TopWorkloads(context.Background(), snapshot, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 workload, got %d", len(result))
	}
	if result[0].Kind != "Pod" {
		t.Errorf("orphan pod kind = %q, want Pod", result[0].Kind)
	}
}

func TestTopWorkloads_ReplicaCounting(t *testing.T) {
	a := NewAllocator(nil)

	snapshot := &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{
			nodeWithPods("node1", 8000, 32*gi, 1.0, []*corev1.Pod{
				pod("web-a", "prod", 1000, 4*gi, corev1.PodRunning, "ReplicaSet", "web-abc"),
				pod("web-b", "prod", 1000, 4*gi, corev1.PodRunning, "ReplicaSet", "web-abc"),
				pod("web-c", "prod", 1000, 4*gi, corev1.PodRunning, "ReplicaSet", "web-abc"),
			}),
		},
	}

	result, err := a.TopWorkloads(context.Background(), snapshot, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 workload (aggregated), got %d", len(result))
	}
	if result[0].Replicas != 3 {
		t.Errorf("replicas = %d, want 3", result[0].Replicas)
	}
}

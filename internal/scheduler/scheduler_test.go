package scheduler

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func readyNode(name string, cpuMillis int64, memBytes int64, labels map[string]string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
			},
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    *resource.NewMilliQuantity(cpuMillis, resource.DecimalSI),
				corev1.ResourceMemory: *resource.NewQuantity(memBytes, resource.BinarySI),
			},
		},
	}
}

func simplePod(name string, cpuMillis int64, memBytes int64) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
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

const gi = int64(1024 * 1024 * 1024)

// ---------------------------------------------------------------------------
// PodFitsNode Tests
// ---------------------------------------------------------------------------

func TestPodFitsNode_Basic(t *testing.T) {
	node := readyNode("n1", 4000, 16*gi, nil)
	pod := simplePod("p1", 1000, 4*gi)
	if !PodFitsNode(pod, node, nil) {
		t.Error("pod should fit on node with sufficient resources")
	}
}

func TestPodFitsNode_InsufficientCPU(t *testing.T) {
	node := readyNode("n1", 1000, 16*gi, nil)
	pod := simplePod("p1", 2000, 4*gi)
	if PodFitsNode(pod, node, nil) {
		t.Error("pod should not fit: insufficient CPU")
	}
}

func TestPodFitsNode_InsufficientMemory(t *testing.T) {
	node := readyNode("n1", 4000, 2*gi, nil)
	pod := simplePod("p1", 1000, 4*gi)
	if PodFitsNode(pod, node, nil) {
		t.Error("pod should not fit: insufficient memory")
	}
}

func TestPodFitsNode_ExistingPodsConsumption(t *testing.T) {
	node := readyNode("n1", 4000, 16*gi, nil)
	pod := simplePod("p-new", 2000, 8*gi)
	existing := []*corev1.Pod{
		simplePod("p-existing", 3000, 10*gi),
	}
	if PodFitsNode(pod, node, existing) {
		t.Error("pod should not fit: existing pods consume too much")
	}
}

// ---------------------------------------------------------------------------
// Node Readiness Tests
// ---------------------------------------------------------------------------

func TestCanSchedule_NodeNotReady(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "n1"},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionFalse},
			},
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    *resource.NewMilliQuantity(4000, resource.DecimalSI),
				corev1.ResourceMemory: *resource.NewQuantity(16*gi, resource.BinarySI),
			},
		},
	}
	pod := simplePod("p1", 100, gi)
	sim := NewSimulator()
	result := sim.CanSchedule(pod, node, nil)
	if result.Feasible {
		t.Error("should not schedule on not-ready node")
	}
	if result.Reason != "node not ready" {
		t.Errorf("reason = %q, want 'node not ready'", result.Reason)
	}
}

func TestCanSchedule_NodeCordoned(t *testing.T) {
	node := readyNode("n1", 4000, 16*gi, nil)
	node.Spec.Unschedulable = true
	pod := simplePod("p1", 100, gi)

	sim := NewSimulator()
	result := sim.CanSchedule(pod, node, nil)
	if result.Feasible {
		t.Error("should not schedule on cordoned node")
	}
	if result.Reason != "node is cordoned" {
		t.Errorf("reason = %q, want 'node is cordoned'", result.Reason)
	}
}

// ---------------------------------------------------------------------------
// Taint / Toleration Tests
// ---------------------------------------------------------------------------

func TestCanSchedule_UntoleratedTaint(t *testing.T) {
	node := readyNode("n1", 4000, 16*gi, nil)
	node.Spec.Taints = []corev1.Taint{
		{Key: "special", Value: "true", Effect: corev1.TaintEffectNoSchedule},
	}
	pod := simplePod("p1", 100, gi)

	sim := NewSimulator()
	result := sim.CanSchedule(pod, node, nil)
	if result.Feasible {
		t.Error("should not schedule: pod doesn't tolerate taint")
	}
}

func TestCanSchedule_ToleratedTaint(t *testing.T) {
	node := readyNode("n1", 4000, 16*gi, nil)
	node.Spec.Taints = []corev1.Taint{
		{Key: "special", Value: "true", Effect: corev1.TaintEffectNoSchedule},
	}
	pod := simplePod("p1", 100, gi)
	pod.Spec.Tolerations = []corev1.Toleration{
		{Key: "special", Value: "true", Effect: corev1.TaintEffectNoSchedule, Operator: corev1.TolerationOpEqual},
	}

	sim := NewSimulator()
	result := sim.CanSchedule(pod, node, nil)
	if !result.Feasible {
		t.Errorf("should schedule: pod tolerates taint. Reason: %s", result.Reason)
	}
}

func TestCanSchedule_WildcardToleration(t *testing.T) {
	node := readyNode("n1", 4000, 16*gi, nil)
	node.Spec.Taints = []corev1.Taint{
		{Key: "anything", Value: "true", Effect: corev1.TaintEffectNoSchedule},
	}
	pod := simplePod("p1", 100, gi)
	pod.Spec.Tolerations = []corev1.Toleration{
		{Operator: corev1.TolerationOpExists}, // wildcard: tolerates everything
	}

	sim := NewSimulator()
	result := sim.CanSchedule(pod, node, nil)
	if !result.Feasible {
		t.Error("wildcard toleration should match all taints")
	}
}

func TestCanSchedule_NoExecuteTaint(t *testing.T) {
	node := readyNode("n1", 4000, 16*gi, nil)
	node.Spec.Taints = []corev1.Taint{
		{Key: "node.kubernetes.io/unreachable", Effect: corev1.TaintEffectNoExecute},
	}
	pod := simplePod("p1", 100, gi)

	sim := NewSimulator()
	result := sim.CanSchedule(pod, node, nil)
	if result.Feasible {
		t.Error("should not schedule: NoExecute taint not tolerated")
	}
}

// ---------------------------------------------------------------------------
// Node Selector Tests
// ---------------------------------------------------------------------------

func TestCanSchedule_NodeSelectorMatch(t *testing.T) {
	node := readyNode("n1", 4000, 16*gi, map[string]string{"zone": "us-east-1a", "tier": "frontend"})
	pod := simplePod("p1", 100, gi)
	pod.Spec.NodeSelector = map[string]string{"zone": "us-east-1a"}

	sim := NewSimulator()
	result := sim.CanSchedule(pod, node, nil)
	if !result.Feasible {
		t.Error("should schedule: node selector matches")
	}
}

func TestCanSchedule_NodeSelectorMismatch(t *testing.T) {
	node := readyNode("n1", 4000, 16*gi, map[string]string{"zone": "us-east-1a"})
	pod := simplePod("p1", 100, gi)
	pod.Spec.NodeSelector = map[string]string{"zone": "us-west-2a"}

	sim := NewSimulator()
	result := sim.CanSchedule(pod, node, nil)
	if result.Feasible {
		t.Error("should not schedule: node selector mismatch")
	}
	if result.Reason != "node selector mismatch" {
		t.Errorf("reason = %q, want 'node selector mismatch'", result.Reason)
	}
}

// ---------------------------------------------------------------------------
// Node Affinity Tests
// ---------------------------------------------------------------------------

func TestCanSchedule_NodeAffinityIn(t *testing.T) {
	node := readyNode("n1", 4000, 16*gi, map[string]string{"topology.kubernetes.io/zone": "us-east-1a"})
	pod := simplePod("p1", 100, gi)
	pod.Spec.Affinity = &corev1.Affinity{
		NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{
					{
						MatchExpressions: []corev1.NodeSelectorRequirement{
							{Key: "topology.kubernetes.io/zone", Operator: corev1.NodeSelectorOpIn, Values: []string{"us-east-1a", "us-east-1b"}},
						},
					},
				},
			},
		},
	}

	sim := NewSimulator()
	result := sim.CanSchedule(pod, node, nil)
	if !result.Feasible {
		t.Error("should schedule: node affinity In matches")
	}
}

func TestCanSchedule_NodeAffinityNotIn(t *testing.T) {
	node := readyNode("n1", 4000, 16*gi, map[string]string{"topology.kubernetes.io/zone": "us-east-1a"})
	pod := simplePod("p1", 100, gi)
	pod.Spec.Affinity = &corev1.Affinity{
		NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{
					{
						MatchExpressions: []corev1.NodeSelectorRequirement{
							{Key: "topology.kubernetes.io/zone", Operator: corev1.NodeSelectorOpNotIn, Values: []string{"us-east-1a"}},
						},
					},
				},
			},
		},
	}

	sim := NewSimulator()
	result := sim.CanSchedule(pod, node, nil)
	if result.Feasible {
		t.Error("should not schedule: node affinity NotIn excludes this zone")
	}
}

func TestCanSchedule_NodeAffinityExists(t *testing.T) {
	node := readyNode("n1", 4000, 16*gi, map[string]string{"gpu": "true"})
	pod := simplePod("p1", 100, gi)
	pod.Spec.Affinity = &corev1.Affinity{
		NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{
					{
						MatchExpressions: []corev1.NodeSelectorRequirement{
							{Key: "gpu", Operator: corev1.NodeSelectorOpExists},
						},
					},
				},
			},
		},
	}

	sim := NewSimulator()
	result := sim.CanSchedule(pod, node, nil)
	if !result.Feasible {
		t.Error("should schedule: gpu label exists")
	}
}

func TestCanSchedule_NodeAffinityDoesNotExist(t *testing.T) {
	node := readyNode("n1", 4000, 16*gi, map[string]string{"gpu": "true"})
	pod := simplePod("p1", 100, gi)
	pod.Spec.Affinity = &corev1.Affinity{
		NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{
					{
						MatchExpressions: []corev1.NodeSelectorRequirement{
							{Key: "gpu", Operator: corev1.NodeSelectorOpDoesNotExist},
						},
					},
				},
			},
		},
	}

	sim := NewSimulator()
	result := sim.CanSchedule(pod, node, nil)
	if result.Feasible {
		t.Error("should not schedule: gpu label exists but DoesNotExist required")
	}
}

func TestCanSchedule_NodeAffinityORTerms(t *testing.T) {
	node := readyNode("n1", 4000, 16*gi, map[string]string{"tier": "backend"})
	pod := simplePod("p1", 100, gi)
	pod.Spec.Affinity = &corev1.Affinity{
		NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{
					{
						MatchExpressions: []corev1.NodeSelectorRequirement{
							{Key: "tier", Operator: corev1.NodeSelectorOpIn, Values: []string{"frontend"}},
						},
					},
					{
						MatchExpressions: []corev1.NodeSelectorRequirement{
							{Key: "tier", Operator: corev1.NodeSelectorOpIn, Values: []string{"backend"}},
						},
					},
				},
			},
		},
	}

	sim := NewSimulator()
	result := sim.CanSchedule(pod, node, nil)
	if !result.Feasible {
		t.Error("should schedule: second OR term matches")
	}
}

// ---------------------------------------------------------------------------
// Pod Anti-Affinity Tests
// ---------------------------------------------------------------------------

func TestCanSchedule_PodAntiAffinity(t *testing.T) {
	node := readyNode("n1", 4000, 16*gi, map[string]string{"kubernetes.io/hostname": "n1"})
	pod := simplePod("p-new", 100, gi)
	pod.Labels = map[string]string{"app": "web"}
	pod.Spec.Affinity = &corev1.Affinity{
		PodAntiAffinity: &corev1.PodAntiAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
				{
					LabelSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "web"},
					},
					TopologyKey: "kubernetes.io/hostname",
				},
			},
		},
	}

	existing := []*corev1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "p-existing",
				Namespace: "default",
				Labels:    map[string]string{"app": "web"},
			},
		},
	}

	sim := NewSimulator()
	result := sim.CanSchedule(pod, node, existing)
	if result.Feasible {
		t.Error("should not schedule: anti-affinity with existing pod on same host")
	}
}

func TestCanSchedule_PodAntiAffinity_NoConflict(t *testing.T) {
	node := readyNode("n1", 4000, 16*gi, map[string]string{"kubernetes.io/hostname": "n1"})
	pod := simplePod("p-new", 100, gi)
	pod.Labels = map[string]string{"app": "web"}
	pod.Spec.Affinity = &corev1.Affinity{
		PodAntiAffinity: &corev1.PodAntiAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
				{
					LabelSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "web"},
					},
					TopologyKey: "kubernetes.io/hostname",
				},
			},
		},
	}

	existing := []*corev1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "p-other",
				Namespace: "default",
				Labels:    map[string]string{"app": "api"}, // different label
			},
		},
	}

	sim := NewSimulator()
	result := sim.CanSchedule(pod, node, existing)
	if !result.Feasible {
		t.Error("should schedule: no conflicting pod labels")
	}
}

// ---------------------------------------------------------------------------
// Topology Spread Constraints Tests
// ---------------------------------------------------------------------------

func TestCanSchedule_TopologySpread_WithinSkew(t *testing.T) {
	nodes := []*corev1.Node{
		readyNode("n1", 4000, 16*gi, map[string]string{"topology.kubernetes.io/zone": "zone-a"}),
		readyNode("n2", 4000, 16*gi, map[string]string{"topology.kubernetes.io/zone": "zone-b"}),
	}

	pod := simplePod("p-new", 100, gi)
	pod.Labels = map[string]string{"app": "web"}
	pod.Spec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{
		{
			MaxSkew:           1,
			TopologyKey:       "topology.kubernetes.io/zone",
			WhenUnsatisfiable: corev1.DoNotSchedule,
			LabelSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "web"},
			},
		},
	}

	podsByNode := map[string][]*corev1.Pod{
		"n1": {{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}}}},
		"n2": {},
	}

	sim := NewSimulator()
	// Schedule on zone-b (0 pods) while zone-a has 1 → after placement: 1,1 → skew 0 ≤ 1
	result := sim.CanScheduleWithTopology(pod, nodes[1], podsByNode["n2"], nodes, podsByNode)
	if !result.Feasible {
		t.Error("should schedule: topology spread within MaxSkew")
	}
}

func TestCanSchedule_TopologySpread_ExceedsSkew(t *testing.T) {
	nodes := []*corev1.Node{
		readyNode("n1", 4000, 16*gi, map[string]string{"topology.kubernetes.io/zone": "zone-a"}),
		readyNode("n2", 4000, 16*gi, map[string]string{"topology.kubernetes.io/zone": "zone-b"}),
	}

	pod := simplePod("p-new", 100, gi)
	pod.Labels = map[string]string{"app": "web"}
	pod.Spec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{
		{
			MaxSkew:           1,
			TopologyKey:       "topology.kubernetes.io/zone",
			WhenUnsatisfiable: corev1.DoNotSchedule,
			LabelSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "web"},
			},
		},
	}

	// zone-a has 2, zone-b has 0. Adding to zone-a → 3,0 → skew 3 > 1
	podsByNode := map[string][]*corev1.Pod{
		"n1": {
			{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}}},
			{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}}},
		},
		"n2": {},
	}

	sim := NewSimulator()
	result := sim.CanScheduleWithTopology(pod, nodes[0], podsByNode["n1"], nodes, podsByNode)
	if result.Feasible {
		t.Error("should not schedule: topology spread would exceed MaxSkew")
	}
}

// ---------------------------------------------------------------------------
// EffectivePodResources Tests
// ---------------------------------------------------------------------------

func TestEffectivePodResources_ContainersOnly(t *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    *resource.NewMilliQuantity(500, resource.DecimalSI),
							corev1.ResourceMemory: *resource.NewQuantity(gi, resource.BinarySI),
						},
					},
				},
				{
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    *resource.NewMilliQuantity(200, resource.DecimalSI),
							corev1.ResourceMemory: *resource.NewQuantity(512*1024*1024, resource.BinarySI),
						},
					},
				},
			},
		},
	}

	cpu, mem := EffectivePodResources(pod)
	if cpu != 700 {
		t.Errorf("CPU = %d, want 700", cpu)
	}
	expectedMem := int64(gi + 512*1024*1024)
	if mem != expectedMem {
		t.Errorf("memory = %d, want %d", mem, expectedMem)
	}
}

func TestEffectivePodResources_InitContainerDominates(t *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{
				{
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    *resource.NewMilliQuantity(2000, resource.DecimalSI),
							corev1.ResourceMemory: *resource.NewQuantity(4*gi, resource.BinarySI),
						},
					},
				},
			},
			Containers: []corev1.Container{
				{
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    *resource.NewMilliQuantity(500, resource.DecimalSI),
							corev1.ResourceMemory: *resource.NewQuantity(gi, resource.BinarySI),
						},
					},
				},
			},
		},
	}

	cpu, mem := EffectivePodResources(pod)
	// Init container dominates: max(sum(containers), max(initContainers))
	if cpu != 2000 {
		t.Errorf("CPU = %d, want 2000 (init container dominates)", cpu)
	}
	if mem != 4*gi {
		t.Errorf("memory = %d, want %d (init container dominates)", mem, 4*gi)
	}
}

func TestEffectivePodResources_ContainersDominate(t *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{
				{
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    *resource.NewMilliQuantity(100, resource.DecimalSI),
							corev1.ResourceMemory: *resource.NewQuantity(256*1024*1024, resource.BinarySI),
						},
					},
				},
			},
			Containers: []corev1.Container{
				{
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    *resource.NewMilliQuantity(500, resource.DecimalSI),
							corev1.ResourceMemory: *resource.NewQuantity(2*gi, resource.BinarySI),
						},
					},
				},
				{
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    *resource.NewMilliQuantity(300, resource.DecimalSI),
							corev1.ResourceMemory: *resource.NewQuantity(gi, resource.BinarySI),
						},
					},
				},
			},
		},
	}

	cpu, mem := EffectivePodResources(pod)
	if cpu != 800 {
		t.Errorf("CPU = %d, want 800 (sum of containers)", cpu)
	}
	if mem != 3*gi {
		t.Errorf("memory = %d, want %d (sum of containers)", mem, 3*gi)
	}
}

// ---------------------------------------------------------------------------
// Predicate Helper Tests
// ---------------------------------------------------------------------------

func TestHasGPURequest(t *testing.T) {
	gpuPod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							"nvidia.com/gpu": *resource.NewQuantity(1, resource.DecimalSI),
						},
					},
				},
			},
		},
	}
	if !HasGPURequest(gpuPod) {
		t.Error("should detect GPU request")
	}

	cpuPod := simplePod("p1", 1000, gi)
	if HasGPURequest(cpuPod) {
		t.Error("should not detect GPU request on CPU-only pod")
	}
}

func TestIsNodeReady(t *testing.T) {
	ready := readyNode("n1", 4000, 16*gi, nil)
	if !IsNodeReady(ready) {
		t.Error("should be ready")
	}

	notReady := &corev1.Node{
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionFalse},
			},
		},
	}
	if IsNodeReady(notReady) {
		t.Error("should not be ready")
	}

	noCondition := &corev1.Node{}
	if IsNodeReady(noCondition) {
		t.Error("should not be ready with no conditions")
	}
}

func TestIsPodRunning(t *testing.T) {
	if !IsPodRunning(&corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodRunning}}) {
		t.Error("running pod should be running")
	}
	if IsPodRunning(&corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodPending}}) {
		t.Error("pending pod should not be running")
	}
}

func TestIsPodPending(t *testing.T) {
	if !IsPodPending(&corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodPending}}) {
		t.Error("pending pod should be pending")
	}
	if IsPodPending(&corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodRunning}}) {
		t.Error("running pod should not be pending")
	}
}

func TestGetPodCPURequest(t *testing.T) {
	pod := simplePod("p1", 1500, gi)
	if got := GetPodCPURequest(pod); got != 1500 {
		t.Errorf("CPU request = %d, want 1500", got)
	}
}

func TestGetPodMemoryRequest(t *testing.T) {
	pod := simplePod("p1", 1000, 4*gi)
	if got := GetPodMemoryRequest(pod); got != 4*gi {
		t.Errorf("memory request = %d, want %d", got, 4*gi)
	}
}

// ---------------------------------------------------------------------------
// FindFittingNodes / CountUnschedulable
// ---------------------------------------------------------------------------

func TestFindFittingNodes(t *testing.T) {
	nodes := []*corev1.Node{
		readyNode("big", 8000, 32*gi, nil),
		readyNode("small", 1000, 2*gi, nil),
	}
	pod := simplePod("p1", 2000, 4*gi)
	podsByNode := map[string][]*corev1.Pod{
		"big":   {},
		"small": {},
	}

	sim := NewSimulator()
	fitting := sim.FindFittingNodes(pod, nodes, podsByNode)
	if len(fitting) != 1 {
		t.Fatalf("expected 1 fitting node, got %d", len(fitting))
	}
	if fitting[0] != "big" {
		t.Errorf("fitting node = %q, want 'big'", fitting[0])
	}
}

func TestCountUnschedulable(t *testing.T) {
	nodes := []*corev1.Node{
		readyNode("n1", 2000, 4*gi, nil),
	}
	pendingPods := []*corev1.Pod{
		simplePod("fits", 1000, 2*gi),
		simplePod("too-big", 4000, 8*gi),
	}
	podsByNode := map[string][]*corev1.Pod{
		"n1": {},
	}

	sim := NewSimulator()
	count := sim.CountUnschedulable(pendingPods, nodes, podsByNode)
	if count != 1 {
		t.Errorf("unschedulable count = %d, want 1", count)
	}
}

// ---------------------------------------------------------------------------
// Toleration Matching Details
// ---------------------------------------------------------------------------

func TestTolerationMatchesTaint_ExactMatch(t *testing.T) {
	taint := corev1.Taint{Key: "key1", Value: "val1", Effect: corev1.TaintEffectNoSchedule}
	tol := corev1.Toleration{Key: "key1", Value: "val1", Effect: corev1.TaintEffectNoSchedule, Operator: corev1.TolerationOpEqual}
	if !tolerationMatchesTaint(tol, taint) {
		t.Error("exact match should succeed")
	}
}

func TestTolerationMatchesTaint_KeyMismatch(t *testing.T) {
	taint := corev1.Taint{Key: "key1", Value: "val1", Effect: corev1.TaintEffectNoSchedule}
	tol := corev1.Toleration{Key: "key2", Value: "val1", Effect: corev1.TaintEffectNoSchedule, Operator: corev1.TolerationOpEqual}
	if tolerationMatchesTaint(tol, taint) {
		t.Error("key mismatch should fail")
	}
}

func TestTolerationMatchesTaint_ExistsOperator(t *testing.T) {
	taint := corev1.Taint{Key: "key1", Value: "anything", Effect: corev1.TaintEffectNoSchedule}
	tol := corev1.Toleration{Key: "key1", Operator: corev1.TolerationOpExists}
	if !tolerationMatchesTaint(tol, taint) {
		t.Error("Exists operator should match regardless of value")
	}
}

func TestTolerationMatchesTaint_EffectMismatch(t *testing.T) {
	taint := corev1.Taint{Key: "key1", Value: "val1", Effect: corev1.TaintEffectNoExecute}
	tol := corev1.Toleration{Key: "key1", Value: "val1", Effect: corev1.TaintEffectNoSchedule, Operator: corev1.TolerationOpEqual}
	if tolerationMatchesTaint(tol, taint) {
		t.Error("effect mismatch should fail")
	}
}

func TestTolerationMatchesTaint_EmptyEffectMatchesAll(t *testing.T) {
	taint := corev1.Taint{Key: "key1", Value: "val1", Effect: corev1.TaintEffectNoExecute}
	tol := corev1.Toleration{Key: "key1", Value: "val1", Effect: "", Operator: corev1.TolerationOpEqual}
	if !tolerationMatchesTaint(tol, taint) {
		t.Error("empty effect should match all effects")
	}
}

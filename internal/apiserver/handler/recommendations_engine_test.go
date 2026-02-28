package handler

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	intmetrics "github.com/koptimizer/koptimizer/internal/metrics"
	"github.com/koptimizer/koptimizer/internal/state"
	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
	pkgmetrics "github.com/koptimizer/koptimizer/pkg/metrics"
)

// --- helpers to build test fixtures ---

func makeNode(name string, cpuCapMilli, memCapBytes, cpuUsedMilli, memUsedBytes int64, hourlyCost float64, opts ...func(*state.NodeState)) *state.NodeState {
	n := &state.NodeState{
		Node: &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: name},
		},
		CPUCapacity:    cpuCapMilli,
		MemoryCapacity: memCapBytes,
		CPUUsed:        cpuUsedMilli,
		MemoryUsed:     memUsedBytes,
		HourlyCostUSD:  hourlyCost,
	}
	for _, fn := range opts {
		fn(n)
	}
	return n
}

func withPods(pods ...*corev1.Pod) func(*state.NodeState) {
	return func(n *state.NodeState) {
		n.Pods = pods
		n.CPURequested = 0
		n.MemoryRequested = 0
		for _, p := range pods {
			cpu, mem := state.ExtractPodRequests(p)
			n.CPURequested += cpu
			n.MemoryRequested += mem
		}
	}
}

func withGPU(n *state.NodeState) { n.IsGPUNode = true; n.GPUCapacity = 1 }
func withSpot(n *state.NodeState) { n.IsSpot = true }

func withNodeGroup(id, name string) func(*state.NodeState) {
	return func(n *state.NodeState) {
		n.NodeGroupID = id
		n.NodeGroupName = name
	}
}

func makePod(ns, name, nodeName string, cpuReqMilli, memReqBytes, cpuUsageMilli, memUsageBytes int64, ownerKind, ownerName string) *state.PodState {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Spec: corev1.PodSpec{
			NodeName: nodeName,
			Containers: []corev1.Container{{
				Name: "main",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    *resource.NewMilliQuantity(cpuReqMilli, resource.DecimalSI),
						corev1.ResourceMemory: *resource.NewQuantity(memReqBytes, resource.BinarySI),
					},
				},
			}},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}
	if ownerKind != "" {
		pod.OwnerReferences = []metav1.OwnerReference{{Kind: ownerKind, Name: ownerName}}
	}
	return &state.PodState{
		Pod:           pod,
		NodeName:      nodeName,
		Namespace:     ns,
		Name:          name,
		OwnerKind:     ownerKind,
		OwnerName:     ownerName,
		CPURequest:    cpuReqMilli,
		MemoryRequest: memReqBytes,
		CPUUsage:      cpuUsageMilli,
		MemoryUsage:   memUsageBytes,
	}
}

func makeDaemonSetPod(ns, name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			OwnerReferences: []metav1.OwnerReference{{
				Kind: "DaemonSet",
				Name: "kube-proxy",
			}},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name: "main",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("100m"),
						corev1.ResourceMemory: resource.MustParse("128Mi"),
					},
				},
			}},
		},
	}
}

func makeWorkloadPod(ns, name string, cpuReq string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			OwnerReferences: []metav1.OwnerReference{{
				Kind: "Deployment",
				Name: "web-app",
			}},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name: "main",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse(cpuReq),
						corev1.ResourceMemory: resource.MustParse("256Mi"),
					},
				},
			}},
		},
	}
}

func makeNodeGroup(id, name string, nodes []*state.NodeState) *state.NodeGroupInfo {
	ng := &state.NodeGroupInfo{
		NodeGroup: &cloudprovider.NodeGroup{ID: id, Name: name},
		Nodes:     nodes,
	}
	for _, n := range nodes {
		ng.TotalCPU += n.CPUCapacity
		ng.TotalMemory += n.MemoryCapacity
		ng.UsedCPU += n.CPUUsed
		ng.UsedMemory += n.MemoryUsed
		ng.MonthlyCostUSD += n.HourlyCostUSD * 730
	}
	return ng
}

// --- Tests ---

func TestEmptyNodeRecs(t *testing.T) {
	// Node with only DS pods → empty
	emptyNode := makeNode("empty-1", 4000, 8e9, 100, 100e6, 0.20,
		withPods(makeDaemonSetPod("kube-system", "kube-proxy-abc")))

	// Node with real workload → not empty
	busyNode := makeNode("busy-1", 4000, 8e9, 2000, 4e9, 0.20,
		withPods(makeWorkloadPod("default", "web-1", "500m")))

	// GPU empty node → skipped
	gpuNode := makeNode("gpu-1", 8000, 16e9, 0, 0, 0.50, withGPU)

	recs := computeFromData(
		[]*state.NodeState{emptyNode, busyNode, gpuNode},
		nil, nil, nil,
	)

	count := 0
	for _, r := range recs {
		if r.Target == "empty-1" && r.Priority == "critical" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 empty-node rec for empty-1, got %d (total recs: %d)", count, len(recs))
	}
	// GPU node should NOT appear
	for _, r := range recs {
		if r.Target == "gpu-1" {
			t.Error("GPU empty node should not generate a recommendation")
		}
	}
}

func TestUnderutilizedNodeRecs(t *testing.T) {
	// Node at 5% CPU, 3% mem → high priority
	lowNode := makeNode("low-1", 16000, 64e9, 800, int64(1.92e9), 0.50,
		withPods(makeWorkloadPod("default", "pod-1", "200m")))

	// Node at 15% CPU, 12% mem → medium priority
	medNode := makeNode("med-1", 4000, 8e9, 600, int64(0.96e9), 0.10,
		withPods(makeWorkloadPod("default", "pod-2", "600m")))

	// Node at 50% CPU → above threshold
	highNode := makeNode("high-1", 4000, 8e9, 2000, 4e9, 0.10,
		withPods(makeWorkloadPod("default", "pod-3", "2000m")))

	recs := computeFromData(
		[]*state.NodeState{lowNode, medNode, highNode},
		nil, nil, nil,
	)

	var lowRec, medRec *ComputedRecommendation
	for i, r := range recs {
		if r.Target == "low-1" {
			lowRec = &recs[i]
		}
		if r.Target == "med-1" {
			medRec = &recs[i]
		}
	}

	if lowRec == nil {
		t.Fatal("expected underutil rec for low-1")
	}
	if lowRec.Priority != "high" {
		t.Errorf("low-1 priority: got %s, want high", lowRec.Priority)
	}

	if medRec == nil {
		t.Fatal("expected underutil rec for med-1")
	}
	if medRec.Priority != "medium" {
		t.Errorf("med-1 priority: got %s, want medium", medRec.Priority)
	}

	for _, r := range recs {
		if r.Target == "high-1" && r.Type == "consolidation" {
			t.Error("high-1 should not get a consolidation rec")
		}
	}
}

func TestSpotAdoptionRecs(t *testing.T) {
	n1 := makeNode("od-1", 4000, 8e9, 2000, 4e9, 0.10,
		withNodeGroup("ng-1", "web-pool"),
		withPods(makeWorkloadPod("default", "p1", "1000m")))
	n2 := makeNode("od-2", 4000, 8e9, 2000, 4e9, 0.10,
		withNodeGroup("ng-1", "web-pool"),
		withPods(makeWorkloadPod("default", "p2", "1000m")))
	// Already spot → skip
	spotNode := makeNode("spot-1", 4000, 8e9, 2000, 4e9, 0.05,
		withSpot, withNodeGroup("ng-2", "spot-pool"),
		withPods(makeWorkloadPod("default", "p3", "1000m")))
	// GPU → skip
	gpuNode := makeNode("gpu-2", 8000, 16e9, 4000, 8e9, 0.50,
		withGPU, withNodeGroup("ng-3", "gpu-pool"),
		withPods(makeWorkloadPod("default", "p4", "2000m")))

	recs := computeFromData(
		[]*state.NodeState{n1, n2, spotNode, gpuNode},
		nil, nil, nil,
	)

	spotRecs := 0
	for _, r := range recs {
		if r.Type == "spot" {
			spotRecs++
			if r.Target != "web-pool" {
				t.Errorf("spot rec target: got %s, want web-pool", r.Target)
			}
			// 2 nodes * $0.10/h * 0.60 * 730.5 = $87.66
			if r.EstimatedSavings < 83 || r.EstimatedSavings > 92 {
				t.Errorf("spot savings: got %.2f, want ~88", r.EstimatedSavings)
			}
		}
	}
	if spotRecs != 1 {
		t.Errorf("expected 1 spot rec, got %d", spotRecs)
	}
}

func TestPodRightsizingRecs(t *testing.T) {
	node := makeNode("n1", 16000, 64e9, 1000, 2e9, 1.00,
		withPods(makeWorkloadPod("app", "web-1", "4000m")))

	// Pod requesting 4000m CPU but only using 400m (10% efficiency) → overprovisioned
	pod := makePod("app", "web-1", "n1",
		4000, 4e9,  // requests
		400, 400e6, // usage: 10% CPU, 10% mem
		"Deployment", "web-app")

	recs := computeFromData([]*state.NodeState{node}, []*state.PodState{pod}, nil, nil)

	found := false
	for _, r := range recs {
		if r.Type == "rightsizing" && r.Target == "app/Deployment/web-app" {
			found = true
			if r.EstimatedSavings <= 0 {
				t.Errorf("rightsizing savings should be > 0, got %.2f", r.EstimatedSavings)
			}
		}
	}
	if !found {
		t.Error("expected rightsizing rec for app/Deployment/web-app")
	}
}

func TestPodRightsizingSkipsSystemNamespaces(t *testing.T) {
	node := makeNode("n1", 16000, 64e9, 1000, 2e9, 1.00,
		withPods(makeWorkloadPod("kube-system", "coredns-1", "2000m")))

	pod := makePod("kube-system", "coredns-1", "n1",
		2000, 2e9, 200, 200e6,
		"Deployment", "coredns")

	recs := computeFromData([]*state.NodeState{node}, []*state.PodState{pod}, nil, nil)

	for _, r := range recs {
		if r.Type == "rightsizing" && r.Target == "kube-system/Deployment/coredns" {
			t.Error("should not generate rightsizing rec for kube-system")
		}
	}
}

func TestNodeGroupRightsizingRecs(t *testing.T) {
	// 4 nodes at ~10% utilization
	nodes := make([]*state.NodeState, 4)
	for i := range nodes {
		nodes[i] = makeNode("n"+string(rune('1'+i)), 8000, 32e9, 800, int64(3.2e9), 0.30)
	}

	ng := makeNodeGroup("ng-test", "test-pool", nodes)

	recs := computeFromData(nil, nil, []*state.NodeGroupInfo{ng}, nil)

	found := false
	for _, r := range recs {
		if r.Type == "consolidation" && r.Target == "test-pool" {
			found = true
			if r.EstimatedSavings <= 0 {
				t.Errorf("ng rightsizing savings should be > 0, got %.2f", r.EstimatedSavings)
			}
		}
	}
	if !found {
		t.Error("expected node group rightsizing rec for test-pool")
	}
}

func TestNodeGroupSkipsGPU(t *testing.T) {
	n1 := makeNode("g1", 8000, 32e9, 800, 3e9, 0.50, withGPU)
	n2 := makeNode("g2", 8000, 32e9, 800, 3e9, 0.50, withGPU)
	ng := makeNodeGroup("ng-gpu", "gpu-pool", []*state.NodeState{n1, n2})

	recs := computeFromData(nil, nil, []*state.NodeGroupInfo{ng}, nil)

	for _, r := range recs {
		if r.Target == "gpu-pool" {
			t.Error("should not generate rec for GPU node group")
		}
	}
}

func TestNodeGroupSkipsSingleNode(t *testing.T) {
	n := makeNode("solo", 8000, 32e9, 800, 3e9, 0.30)
	ng := makeNodeGroup("ng-solo", "solo-pool", []*state.NodeState{n})

	recs := computeFromData(nil, nil, []*state.NodeGroupInfo{ng}, nil)

	for _, r := range recs {
		if r.Target == "solo-pool" {
			t.Error("should not generate rec for single-node group")
		}
	}
}

func TestMinSavingsThreshold(t *testing.T) {
	// Cheap empty node → below $5/mo threshold
	cheapNode := makeNode("cheap-1", 1000, 1e9, 0, 0, 0.001,
		withPods(makeDaemonSetPod("kube-system", "proxy")))

	recs := computeFromData([]*state.NodeState{cheapNode}, nil, nil, nil)

	for _, r := range recs {
		if r.Target == "cheap-1" {
			t.Error("cheap node should be below $5/mo threshold")
		}
	}
}

func TestDeterministicIDs(t *testing.T) {
	id1 := computedID("consolidation", "node-abc")
	id2 := computedID("consolidation", "node-abc")
	id3 := computedID("consolidation", "node-xyz")

	if id1 != id2 {
		t.Errorf("same inputs should produce same ID: %s != %s", id1, id2)
	}
	if id1 == id3 {
		t.Error("different inputs should produce different IDs")
	}
	if len(id1) < 20 {
		t.Errorf("ID too short: %s", id1)
	}
}

func TestComputeSavingsOpportunities(t *testing.T) {
	recs := []ComputedRecommendation{
		{Type: "consolidation", Target: "node-1", Description: "desc-1", EstimatedSavings: 100},
		{Type: "spot", Target: "ng-1", Description: "desc-2", EstimatedSavings: 200},
	}
	opps := ComputeSavingsOpportunities(recs)
	if len(opps) != 2 {
		t.Fatalf("expected 2 opportunities, got %d", len(opps))
	}
	if opps[0].Type != "consolidation" || opps[0].EstimatedSavings != 100 {
		t.Errorf("first opp mismatch: %+v", opps[0])
	}
}

func TestComputeTotalPotentialSavings(t *testing.T) {
	recs := []ComputedRecommendation{
		{Type: "consolidation", Target: "node-1", EstimatedSavings: 100.50},
		{Type: "spot", Target: "ng-1", EstimatedSavings: 200.25},
		{Type: "rightsizing", Target: "wl-1", EstimatedSavings: 50.00},
	}
	total := ComputeTotalPotentialSavings(recs)
	if total != 350.75 {
		t.Errorf("total savings: got %.2f, want 350.75", total)
	}
}

func TestComputeTotalPotentialSavings_Dedup(t *testing.T) {
	// Two recommendations for the same target should be deduped (keep highest)
	recs := []ComputedRecommendation{
		{Type: "consolidation", Target: "node-1", EstimatedSavings: 100.00},
		{Type: "consolidation", Target: "node-1", EstimatedSavings: 150.00},
		{Type: "spot", Target: "ng-1", EstimatedSavings: 200.00},
	}
	total := ComputeTotalPotentialSavings(recs)
	if total != 350.00 {
		t.Errorf("total savings with dedup: got %.2f, want 350.00", total)
	}
}

func TestComputeTotalPotentialSavings_MultipleTargets(t *testing.T) {
	recs := []ComputedRecommendation{
		{Type: "consolidation", Target: "node-1", EstimatedSavings: 5000},
		{Type: "rightsizing", Target: "wl-1", EstimatedSavings: 8000},
		{Type: "spot", Target: "ng-1", EstimatedSavings: 3000},
	}
	total := ComputeTotalPotentialSavings(recs)
	if total != 16000 {
		t.Errorf("total savings: got %.2f, want 16000.00", total)
	}
}

func TestComputeTotalPotentialSavings_SpotAndConsolidationDedup(t *testing.T) {
	// Same target with consolidation AND spot should only count once (max)
	recs := []ComputedRecommendation{
		{Type: "consolidation", Target: "my-nodegroup", EstimatedSavings: 4000},
		{Type: "spot", Target: "my-nodegroup", EstimatedSavings: 3000},
	}
	total := ComputeTotalPotentialSavings(recs)
	if total != 4000 {
		t.Errorf("same target should dedup to max: got %.2f, want 4000.00", total)
	}
}

func TestSortedBySavingsDescending(t *testing.T) {
	n1 := makeNode("cheap", 4000, 8e9, 100, 100e6, 0.05,
		withPods(makeDaemonSetPod("kube-system", "proxy")))
	n2 := makeNode("expensive", 4000, 8e9, 100, 100e6, 0.50,
		withPods(makeDaemonSetPod("kube-system", "proxy")))

	recs := computeFromData([]*state.NodeState{n1, n2}, nil, nil, nil)

	if len(recs) < 2 {
		t.Fatalf("expected at least 2 recs, got %d", len(recs))
	}
	for i := 1; i < len(recs); i++ {
		if recs[i].EstimatedSavings > recs[i-1].EstimatedSavings {
			t.Errorf("recs not sorted: [%d]=$%.2f > [%d]=$%.2f",
				i, recs[i].EstimatedSavings, i-1, recs[i-1].EstimatedSavings)
		}
	}
}

func TestUnderutilizedWithHistoricalP95(t *testing.T) {
	// Create a metrics store and seed it with 6h of synthetic data
	ms := intmetrics.NewStore(7 * 24 * time.Hour)
	now := time.Now()

	// Node at 5% P95 CPU, 3% P95 memory (capacity: 16000m CPU, 64GiB mem)
	// Seed 500 points within last 5h55m (>360 threshold, well within 6h window)
	for i := 0; i < 500; i++ {
		ms.RecordNodeMetrics(pkgmetrics.NodeMetrics{
			Name:        "hist-node-1",
			Timestamp:   now.Add(-355*time.Minute + time.Duration(i)*43*time.Second),
			CPUUsage:    800,     // 800m out of 16000m = 5%
			MemoryUsage: 1920e6, // ~1.92GB out of 64GB = 3%
		})
	}

	node := makeNode("hist-node-1", 16000, 64e9, 800, int64(1.92e9), 0.50,
		withPods(makeWorkloadPod("default", "pod-1", "200m")))

	recs := computeFromData([]*state.NodeState{node}, nil, nil, ms)

	var found *ComputedRecommendation
	for i, r := range recs {
		if r.Target == "hist-node-1" && r.Type == "consolidation" {
			found = &recs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected underutil rec for hist-node-1 with historical data")
	}
	// Historical data should give confidence 0.90
	if found.Confidence != 0.90 {
		t.Errorf("confidence: got %.2f, want 0.90", found.Confidence)
	}
	if found.Priority != "high" {
		t.Errorf("priority: got %s, want high (CPU 5%%, mem 3%%)", found.Priority)
	}
}

func TestUnderutilizedFallsBackWithoutEnoughData(t *testing.T) {
	// Create a metrics store with insufficient data points (<360)
	ms := intmetrics.NewStore(7 * 24 * time.Hour)
	now := time.Now()

	// Only 100 points — below 360 threshold
	for i := 0; i < 100; i++ {
		ms.RecordNodeMetrics(pkgmetrics.NodeMetrics{
			Name:        "sparse-node",
			Timestamp:   now.Add(-time.Duration(100-i) * time.Minute),
			CPUUsage:    800,
			MemoryUsage: 1920e6,
		})
	}

	node := makeNode("sparse-node", 16000, 64e9, 800, int64(1.92e9), 0.50,
		withPods(makeWorkloadPod("default", "pod-1", "200m")))

	recs := computeFromData([]*state.NodeState{node}, nil, nil, ms)

	var found *ComputedRecommendation
	for i, r := range recs {
		if r.Target == "sparse-node" && r.Type == "consolidation" {
			found = &recs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected underutil rec for sparse-node via point-in-time fallback")
	}
	// Should fall back to point-in-time confidence
	if found.Confidence != 0.70 {
		t.Errorf("confidence: got %.2f, want 0.70 (fallback)", found.Confidence)
	}
}

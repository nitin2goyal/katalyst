package evictor

import (
	"math"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/pkg/cost"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func makePod(name, namespace string, cpuMilli, memBytes int64) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
}

func makeDaemonSetPod(name, namespace string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "DaemonSet", Name: "ds-1"},
			},
		},
	}
}

func makeNode(name string, cpuCap, cpuReq, memCap, memReq int64, hourlyCost float64, pods []*corev1.Pod) optimizer.NodeInfo {
	return optimizer.NodeInfo{
		Node: &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: name},
		},
		CPUCapacity:     cpuCap,
		MemoryCapacity:  memCap,
		CPURequested:    cpuReq,
		MemoryRequested: memReq,
		HourlyCostUSD:   hourlyCost,
		Pods:            pods,
	}
}

func approxEqual(a, b, epsilon float64) bool {
	return math.Abs(a-b) < epsilon
}

// memPct calculates a percentage of a memory capacity value at runtime,
// avoiding compile-time constant overflow issues with int64 conversions.
func memPct(capacity int64, pct float64) int64 {
	return int64(float64(capacity) * pct)
}

// ---------------------------------------------------------------------------
// FragmentationScorer.Score tests
// ---------------------------------------------------------------------------

func TestFragmentationScorer_Score(t *testing.T) {
	scorer := NewFragmentationScorer()

	tests := []struct {
		name            string
		nodes           []optimizer.NodeInfo
		wantScores      []float64  // expected Score per node (order matches input)
		wantCandidate   []bool     // expected IsCandidate per node
		wantPodCounts   []int      // expected PodCount per node
		wantCPUUtilPcts []float64  // expected CPUUtilPct per node
		wantMemUtilPcts []float64  // expected MemUtilPct per node
		epsilon         float64
	}{
		{
			name: "fully utilized node (90% CPU, 90% mem)",
			nodes: []optimizer.NodeInfo{
				makeNode("node-full", 4000, 3600, 16*1024*1024*1024, memPct(16*1024*1024*1024, 0.9), 0.20,
					[]*corev1.Pod{makePod("pod-1", "default", 3600, 0)}),
			},
			wantScores:      []float64{0.1},
			wantCandidate:   []bool{false},
			wantPodCounts:   []int{1},
			wantCPUUtilPcts: []float64{90.0},
			wantMemUtilPcts: []float64{90.0},
			epsilon:         0.02,
		},
		{
			name: "nearly empty node (10% CPU, 10% mem)",
			nodes: []optimizer.NodeInfo{
				makeNode("node-empty", 4000, 400, 16*1024*1024*1024, memPct(16*1024*1024*1024, 0.1), 0.20,
					[]*corev1.Pod{makePod("pod-1", "default", 400, 0)}),
			},
			wantScores:      []float64{0.9},
			wantCandidate:   []bool{true},
			wantPodCounts:   []int{1},
			wantCPUUtilPcts: []float64{10.0},
			wantMemUtilPcts: []float64{10.0},
			epsilon:         0.02,
		},
		{
			name: "mixed utilization (80% CPU, 20% mem)",
			nodes: []optimizer.NodeInfo{
				makeNode("node-mixed", 4000, 3200, 16*1024*1024*1024, memPct(16*1024*1024*1024, 0.2), 0.20,
					[]*corev1.Pod{makePod("pod-1", "default", 3200, 0)}),
			},
			wantScores:      []float64{0.5},
			wantCandidate:   []bool{false},
			wantPodCounts:   []int{1},
			wantCPUUtilPcts: []float64{80.0},
			wantMemUtilPcts: []float64{20.0},
			epsilon:         0.02,
		},
		{
			name: "completely empty node (0% utilization)",
			nodes: []optimizer.NodeInfo{
				makeNode("node-zero", 4000, 0, 16*1024*1024*1024, 0, 0.20, nil),
			},
			wantScores:      []float64{1.0},
			wantCandidate:   []bool{true},
			wantPodCounts:   []int{0},
			wantCPUUtilPcts: []float64{0.0},
			wantMemUtilPcts: []float64{0.0},
			epsilon:         0.001,
		},
		{
			name: "DaemonSet pods are excluded from PodCount",
			nodes: []optimizer.NodeInfo{
				makeNode("node-ds", 4000, 400, 16*1024*1024*1024, memPct(16*1024*1024*1024, 0.1), 0.20,
					[]*corev1.Pod{
						makePod("app-pod", "default", 200, 0),
						makeDaemonSetPod("ds-pod", "kube-system"),
					}),
			},
			wantScores:      []float64{0.9},
			wantCandidate:   []bool{true},
			wantPodCounts:   []int{1}, // only app-pod counted
			wantCPUUtilPcts: []float64{10.0},
			wantMemUtilPcts: []float64{10.0},
			epsilon:         0.02,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := &optimizer.ClusterSnapshot{Nodes: tt.nodes}
			scores := scorer.Score(snapshot)

			if len(scores) != len(tt.nodes) {
				t.Fatalf("got %d scores, want %d", len(scores), len(tt.nodes))
			}

			for i, s := range scores {
				if !approxEqual(s.Score, tt.wantScores[i], tt.epsilon) {
					t.Errorf("node %q Score = %.4f, want ~%.4f (epsilon %.4f)",
						s.NodeName, s.Score, tt.wantScores[i], tt.epsilon)
				}
				if s.IsCandidate != tt.wantCandidate[i] {
					t.Errorf("node %q IsCandidate = %v, want %v",
						s.NodeName, s.IsCandidate, tt.wantCandidate[i])
				}
				if s.PodCount != tt.wantPodCounts[i] {
					t.Errorf("node %q PodCount = %d, want %d",
						s.NodeName, s.PodCount, tt.wantPodCounts[i])
				}
				if !approxEqual(s.CPUUtilPct, tt.wantCPUUtilPcts[i], tt.epsilon) {
					t.Errorf("node %q CPUUtilPct = %.2f, want ~%.2f",
						s.NodeName, s.CPUUtilPct, tt.wantCPUUtilPcts[i])
				}
				if !approxEqual(s.MemUtilPct, tt.wantMemUtilPcts[i], tt.epsilon) {
					t.Errorf("node %q MemUtilPct = %.2f, want ~%.2f",
						s.NodeName, s.MemUtilPct, tt.wantMemUtilPcts[i])
				}
			}
		})
	}
}

func TestFragmentationScorer_Score_CPUFreeAndMemFree(t *testing.T) {
	scorer := NewFragmentationScorer()

	cpuCap := int64(4000)
	cpuReq := int64(1500)
	memCap := int64(16 * 1024 * 1024 * 1024)
	memReq := int64(4 * 1024 * 1024 * 1024)

	snapshot := &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{
			makeNode("node-1", cpuCap, cpuReq, memCap, memReq, 0.10,
				[]*corev1.Pod{makePod("p1", "default", 1500, 0)}),
		},
	}

	scores := scorer.Score(snapshot)
	if len(scores) != 1 {
		t.Fatalf("expected 1 score, got %d", len(scores))
	}

	s := scores[0]
	if s.CPUFree != cpuCap-cpuReq {
		t.Errorf("CPUFree = %d, want %d", s.CPUFree, cpuCap-cpuReq)
	}
	if s.MemFree != memCap-memReq {
		t.Errorf("MemFree = %d, want %d", s.MemFree, memCap-memReq)
	}
}

func TestFragmentationScorer_Score_MultipleNodesSorted(t *testing.T) {
	scorer := NewFragmentationScorer()

	mem16g := int64(16 * 1024 * 1024 * 1024)

	snapshot := &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{
			// Node A: 90% utilized -> Score ~0.1
			makeNode("node-a", 4000, 3600, mem16g, int64(float64(mem16g)*0.9), 0.20,
				[]*corev1.Pod{makePod("pa", "default", 0, 0)}),
			// Node B: 10% utilized -> Score ~0.9
			makeNode("node-b", 4000, 400, mem16g, int64(float64(mem16g)*0.1), 0.20,
				[]*corev1.Pod{makePod("pb", "default", 0, 0)}),
			// Node C: 50% utilized -> Score ~0.5
			makeNode("node-c", 4000, 2000, mem16g, int64(float64(mem16g)*0.5), 0.20,
				[]*corev1.Pod{makePod("pc", "default", 0, 0)}),
		},
	}

	scores := scorer.Score(snapshot)

	// Find each node's score by name.
	scoreMap := make(map[string]NodeScore)
	for _, s := range scores {
		scoreMap[s.NodeName] = s
	}

	if scoreMap["node-a"].Score >= scoreMap["node-c"].Score {
		t.Errorf("node-a (Score=%.2f) should have lower score than node-c (Score=%.2f)",
			scoreMap["node-a"].Score, scoreMap["node-c"].Score)
	}
	if scoreMap["node-c"].Score >= scoreMap["node-b"].Score {
		t.Errorf("node-c (Score=%.2f) should have lower score than node-b (Score=%.2f)",
			scoreMap["node-c"].Score, scoreMap["node-b"].Score)
	}

	// Only node-b (Score > 0.6) should be a candidate.
	if scoreMap["node-a"].IsCandidate {
		t.Error("node-a should not be a candidate")
	}
	if scoreMap["node-c"].IsCandidate {
		t.Error("node-c should not be a candidate")
	}
	if !scoreMap["node-b"].IsCandidate {
		t.Error("node-b should be a candidate")
	}
}

// ---------------------------------------------------------------------------
// Consolidator.Plan tests
// ---------------------------------------------------------------------------

func defaultTestConfig() *config.Config {
	return &config.Config{
		Mode: "active",
		Evictor: config.EvictorConfig{
			MaxConcurrentEvictions: 5,
			UtilizationThreshold:  40.0,
		},
		AIGate: config.AIGateConfig{
			MaxEvictNodes: 3,
		},
	}
}

func TestConsolidatorPlan_NoCandidates(t *testing.T) {
	cfg := defaultTestConfig()
	consolidator := NewConsolidator(cfg)

	mem16g := int64(16 * 1024 * 1024 * 1024)

	// All nodes are well-utilized (Score < 0.6) -> no candidates.
	snapshot := &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{
			makeNode("node-1", 4000, 3600, mem16g, int64(float64(mem16g)*0.9), 0.20,
				[]*corev1.Pod{makePod("p1", "default", 0, 0)}),
			makeNode("node-2", 4000, 3200, mem16g, int64(float64(mem16g)*0.8), 0.20,
				[]*corev1.Pod{makePod("p2", "default", 0, 0)}),
		},
	}

	scorer := NewFragmentationScorer()
	scores := scorer.Score(snapshot)

	recs, err := consolidator.Plan(snapshot, scores)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("expected 0 recommendations, got %d", len(recs))
	}
}

func TestConsolidatorPlan_OneCandidateFitsElsewhere(t *testing.T) {
	cfg := defaultTestConfig()
	consolidator := NewConsolidator(cfg)

	mem16g := int64(16 * 1024 * 1024 * 1024)

	// node-1: nearly empty (10% CPU, 10% mem) with 1 pod -> candidate
	// node-2: 50% utilized -> not candidate, has plenty of free capacity
	snapshot := &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{
			makeNode("node-1", 4000, 400, mem16g, int64(float64(mem16g)*0.1), 0.20,
				[]*corev1.Pod{makePod("p1", "default", 400, 0)}),
			makeNode("node-2", 4000, 2000, mem16g, int64(float64(mem16g)*0.5), 0.15,
				[]*corev1.Pod{makePod("p2", "default", 2000, 0)}),
		},
	}

	scorer := NewFragmentationScorer()
	scores := scorer.Score(snapshot)

	recs, err := consolidator.Plan(snapshot, scores)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 recommendation, got %d", len(recs))
	}

	rec := recs[0]
	if rec.Type != optimizer.RecommendationEviction {
		t.Errorf("Type = %q, want %q", rec.Type, optimizer.RecommendationEviction)
	}
	if rec.TargetName != "node-1" {
		t.Errorf("TargetName = %q, want %q", rec.TargetName, "node-1")
	}
	if rec.TargetKind != "Node" {
		t.Errorf("TargetKind = %q, want %q", rec.TargetKind, "Node")
	}
	if rec.Priority != optimizer.PriorityMedium {
		t.Errorf("Priority = %q, want %q", rec.Priority, optimizer.PriorityMedium)
	}
}

func TestConsolidatorPlan_CandidateTooBigToFit(t *testing.T) {
	cfg := defaultTestConfig()
	consolidator := NewConsolidator(cfg)

	mem16g := int64(16 * 1024 * 1024 * 1024)

	// node-1: candidate (10% CPU, 10% mem) but its pods request 400m CPU
	// node-2: nearly full (95% CPU, 95% mem) -> barely any free capacity
	snapshot := &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{
			makeNode("node-1", 4000, 400, mem16g, int64(float64(mem16g)*0.1), 0.20,
				[]*corev1.Pod{makePod("p1", "default", 400, 0)}),
			makeNode("node-2", 4000, 3900, mem16g, int64(float64(mem16g)*0.98), 0.20,
				[]*corev1.Pod{makePod("p2", "default", 3900, 0)}),
		},
	}

	scorer := NewFragmentationScorer()
	scores := scorer.Score(snapshot)

	recs, err := consolidator.Plan(snapshot, scores)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// node-1 is candidate but node-2 has only 100m free CPU and ~2% free mem.
	// node-1's pods use 400m CPU which doesn't fit in 100m, so no recommendation.
	if len(recs) != 0 {
		t.Errorf("expected 0 recommendations (pods can't fit elsewhere), got %d", len(recs))
	}
}

func TestConsolidatorPlan_LimitedByMaxConcurrentEvictions(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.Evictor.MaxConcurrentEvictions = 2
	consolidator := NewConsolidator(cfg)

	mem16g := int64(16 * 1024 * 1024 * 1024)

	// 3 candidate nodes (nearly empty) and 1 large non-candidate node with lots of free space.
	snapshot := &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{
			// Candidates: very low utilization (5% CPU, 5% mem)
			makeNode("cand-1", 4000, 200, mem16g, int64(float64(mem16g)*0.05), 0.10,
				[]*corev1.Pod{makePod("p1", "default", 200, 0)}),
			makeNode("cand-2", 4000, 200, mem16g, int64(float64(mem16g)*0.05), 0.10,
				[]*corev1.Pod{makePod("p2", "default", 200, 0)}),
			makeNode("cand-3", 4000, 200, mem16g, int64(float64(mem16g)*0.05), 0.10,
				[]*corev1.Pod{makePod("p3", "default", 200, 0)}),
			// Non-candidate: heavily utilized (80% CPU, 80% mem), but has enough free
			// capacity (800m CPU, 20% mem) to absorb 2 candidates.
			makeNode("receiver", 8000, 6400, 4*mem16g, int64(float64(4*mem16g)*0.8), 0.80,
				[]*corev1.Pod{makePod("pr", "default", 6400, 0)}),
		},
	}

	scorer := NewFragmentationScorer()
	scores := scorer.Score(snapshot)

	recs, err := consolidator.Plan(snapshot, scores)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) > 2 {
		t.Errorf("expected at most 2 recommendations (MaxConcurrentEvictions=2), got %d", len(recs))
	}
}

func TestConsolidatorPlan_SavingsCalculation(t *testing.T) {
	cfg := defaultTestConfig()
	consolidator := NewConsolidator(cfg)

	mem16g := int64(16 * 1024 * 1024 * 1024)
	hourlyCost := 0.20

	// node-cheap: Score ~0.95 -> candidate
	// node-big: 8000 CPU / 6000 req -> cpuFreeRatio=0.25, mem 55% used -> memFreeRatio=0.45
	//   Score = (0.25+0.45)/2 = 0.35 -> NOT a candidate, provides free capacity
	snapshot := &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{
			// Candidate with known hourly cost
			makeNode("node-cheap", 4000, 200, mem16g, int64(float64(mem16g)*0.05), hourlyCost,
				[]*corev1.Pod{makePod("p1", "default", 200, 0)}),
			// Receiver: well-utilized, Score < 0.6
			makeNode("node-big", 8000, 6000, 4*mem16g, int64(float64(4*mem16g)*0.55), 0.80,
				[]*corev1.Pod{makePod("p2", "default", 6000, 0)}),
		},
	}

	scorer := NewFragmentationScorer()
	scores := scorer.Score(snapshot)

	recs, err := consolidator.Plan(snapshot, scores)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 recommendation, got %d", len(recs))
	}

	rec := recs[0]
	expectedMonthlySavings := hourlyCost * cost.HoursPerMonth
	if !approxEqual(rec.EstimatedSaving.MonthlySavingsUSD, expectedMonthlySavings, 0.01) {
		t.Errorf("MonthlySavingsUSD = %.2f, want %.2f",
			rec.EstimatedSaving.MonthlySavingsUSD, expectedMonthlySavings)
	}
	expectedAnnualSavings := hourlyCost * cost.HoursPerMonth * 12
	if !approxEqual(rec.EstimatedSaving.AnnualSavingsUSD, expectedAnnualSavings, 0.01) {
		t.Errorf("AnnualSavingsUSD = %.2f, want %.2f",
			rec.EstimatedSaving.AnnualSavingsUSD, expectedAnnualSavings)
	}
	if rec.EstimatedSaving.Currency != "USD" {
		t.Errorf("Currency = %q, want %q", rec.EstimatedSaving.Currency, "USD")
	}
}

func TestConsolidatorPlan_EmptyNodeNoPods_NotCandidate(t *testing.T) {
	cfg := defaultTestConfig()
	consolidator := NewConsolidator(cfg)

	mem16g := int64(16 * 1024 * 1024 * 1024)

	// An empty node with Score=1.0 but PodCount=0 should NOT be a candidate
	// (nothing to evict).
	snapshot := &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{
			makeNode("node-empty", 4000, 0, mem16g, 0, 0.20, nil),
			makeNode("node-full", 4000, 3600, mem16g, int64(float64(mem16g)*0.9), 0.20,
				[]*corev1.Pod{makePod("p1", "default", 3600, 0)}),
		},
	}

	scorer := NewFragmentationScorer()
	scores := scorer.Score(snapshot)

	recs, err := consolidator.Plan(snapshot, scores)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// node-empty is IsCandidate=true but PodCount=0, so Plan should skip it.
	if len(recs) != 0 {
		t.Errorf("expected 0 recommendations (empty node has no pods), got %d", len(recs))
	}
}

func TestConsolidatorPlan_RecommendationFields(t *testing.T) {
	cfg := defaultTestConfig()
	consolidator := NewConsolidator(cfg)

	mem16g := int64(16 * 1024 * 1024 * 1024)

	// drain-me: Score ~0.95 -> candidate
	// receiver: 8000 CPU / 6000 req -> cpuFreeRatio=0.25, mem 55% -> memFreeRatio=0.45
	//   Score = 0.35 -> NOT candidate
	snapshot := &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{
			makeNode("drain-me", 4000, 300, mem16g, int64(float64(mem16g)*0.05), 0.25,
				[]*corev1.Pod{
					makePod("app-1", "default", 150, 0),
					makePod("app-2", "default", 150, 0),
				}),
			makeNode("receiver", 8000, 6000, 4*mem16g, int64(float64(4*mem16g)*0.55), 0.80,
				[]*corev1.Pod{makePod("p-recv", "default", 6000, 0)}),
		},
	}

	scorer := NewFragmentationScorer()
	scores := scorer.Score(snapshot)

	recs, err := consolidator.Plan(snapshot, scores)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 recommendation, got %d", len(recs))
	}

	rec := recs[0]

	// Verify action steps
	if len(rec.ActionSteps) != 4 {
		t.Errorf("ActionSteps count = %d, want 4", len(rec.ActionSteps))
	}

	// Verify details map
	if rec.Details["nodeName"] != "drain-me" {
		t.Errorf("Details[nodeName] = %q, want %q", rec.Details["nodeName"], "drain-me")
	}
	if rec.Details["podCount"] != "2" {
		t.Errorf("Details[podCount] = %q, want %q", rec.Details["podCount"], "2")
	}

	// Verify impact
	if rec.EstimatedImpact.NodesAffected != 1 {
		t.Errorf("NodesAffected = %d, want 1", rec.EstimatedImpact.NodesAffected)
	}
	if rec.EstimatedImpact.PodsAffected != 2 {
		t.Errorf("PodsAffected = %d, want 2", rec.EstimatedImpact.PodsAffected)
	}
}

func TestConsolidatorPlan_CapacityReducedAfterEachConsolidation(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.Evictor.MaxConcurrentEvictions = 10
	consolidator := NewConsolidator(cfg)

	mem16g := int64(16 * 1024 * 1024 * 1024)

	// Two candidates each using 200m CPU. Receiver has 300m free CPU total.
	// First candidate fits (200m <= 300m), but after that only 100m free,
	// so second candidate (200m) should NOT fit.
	snapshot := &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{
			makeNode("cand-a", 4000, 200, mem16g, int64(float64(mem16g)*0.03), 0.10,
				[]*corev1.Pod{makePod("pa", "default", 200, 0)}),
			makeNode("cand-b", 4000, 200, mem16g, int64(float64(mem16g)*0.03), 0.10,
				[]*corev1.Pod{makePod("pb", "default", 200, 0)}),
			makeNode("receiver", 4000, 3700, mem16g, int64(float64(mem16g)*0.90), 0.50,
				[]*corev1.Pod{makePod("pr", "default", 3700, 0)}),
		},
	}

	scorer := NewFragmentationScorer()
	scores := scorer.Score(snapshot)

	recs, err := consolidator.Plan(snapshot, scores)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != 1 {
		t.Errorf("expected 1 recommendation (only first candidate fits), got %d", len(recs))
	}
}

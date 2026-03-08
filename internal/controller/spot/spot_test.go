package spot

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func spotNode(name string) optimizer.NodeInfo {
	return optimizer.NodeInfo{
		Node: &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:   name,
				Labels: map[string]string{"node.kubernetes.io/lifecycle": "spot"},
			},
		},
		HourlyCostUSD: 0.10,
	}
}

func onDemandNode(name string) optimizer.NodeInfo {
	return optimizer.NodeInfo{
		Node: &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:   name,
				Labels: map[string]string{},
			},
		},
		HourlyCostUSD: 0.30,
	}
}

func gpuNode(name string) optimizer.NodeInfo {
	return optimizer.NodeInfo{
		Node: &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:   name,
				Labels: map[string]string{},
			},
		},
		HourlyCostUSD: 2.50,
		IsGPUNode:     true,
	}
}

func defaultSpotConfig() *config.Config {
	return &config.Config{
		ClusterName: "test-cluster",
		Region:      "us-east-1",
		Spot: config.SpotConfig{
			Enabled:              true,
			MaxSpotPercentage:    70,
			DiversityMinTypes:    3,
			InterruptionHandling: true,
		},
	}
}

// stubProvider implements CloudProvider for unit testing the mixer.
type stubProvider struct{}

func (s *stubProvider) Name() string                                                     { return "stub" }
func (s *stubProvider) GetInstanceTypes(ctx context.Context, region string) ([]*cloudprovider.InstanceType, error) {
	return nil, nil
}
func (s *stubProvider) GetCurrentPricing(ctx context.Context, region string) (*cloudprovider.PricingInfo, error) {
	return nil, nil
}
func (s *stubProvider) GetNodeCost(ctx context.Context, node *corev1.Node) (*cloudprovider.NodeCost, error) {
	return nil, nil
}
func (s *stubProvider) GetGPUInstanceTypes(ctx context.Context, region string) ([]*cloudprovider.GPUInstanceType, error) {
	return nil, nil
}
func (s *stubProvider) GetNodeInstanceType(ctx context.Context, node *corev1.Node) (string, error) {
	return "m5.xlarge", nil
}
func (s *stubProvider) GetNodeRegion(ctx context.Context, node *corev1.Node) (string, error) {
	return "us-east-1", nil
}
func (s *stubProvider) GetNodeZone(ctx context.Context, node *corev1.Node) (string, error) {
	return "us-east-1a", nil
}
func (s *stubProvider) DiscoverNodeGroups(ctx context.Context) ([]*cloudprovider.NodeGroup, error) {
	return nil, nil
}
func (s *stubProvider) GetNodeGroup(ctx context.Context, id string) (*cloudprovider.NodeGroup, error) {
	return nil, nil
}
func (s *stubProvider) ScaleNodeGroup(ctx context.Context, id string, desiredCount int) error {
	return nil
}
func (s *stubProvider) SetNodeGroupMinCount(ctx context.Context, id string, minCount int) error {
	return nil
}
func (s *stubProvider) SetNodeGroupMaxCount(ctx context.Context, id string, maxCount int) error {
	return nil
}
func (s *stubProvider) GetFamilySizes(ctx context.Context, instanceType string) ([]*cloudprovider.InstanceType, error) {
	return nil, nil
}
func (s *stubProvider) GetReservedInstances(ctx context.Context) ([]*cloudprovider.Commitment, error) {
	return nil, nil
}
func (s *stubProvider) GetSavingsPlans(ctx context.Context) ([]*cloudprovider.Commitment, error) {
	return nil, nil
}
func (s *stubProvider) GetCommittedUseDiscounts(ctx context.Context) ([]*cloudprovider.Commitment, error) {
	return nil, nil
}
func (s *stubProvider) GetReservations(ctx context.Context) ([]*cloudprovider.Commitment, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// Mixer Tests
// ---------------------------------------------------------------------------

func TestMixer_EmptyCluster(t *testing.T) {
	m := NewMixer(&stubProvider{}, defaultSpotConfig())
	recs, err := m.Analyze(context.Background(), &optimizer.ClusterSnapshot{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("expected 0 recs for empty cluster, got %d", len(recs))
	}
}

func TestMixer_AllSpotNoRec(t *testing.T) {
	// If all nodes are already spot and above max percentage, no recs
	m := NewMixer(&stubProvider{}, defaultSpotConfig())
	snapshot := &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{
			spotNode("s1"), spotNode("s2"), spotNode("s3"),
		},
	}
	recs, err := m.Analyze(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// All spot at 100%, target is 70%, no need to increase
	for _, r := range recs {
		if r.Details["action"] == "adjust-spot-mix" {
			t.Error("should not recommend increasing spot when already 100%")
		}
	}
}

func TestMixer_BelowTargetRecommendsIncrease(t *testing.T) {
	cfg := defaultSpotConfig()
	cfg.Spot.MaxSpotPercentage = 70
	m := NewMixer(&stubProvider{}, cfg)

	snapshot := &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{
			spotNode("s1"),
			onDemandNode("od1"), onDemandNode("od2"), onDemandNode("od3"),
			onDemandNode("od4"), onDemandNode("od5"), onDemandNode("od6"),
			onDemandNode("od7"), onDemandNode("od8"), onDemandNode("od9"),
		},
	}

	recs, err := m.Analyze(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var found bool
	for _, r := range recs {
		if r.Details["action"] == "adjust-spot-mix" {
			found = true
			if r.AutoExecutable {
				t.Error("spot mix recs should NOT be auto-executable")
			}
			if r.EstimatedSaving.MonthlySavingsUSD <= 0 {
				t.Error("savings should be > 0")
			}
			if r.Type != optimizer.RecommendationSpotOptimize {
				t.Errorf("type = %q, want spot-optimize", r.Type)
			}
		}
	}
	if !found {
		t.Error("expected a spot-mix-increase recommendation")
	}
}

func TestMixer_GPUNodesExcluded(t *testing.T) {
	cfg := defaultSpotConfig()
	cfg.Spot.MaxSpotPercentage = 80
	m := NewMixer(&stubProvider{}, cfg)

	// Only GPU nodes — should not count toward spot calculations
	snapshot := &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{gpuNode("g1"), gpuNode("g2")},
	}

	recs, err := m.Analyze(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, r := range recs {
		if r.Details["action"] == "adjust-spot-mix" {
			t.Error("should not recommend spot changes for GPU-only clusters")
		}
	}
}

func TestMixer_OnDemandNodeGroupConversion(t *testing.T) {
	m := NewMixer(&stubProvider{}, defaultSpotConfig())

	snapshot := &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{onDemandNode("od1"), onDemandNode("od2")},
		NodeGroups: []*cloudprovider.NodeGroup{
			{ID: "ng-1", Name: "workers", Lifecycle: "on-demand", CurrentCount: 5},
		},
	}

	recs, err := m.Analyze(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var found bool
	for _, r := range recs {
		if r.Details["action"] == "convert-to-spot" {
			found = true
			if r.AutoExecutable {
				t.Error("node group conversion recs should NOT be auto-executable")
			}
			if r.TargetKind != "NodeGroup" {
				t.Errorf("TargetKind = %q, want NodeGroup", r.TargetKind)
			}
		}
	}
	if !found {
		t.Error("expected a convert-to-spot recommendation for 100% on-demand node group")
	}
}

func TestMixer_SingleNodeGroupNoConversion(t *testing.T) {
	m := NewMixer(&stubProvider{}, defaultSpotConfig())

	snapshot := &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{onDemandNode("od1")},
		NodeGroups: []*cloudprovider.NodeGroup{
			{ID: "ng-1", Name: "single", Lifecycle: "on-demand", CurrentCount: 1},
		},
	}

	recs, err := m.Analyze(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, r := range recs {
		if r.Details["action"] == "convert-to-spot" && r.Details["nodeGroupID"] == "ng-1" {
			t.Error("should not recommend spot conversion for single-node group")
		}
	}
}

func TestMixer_ExecuteIsNoop(t *testing.T) {
	m := NewMixer(&stubProvider{}, defaultSpotConfig())
	err := m.Execute(context.Background(), optimizer.Recommendation{})
	if err != nil {
		t.Fatalf("Execute should be a no-op, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Diversity Manager Tests
// ---------------------------------------------------------------------------

func TestDiversity_InsufficientTypes(t *testing.T) {
	cfg := defaultSpotConfig()
	cfg.Spot.DiversityMinTypes = 3
	d := NewDiversityManager(&stubProvider{}, cfg)

	snapshot := &optimizer.ClusterSnapshot{
		NodeGroups: []*cloudprovider.NodeGroup{
			{ID: "ng-1", Name: "spot-workers", Lifecycle: "spot", CurrentCount: 5, InstanceTypes: []string{"m5.xlarge"}},
		},
	}

	recs, err := d.Analyze(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 rec, got %d", len(recs))
	}
	if recs[0].Details["action"] != "diversify-spot-types" {
		t.Errorf("action = %q, want diversify-spot-types", recs[0].Details["action"])
	}
	if recs[0].AutoExecutable {
		t.Error("diversity recs should NOT be auto-executable")
	}
	if recs[0].Priority != optimizer.PriorityMedium {
		t.Errorf("priority = %q, want medium", recs[0].Priority)
	}
}

func TestDiversity_SufficientTypes(t *testing.T) {
	cfg := defaultSpotConfig()
	cfg.Spot.DiversityMinTypes = 3
	d := NewDiversityManager(&stubProvider{}, cfg)

	snapshot := &optimizer.ClusterSnapshot{
		NodeGroups: []*cloudprovider.NodeGroup{
			{ID: "ng-1", Name: "spot-workers", Lifecycle: "spot", CurrentCount: 5, InstanceTypes: []string{"m5.xlarge", "m5.2xlarge", "r5.xlarge"}},
		},
	}

	recs, err := d.Analyze(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("expected 0 recs for sufficient diversity, got %d", len(recs))
	}
}

func TestDiversity_OnDemandSkipped(t *testing.T) {
	d := NewDiversityManager(&stubProvider{}, defaultSpotConfig())

	snapshot := &optimizer.ClusterSnapshot{
		NodeGroups: []*cloudprovider.NodeGroup{
			{ID: "ng-1", Name: "workers", Lifecycle: "on-demand", CurrentCount: 5, InstanceTypes: []string{"m5.xlarge"}},
		},
	}

	recs, err := d.Analyze(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("expected 0 recs for on-demand groups, got %d", len(recs))
	}
}

func TestDiversity_SmallGroupSkipped(t *testing.T) {
	d := NewDiversityManager(&stubProvider{}, defaultSpotConfig())

	snapshot := &optimizer.ClusterSnapshot{
		NodeGroups: []*cloudprovider.NodeGroup{
			{ID: "ng-1", Name: "small-spot", Lifecycle: "spot", CurrentCount: 1, InstanceTypes: []string{"m5.xlarge"}},
		},
	}

	recs, err := d.Analyze(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("expected 0 recs for single-node group, got %d", len(recs))
	}
}

func TestDiversity_MixedLifecycleIncluded(t *testing.T) {
	cfg := defaultSpotConfig()
	cfg.Spot.DiversityMinTypes = 3
	d := NewDiversityManager(&stubProvider{}, cfg)

	snapshot := &optimizer.ClusterSnapshot{
		NodeGroups: []*cloudprovider.NodeGroup{
			{ID: "ng-1", Name: "mixed-group", Lifecycle: "mixed", CurrentCount: 4, InstanceTypes: []string{"m5.xlarge"}},
		},
	}

	recs, err := d.Analyze(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 rec for mixed group with low diversity, got %d", len(recs))
	}
}

func TestDiversity_ZeroInstanceTypes(t *testing.T) {
	cfg := defaultSpotConfig()
	cfg.Spot.DiversityMinTypes = 3
	d := NewDiversityManager(&stubProvider{}, cfg)

	// Empty InstanceTypes slice → treated as 1 type
	snapshot := &optimizer.ClusterSnapshot{
		NodeGroups: []*cloudprovider.NodeGroup{
			{ID: "ng-1", Name: "legacy-spot", Lifecycle: "spot", CurrentCount: 3, InstanceTypes: nil},
		},
	}

	recs, err := d.Analyze(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 rec for zero-types group, got %d", len(recs))
	}
	if recs[0].Details["currentTypes"] != "1" {
		t.Errorf("currentTypes = %q, want 1", recs[0].Details["currentTypes"])
	}
}

// ---------------------------------------------------------------------------
// Interruption Handler Tests (Analyze only — Execute requires k8s client)
// ---------------------------------------------------------------------------

func interruptionNode(name string, conditions []corev1.NodeCondition, annotations map[string]string, taints []corev1.Taint) optimizer.NodeInfo {
	return optimizer.NodeInfo{
		Node: &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:        name,
				Labels:      map[string]string{"node.kubernetes.io/lifecycle": "spot"},
				Annotations: annotations,
			},
			Spec: corev1.NodeSpec{
				Taints: taints,
			},
			Status: corev1.NodeStatus{
				Conditions: conditions,
			},
		},
		Pods: []*corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "p1"}}, {ObjectMeta: metav1.ObjectMeta{Name: "p2"}}},
	}
}

func TestInterruption_Disabled(t *testing.T) {
	cfg := defaultSpotConfig()
	cfg.Spot.InterruptionHandling = false
	h := NewInterruptionHandler(nil, &stubProvider{}, cfg)

	recs, err := h.Analyze(context.Background(), &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{interruptionNode("n1",
			[]corev1.NodeCondition{{Type: "TerminationNotice", Status: corev1.ConditionTrue}},
			nil, nil,
		)},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != 0 {
		t.Error("should return 0 recs when interruption handling is disabled")
	}
}

func TestInterruption_AWSTerminationNotice(t *testing.T) {
	h := NewInterruptionHandler(nil, &stubProvider{}, defaultSpotConfig())

	recs, err := h.Analyze(context.Background(), &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{interruptionNode("aws-spot-1",
			[]corev1.NodeCondition{{Type: "TerminationNotice", Status: corev1.ConditionTrue}},
			nil, nil,
		)},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 rec, got %d", len(recs))
	}
	assertInterruptionRec(t, recs[0], "aws-spot-1", 2)
}

func TestInterruption_GCPPreemptionNotice(t *testing.T) {
	h := NewInterruptionHandler(nil, &stubProvider{}, defaultSpotConfig())

	recs, err := h.Analyze(context.Background(), &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{interruptionNode("gcp-spot-1",
			[]corev1.NodeCondition{{Type: "PreemptionNotice", Status: corev1.ConditionTrue}},
			nil, nil,
		)},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 rec, got %d", len(recs))
	}
	assertInterruptionRec(t, recs[0], "gcp-spot-1", 2)
}

func TestInterruption_GCPMaintenanceEvent(t *testing.T) {
	h := NewInterruptionHandler(nil, &stubProvider{}, defaultSpotConfig())

	recs, err := h.Analyze(context.Background(), &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{interruptionNode("gcp-spot-2",
			[]corev1.NodeCondition{{Type: "MaintenanceEvent", Status: corev1.ConditionTrue}},
			nil, nil,
		)},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 rec, got %d", len(recs))
	}
}

func TestInterruption_GCPTerminationTaint(t *testing.T) {
	h := NewInterruptionHandler(nil, &stubProvider{}, defaultSpotConfig())

	recs, err := h.Analyze(context.Background(), &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{interruptionNode("gcp-tainted",
			nil, nil,
			[]corev1.Taint{{Key: "cloud.google.com/impending-node-termination", Effect: corev1.TaintEffectNoSchedule}},
		)},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 rec, got %d", len(recs))
	}
}

func TestInterruption_AzureScheduledEvent(t *testing.T) {
	h := NewInterruptionHandler(nil, &stubProvider{}, defaultSpotConfig())

	recs, err := h.Analyze(context.Background(), &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{interruptionNode("azure-spot-1",
			nil,
			map[string]string{"kubernetes.azure.com/scheduled-event": "Preempt"},
			nil,
		)},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 rec, got %d", len(recs))
	}
	assertInterruptionRec(t, recs[0], "azure-spot-1", 2)
}

func TestInterruption_UniversalAnnotation(t *testing.T) {
	h := NewInterruptionHandler(nil, &stubProvider{}, defaultSpotConfig())

	recs, err := h.Analyze(context.Background(), &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{interruptionNode("custom-spot",
			nil,
			map[string]string{"koptimizer.io/spot-interruption": "true"},
			nil,
		)},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 rec, got %d", len(recs))
	}
}

func TestInterruption_OnDemandNodeSkipped(t *testing.T) {
	h := NewInterruptionHandler(nil, &stubProvider{}, defaultSpotConfig())

	// On-demand node with termination notice — should be skipped
	node := optimizer.NodeInfo{
		Node: &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "od-node",
				Labels: map[string]string{},
			},
			Status: corev1.NodeStatus{
				Conditions: []corev1.NodeCondition{{Type: "TerminationNotice", Status: corev1.ConditionTrue}},
			},
		},
	}
	recs, err := h.Analyze(context.Background(), &optimizer.ClusterSnapshot{Nodes: []optimizer.NodeInfo{node}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != 0 {
		t.Error("should not generate recs for on-demand nodes")
	}
}

func TestInterruption_NoSignalNoRec(t *testing.T) {
	h := NewInterruptionHandler(nil, &stubProvider{}, defaultSpotConfig())

	recs, err := h.Analyze(context.Background(), &optimizer.ClusterSnapshot{
		Nodes: []optimizer.NodeInfo{interruptionNode("healthy-spot", nil, nil, nil)},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != 0 {
		t.Error("should not generate recs for healthy spot nodes")
	}
}

func assertInterruptionRec(t *testing.T, rec optimizer.Recommendation, expectedNode string, expectedPods int) {
	t.Helper()
	if rec.Type != optimizer.RecommendationSpotOptimize {
		t.Errorf("type = %q, want spot-optimize", rec.Type)
	}
	if rec.Priority != optimizer.PriorityCritical {
		t.Errorf("priority = %q, want critical", rec.Priority)
	}
	if !rec.AutoExecutable {
		t.Error("interruption recs MUST be auto-executable")
	}
	if rec.Details["nodeName"] != expectedNode {
		t.Errorf("nodeName = %q, want %q", rec.Details["nodeName"], expectedNode)
	}
	if rec.EstimatedImpact.PodsAffected != expectedPods {
		t.Errorf("podsAffected = %d, want %d", rec.EstimatedImpact.PodsAffected, expectedPods)
	}
}

// ---------------------------------------------------------------------------
// Helper function tests
// ---------------------------------------------------------------------------

func TestIsDaemonSetPod(t *testing.T) {
	tests := []struct {
		name string
		pod  *corev1.Pod
		want bool
	}{
		{
			name: "daemonset pod",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					OwnerReferences: []metav1.OwnerReference{{Kind: "DaemonSet", Name: "fluentd"}},
				},
			},
			want: true,
		},
		{
			name: "deployment pod",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "app-abc"}},
				},
			},
			want: false,
		},
		{
			name: "no owner",
			pod:  &corev1.Pod{},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDaemonSetPod(tt.pod); got != tt.want {
				t.Errorf("isDaemonSetPod() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsMirrorPod(t *testing.T) {
	tests := []struct {
		name string
		pod  *corev1.Pod
		want bool
	}{
		{
			name: "mirror pod",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{"kubernetes.io/config.mirror": "abc"},
				},
			},
			want: true,
		},
		{
			name: "normal pod",
			pod:  &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{}}},
			want: false,
		},
		{
			name: "nil annotations",
			pod:  &corev1.Pod{},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isMirrorPod(tt.pod); got != tt.want {
				t.Errorf("isMirrorPod() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLabelSet(t *testing.T) {
	ls := labelSet(map[string]string{"app": "web", "env": "prod"})
	if !ls.Has("app") {
		t.Error("Has(app) should be true")
	}
	if ls.Has("missing") {
		t.Error("Has(missing) should be false")
	}
	if ls.Get("app") != "web" {
		t.Errorf("Get(app) = %q, want web", ls.Get("app"))
	}
	if ls.Get("missing") != "" {
		t.Errorf("Get(missing) = %q, want empty", ls.Get("missing"))
	}
}

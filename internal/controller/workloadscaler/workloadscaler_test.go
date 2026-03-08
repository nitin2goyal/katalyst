package workloadscaler

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const gi = int64(1024 * 1024 * 1024)
const mi = int64(1024 * 1024)

func scalerConfig() *config.Config {
	return &config.Config{
		WorkloadScaler: config.WorkloadScalerConfig{
			Enabled:           true,
			VerticalEnabled:   true,
			HorizontalEnabled: true,
			SurgeDetection:    true,
			SurgeThreshold:    2.0,
			MaxReplicasLimit:  500,
			ExcludeNamespaces: []string{"kube-system", "monitoring"},
		},
	}
}

func podInfo(name, ns, ownerKind, ownerName string, cpuReq, memReq, cpuUse, memUse int64) optimizer.PodInfo {
	return optimizer.PodInfo{
		Pod: &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		},
		CPURequest:    cpuReq,
		MemoryRequest: memReq,
		CPUUsage:      cpuUse,
		MemoryUsage:   memUse,
		OwnerKind:     ownerKind,
		OwnerName:     ownerName,
	}
}

// ---------------------------------------------------------------------------
// Vertical Scaler - Analyze Tests
// ---------------------------------------------------------------------------

func TestVertical_CPUOverprovisioned(t *testing.T) {
	cfg := scalerConfig()
	v := NewVerticalScaler(nil, cfg)

	snapshot := &optimizer.ClusterSnapshot{
		Pods: []optimizer.PodInfo{
			// 1000m requested, 100m used = 10% utilization (< 30%)
			podInfo("web-1", "prod", "Deployment", "web", 1000, gi, 100, 500*mi),
		},
	}

	recs, err := v.Analyze(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var cpuRec *optimizer.Recommendation
	for i, r := range recs {
		if r.Details["resource"] == "cpu" {
			cpuRec = &recs[i]
		}
	}
	if cpuRec == nil {
		t.Fatal("expected a CPU vertical scaling recommendation")
	}
	if cpuRec.Details["scalingType"] != "vertical" {
		t.Errorf("scalingType = %q, want vertical", cpuRec.Details["scalingType"])
	}
	if !cpuRec.AutoExecutable {
		t.Error("vertical CPU rec should be auto-executable")
	}
}

func TestVertical_MemoryOverprovisioned(t *testing.T) {
	cfg := scalerConfig()
	v := NewVerticalScaler(nil, cfg)

	snapshot := &optimizer.ClusterSnapshot{
		Pods: []optimizer.PodInfo{
			// 1Gi memory requested, 100Mi used = ~10% utilization
			podInfo("db-1", "prod", "StatefulSet", "db", 500, gi, 400, 100*mi),
		},
	}

	recs, err := v.Analyze(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var memRec *optimizer.Recommendation
	for i, r := range recs {
		if r.Details["resource"] == "memory" {
			memRec = &recs[i]
		}
	}
	if memRec == nil {
		t.Fatal("expected a memory vertical scaling recommendation")
	}
	if memRec.Details["scalingType"] != "vertical" {
		t.Errorf("scalingType = %q, want vertical", memRec.Details["scalingType"])
	}
}

func TestVertical_WellSizedNoRec(t *testing.T) {
	v := NewVerticalScaler(nil, scalerConfig())

	snapshot := &optimizer.ClusterSnapshot{
		Pods: []optimizer.PodInfo{
			// 80% CPU utilization — no recommendation
			podInfo("app-1", "prod", "Deployment", "app", 1000, gi, 800, 800*mi),
		},
	}

	recs, err := v.Analyze(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("expected 0 recs for well-sized pod, got %d", len(recs))
	}
}

func TestVertical_SmallPodSkipped(t *testing.T) {
	v := NewVerticalScaler(nil, scalerConfig())

	snapshot := &optimizer.ClusterSnapshot{
		Pods: []optimizer.PodInfo{
			// 50m CPU — below 100m threshold, even though under-utilized
			podInfo("tiny-1", "prod", "Deployment", "tiny", 50, 32*mi, 5, 10*mi),
		},
	}

	recs, err := v.Analyze(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No CPU rec because CPU < 100m threshold
	for _, r := range recs {
		if r.Details["resource"] == "cpu" {
			t.Error("should not recommend CPU reduction for pods with < 100m request")
		}
	}
}

func TestVertical_ZeroCPURequestSkipped(t *testing.T) {
	v := NewVerticalScaler(nil, scalerConfig())

	snapshot := &optimizer.ClusterSnapshot{
		Pods: []optimizer.PodInfo{
			podInfo("no-req", "prod", "Deployment", "noreq", 0, gi, 100, 500*mi),
		},
	}

	recs, err := v.Analyze(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("expected 0 recs for zero CPU request, got %d", len(recs))
	}
}

func TestVertical_ExcludedNamespace(t *testing.T) {
	v := NewVerticalScaler(nil, scalerConfig())

	snapshot := &optimizer.ClusterSnapshot{
		Pods: []optimizer.PodInfo{
			podInfo("sys-1", "kube-system", "DaemonSet", "kube-proxy", 1000, gi, 100, 100*mi),
		},
	}

	recs, err := v.Analyze(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("expected 0 recs for excluded namespace, got %d", len(recs))
	}
}

func TestVertical_CPUFloor(t *testing.T) {
	v := NewVerticalScaler(nil, scalerConfig())

	snapshot := &optimizer.ClusterSnapshot{
		Pods: []optimizer.PodInfo{
			// 500m CPU, 10m usage = 2% utilization
			// suggested = 10 * 1.3 = 13m, clamped to 50m floor
			podInfo("idle-1", "prod", "Deployment", "idle", 500, gi, 10, 100*mi),
		},
	}

	recs, err := v.Analyze(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var cpuRec *optimizer.Recommendation
	for i, r := range recs {
		if r.Details["resource"] == "cpu" {
			cpuRec = &recs[i]
		}
	}
	if cpuRec == nil {
		t.Fatal("expected a CPU rec")
	}
	if cpuRec.Details["suggested"] != "50m" {
		t.Errorf("suggested = %q, want 50m (floor)", cpuRec.Details["suggested"])
	}
}

func TestVertical_MemoryFloor(t *testing.T) {
	v := NewVerticalScaler(nil, scalerConfig())

	snapshot := &optimizer.ClusterSnapshot{
		Pods: []optimizer.PodInfo{
			// 1Gi memory, 10Mi usage = ~1% utilization
			// suggested = 10Mi * 1.3 = 13Mi, clamped to 64Mi floor
			podInfo("idle-mem", "prod", "Deployment", "idlemem", 500, gi, 400, 10*mi),
		},
	}

	recs, err := v.Analyze(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var memRec *optimizer.Recommendation
	for i, r := range recs {
		if r.Details["resource"] == "memory" {
			memRec = &recs[i]
		}
	}
	if memRec == nil {
		t.Fatal("expected a memory rec")
	}
	if memRec.Details["suggested"] != "64Mi" {
		t.Errorf("suggested = %q, want 64Mi (floor)", memRec.Details["suggested"])
	}
}

// ---------------------------------------------------------------------------
// Surge Detector Tests
// ---------------------------------------------------------------------------

func TestSurge_InitialBaselineNoDetection(t *testing.T) {
	sd := NewSurgeDetector(scalerConfig())

	snapshot := &optimizer.ClusterSnapshot{
		Pods: []optimizer.PodInfo{
			podInfo("web-1", "prod", "Deployment", "web", 1000, gi, 500, 500*mi),
		},
	}

	recs, err := sd.Detect(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != 0 {
		t.Error("first observation should establish baseline, not detect surge")
	}
}

func TestSurge_DetectsSpike(t *testing.T) {
	cfg := scalerConfig()
	cfg.WorkloadScaler.SurgeThreshold = 2.0
	sd := NewSurgeDetector(cfg)

	// Establish baseline at 500m
	baseline := &optimizer.ClusterSnapshot{
		Pods: []optimizer.PodInfo{
			podInfo("web-1", "prod", "Deployment", "web", 1000, gi, 500, 500*mi),
		},
	}
	sd.Detect(context.Background(), baseline)

	// Spike to 1500m (3x baseline) — should trigger
	spike := &optimizer.ClusterSnapshot{
		Pods: []optimizer.PodInfo{
			podInfo("web-1", "prod", "Deployment", "web", 1000, gi, 1500, 500*mi),
		},
	}
	recs, err := sd.Detect(context.Background(), spike)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 surge rec, got %d", len(recs))
	}
	if recs[0].Priority != optimizer.PriorityCritical {
		t.Errorf("priority = %q, want critical", recs[0].Priority)
	}
	if !recs[0].AutoExecutable {
		t.Error("surge recs MUST be auto-executable")
	}
	if recs[0].Details["reason"] != "surge" {
		t.Errorf("reason = %q, want surge", recs[0].Details["reason"])
	}
}

func TestSurge_NormalGrowthNoDetection(t *testing.T) {
	cfg := scalerConfig()
	cfg.WorkloadScaler.SurgeThreshold = 2.0
	sd := NewSurgeDetector(cfg)

	// Baseline at 500m
	sd.Detect(context.Background(), &optimizer.ClusterSnapshot{
		Pods: []optimizer.PodInfo{
			podInfo("web-1", "prod", "Deployment", "web", 1000, gi, 500, 500*mi),
		},
	})

	// 50% increase (750m) — below 2x threshold
	recs, _ := sd.Detect(context.Background(), &optimizer.ClusterSnapshot{
		Pods: []optimizer.PodInfo{
			podInfo("web-1", "prod", "Deployment", "web", 1000, gi, 750, 500*mi),
		},
	})
	if len(recs) != 0 {
		t.Error("50% increase should not trigger surge at 2x threshold")
	}
}

func TestSurge_BaselineNotUpdatedDuringSurge(t *testing.T) {
	cfg := scalerConfig()
	cfg.WorkloadScaler.SurgeThreshold = 2.0
	sd := NewSurgeDetector(cfg)

	key := "prod/Deployment/web"

	// Establish baseline at 1000m
	sd.Detect(context.Background(), &optimizer.ClusterSnapshot{
		Pods: []optimizer.PodInfo{
			podInfo("web-1", "prod", "Deployment", "web", 2000, gi, 1000, 500*mi),
		},
	})
	baselineBefore := sd.baselines[key]

	// Surge at 3000m — baseline should NOT update
	sd.Detect(context.Background(), &optimizer.ClusterSnapshot{
		Pods: []optimizer.PodInfo{
			podInfo("web-1", "prod", "Deployment", "web", 2000, gi, 3000, 500*mi),
		},
	})
	baselineAfter := sd.baselines[key]

	if baselineAfter != baselineBefore {
		t.Errorf("baseline changed during surge: before=%.0f, after=%.0f", baselineBefore, baselineAfter)
	}
}

func TestSurge_BaselineUpdatesNormally(t *testing.T) {
	cfg := scalerConfig()
	cfg.WorkloadScaler.SurgeThreshold = 2.0
	sd := NewSurgeDetector(cfg)

	key := "prod/Deployment/web"

	// First observation: baseline = 1000
	sd.Detect(context.Background(), &optimizer.ClusterSnapshot{
		Pods: []optimizer.PodInfo{
			podInfo("web-1", "prod", "Deployment", "web", 2000, gi, 1000, 500*mi),
		},
	})

	// Second observation: 1200m, not a surge. Baseline should EMA update.
	sd.Detect(context.Background(), &optimizer.ClusterSnapshot{
		Pods: []optimizer.PodInfo{
			podInfo("web-1", "prod", "Deployment", "web", 2000, gi, 1200, 500*mi),
		},
	})

	// EMA: 1000 * 0.9 + 1200 * 0.1 = 900 + 120 = 1020
	expected := 1020.0
	if sd.baselines[key] != expected {
		t.Errorf("baseline = %.0f, want %.0f", sd.baselines[key], expected)
	}
}

func TestSurge_MultiplePodsSameOwner(t *testing.T) {
	cfg := scalerConfig()
	cfg.WorkloadScaler.SurgeThreshold = 2.0
	sd := NewSurgeDetector(cfg)

	// Baseline: 2 pods × 500m = 1000m total
	sd.Detect(context.Background(), &optimizer.ClusterSnapshot{
		Pods: []optimizer.PodInfo{
			podInfo("web-1", "prod", "Deployment", "web", 1000, gi, 500, 500*mi),
			podInfo("web-2", "prod", "Deployment", "web", 1000, gi, 500, 500*mi),
		},
	})

	// Surge: 2 pods × 1500m = 3000m total (3x)
	recs, _ := sd.Detect(context.Background(), &optimizer.ClusterSnapshot{
		Pods: []optimizer.PodInfo{
			podInfo("web-1", "prod", "Deployment", "web", 1000, gi, 1500, 500*mi),
			podInfo("web-2", "prod", "Deployment", "web", 1000, gi, 1500, 500*mi),
		},
	})
	if len(recs) != 1 {
		t.Fatalf("expected 1 surge rec for aggregated workload, got %d", len(recs))
	}
}

func TestSurge_OrphanPodSkipped(t *testing.T) {
	sd := NewSurgeDetector(scalerConfig())

	recs, _ := sd.Detect(context.Background(), &optimizer.ClusterSnapshot{
		Pods: []optimizer.PodInfo{
			podInfo("orphan", "prod", "", "", 1000, gi, 1500, 500*mi),
		},
	})
	if len(recs) != 0 {
		t.Error("pods without owner should be skipped")
	}
}

// ---------------------------------------------------------------------------
// Coordinator Tests
// ---------------------------------------------------------------------------

func TestCoordinator_VerticalOnly(t *testing.T) {
	c := NewCoordinator(scalerConfig())

	recs := []optimizer.Recommendation{
		{TargetNamespace: "prod", TargetKind: "Deployment", TargetName: "web", AutoExecutable: true, Details: map[string]string{"scalingType": "vertical"}},
	}

	resolved := c.Resolve(recs)
	if len(resolved) != 1 {
		t.Fatalf("expected 1 rec, got %d", len(resolved))
	}
	if !resolved[0].AutoExecutable {
		t.Error("vertical-only should remain auto-executable")
	}
}

func TestCoordinator_HorizontalOnly(t *testing.T) {
	c := NewCoordinator(scalerConfig())

	recs := []optimizer.Recommendation{
		{TargetNamespace: "prod", TargetKind: "Deployment", TargetName: "web", AutoExecutable: true, Details: map[string]string{"scalingType": "horizontal"}},
	}

	resolved := c.Resolve(recs)
	if len(resolved) != 1 {
		t.Fatalf("expected 1 rec, got %d", len(resolved))
	}
	if !resolved[0].AutoExecutable {
		t.Error("horizontal-only should remain auto-executable")
	}
}

func TestCoordinator_ConflictDefersHorizontal(t *testing.T) {
	c := NewCoordinator(scalerConfig())

	recs := []optimizer.Recommendation{
		{TargetNamespace: "prod", TargetKind: "Deployment", TargetName: "web", AutoExecutable: true, Details: map[string]string{"scalingType": "vertical", "resource": "cpu"}},
		{TargetNamespace: "prod", TargetKind: "Deployment", TargetName: "web", AutoExecutable: true, Details: map[string]string{"scalingType": "horizontal", "hpaName": "web-hpa"}},
	}

	resolved := c.Resolve(recs)
	if len(resolved) != 2 {
		t.Fatalf("expected 2 recs, got %d", len(resolved))
	}

	for _, r := range resolved {
		if r.Details["scalingType"] == "vertical" {
			if !r.AutoExecutable {
				t.Error("vertical rec should remain auto-executable in conflict")
			}
		}
		if r.Details["scalingType"] == "horizontal" {
			if r.AutoExecutable {
				t.Error("horizontal rec should be deferred (auto-executable=false) in conflict")
			}
			if r.Details["deferred"] != "true" {
				t.Error("horizontal rec should have deferred=true marker")
			}
		}
	}
}

func TestCoordinator_DifferentWorkloadsNoConflict(t *testing.T) {
	c := NewCoordinator(scalerConfig())

	recs := []optimizer.Recommendation{
		{TargetNamespace: "prod", TargetKind: "Deployment", TargetName: "web", AutoExecutable: true, Details: map[string]string{"scalingType": "vertical"}},
		{TargetNamespace: "prod", TargetKind: "Deployment", TargetName: "api", AutoExecutable: true, Details: map[string]string{"scalingType": "horizontal"}},
	}

	resolved := c.Resolve(recs)
	if len(resolved) != 2 {
		t.Fatalf("expected 2 recs, got %d", len(resolved))
	}
	for _, r := range resolved {
		if !r.AutoExecutable {
			t.Errorf("rec for %s should remain auto-executable (different workloads)", r.TargetName)
		}
	}
}

func TestCoordinator_EmptyInput(t *testing.T) {
	c := NewCoordinator(scalerConfig())
	resolved := c.Resolve(nil)
	if len(resolved) != 0 {
		t.Errorf("expected 0 recs, got %d", len(resolved))
	}
}

// ---------------------------------------------------------------------------
// buildVerticalPatch Tests
// ---------------------------------------------------------------------------

func TestBuildVerticalPatch_CPU(t *testing.T) {
	containers := []corev1.Container{
		{Name: "app"},
	}
	patch := buildVerticalPatch(containers, "cpu", "500m")
	if patch == nil {
		t.Fatal("expected non-nil patch")
	}
}

func TestBuildVerticalPatch_Memory(t *testing.T) {
	containers := []corev1.Container{
		{Name: "app"},
	}
	patch := buildVerticalPatch(containers, "memory", "256Mi")
	if patch == nil {
		t.Fatal("expected non-nil patch")
	}
}

func TestBuildVerticalPatch_InvalidResource(t *testing.T) {
	patch := buildVerticalPatch([]corev1.Container{{Name: "app"}}, "gpu", "1")
	if patch != nil {
		t.Error("expected nil patch for invalid resource type")
	}
}

func TestBuildVerticalPatch_InvalidQuantity(t *testing.T) {
	patch := buildVerticalPatch([]corev1.Container{{Name: "app"}}, "cpu", "not-a-quantity")
	if patch != nil {
		t.Error("expected nil patch for invalid quantity")
	}
}

func TestBuildVerticalPatch_NoContainers(t *testing.T) {
	patch := buildVerticalPatch(nil, "cpu", "500m")
	if patch != nil {
		t.Error("expected nil patch for empty containers")
	}
}

// ---------------------------------------------------------------------------
// isExcluded Tests
// ---------------------------------------------------------------------------

func TestVertical_IsExcluded(t *testing.T) {
	v := NewVerticalScaler(nil, scalerConfig())
	if !v.isExcluded("kube-system") {
		t.Error("kube-system should be excluded")
	}
	if !v.isExcluded("monitoring") {
		t.Error("monitoring should be excluded")
	}
	if v.isExcluded("prod") {
		t.Error("prod should not be excluded")
	}
}

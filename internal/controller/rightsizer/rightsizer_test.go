package rightsizer

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

func makePodInfo(name, namespace, ownerKind, ownerName string, cpuReq, memReq int64) optimizer.PodInfo {
	return optimizer.PodInfo{
		Pod: &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
		},
		CPURequest:    cpuReq,
		MemoryRequest: memReq,
		OwnerKind:     ownerKind,
		OwnerName:     ownerName,
	}
}

func defaultTestConfig() *config.Config {
	return &config.Config{
		CloudProvider: "gcp",
		Rightsizer: config.RightsizingConfig{
			CPUTargetUtilPct:    95.0,
			MemoryTargetUtilPct: 95.0,
			MinCPURequest:       "10m",
			MinMemoryRequest:    "32Mi",
			MinKeepRatio:        0.7,
		},
	}
}

func approxEqual(a, b, epsilon float64) bool {
	return math.Abs(a-b) < epsilon
}

func findRecByResource(recs []optimizer.Recommendation, resource string) *optimizer.Recommendation {
	for i, r := range recs {
		if r.Details["resource"] == resource {
			return &recs[i]
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Recommender.Recommend tests — Proportional scaling
// ---------------------------------------------------------------------------

func TestRecommender_OverProvisionedCPUOnly_NoRec(t *testing.T) {
	// Only CPU is over-provisioned → no recommendation (requires both)
	cfg := defaultTestConfig()
	recommender := NewRecommender(cfg)

	analysis := &PodAnalysis{
		PodInfo:         makePodInfo("web-1", "default", "Deployment", "web", 1000, 4*1024*1024*1024),
		CPURequestMilli: 1000,
		MemRequestBytes: 4 * 1024 * 1024 * 1024,
		CPUP50:          100,
		CPUP95:          200,
		CPUP99:          300,
		CPUMax:          400,
		IsOverProvCPU:   true,
		IsOverProvMem:   false,
		IsBothOverProv:  false,
		IsUnderProvCPU:  false,
		IsUnderProvMem:  false,
		DataPoints:      1000,
	}

	recs := recommender.Recommend(analysis)

	if len(recs) != 0 {
		t.Fatalf("expected 0 recommendations (only CPU over-prov, no combined rec), got %d", len(recs))
	}
}

func TestRecommender_OverProvisionedMemoryOnly_NoRec(t *testing.T) {
	// Only memory is over-provisioned → no recommendation (requires both)
	cfg := defaultTestConfig()
	recommender := NewRecommender(cfg)

	gi := int64(1024 * 1024 * 1024)

	analysis := &PodAnalysis{
		PodInfo:         makePodInfo("web-1", "default", "Deployment", "web", 500, 4*gi),
		CPURequestMilli: 500,
		MemRequestBytes: 4 * gi,
		MemP50:          500 * 1024 * 1024,
		MemP95:          1 * gi,
		MemP99:          int64(1.5 * float64(gi)),
		MemMax:          2 * gi,
		IsOverProvCPU:   false,
		IsOverProvMem:   true,
		IsBothOverProv:  false,
		IsUnderProvCPU:  false,
		IsUnderProvMem:  false,
		DataPoints:      1000,
	}

	recs := recommender.Recommend(analysis)

	if len(recs) != 0 {
		t.Fatalf("expected 0 recommendations (only mem over-prov, no combined rec), got %d", len(recs))
	}
}

func TestRecommender_BothCPUAndMemoryOverProvisioned(t *testing.T) {
	cfg := defaultTestConfig()
	recommender := NewRecommender(cfg)

	gi := int64(1024 * 1024 * 1024)

	analysis := &PodAnalysis{
		PodInfo:         makePodInfo("batch-1", "jobs", "Deployment", "batch", 2000, 8*gi),
		CPURequestMilli: 2000,
		MemRequestBytes: 8 * gi,
		CPUP50:          100,
		CPUP95:          300,
		CPUP99:          400,
		CPUMax:          500,
		MemP50:          500 * 1024 * 1024,
		MemP95:          1 * gi,
		MemP99:          int64(1.5 * float64(gi)),
		MemMax:          2 * gi,
		IsOverProvCPU:   true,
		IsOverProvMem:   true,
		IsBothOverProv:  true,
		IsUnderProvCPU:  false,
		IsUnderProvMem:  false,
		DataPoints:      2000,
	}

	recs := recommender.Recommend(analysis)

	if len(recs) != 1 {
		t.Fatalf("expected 1 combined recommendation, got %d", len(recs))
	}

	rec := recs[0]
	if rec.Details["resource"] != "cpu+memory" {
		t.Errorf("resource = %q, want %q", rec.Details["resource"], "cpu+memory")
	}
	if rec.Type != optimizer.RecommendationPodRightsize {
		t.Errorf("Type = %q, want %q", rec.Type, optimizer.RecommendationPodRightsize)
	}
	if rec.Priority != optimizer.PriorityMedium {
		t.Errorf("Priority = %q, want %q", rec.Priority, optimizer.PriorityMedium)
	}
	if rec.TargetName != "batch" {
		t.Errorf("TargetName = %q, want %q", rec.TargetName, "batch")
	}

	// Both should be reduced — verify suggested values exist
	if rec.Details["suggestedCPURequest"] == "" {
		t.Error("missing suggestedCPURequest")
	}
	if rec.Details["suggestedMemRequest"] == "" {
		t.Error("missing suggestedMemRequest")
	}

	// keepRatio should be present
	if rec.Details["keepRatio"] == "" {
		t.Error("missing keepRatio in details")
	}
}

func TestRecommender_ProportionalReduction_MinKeepRatioClamped(t *testing.T) {
	// Both resources vastly over-provisioned → keepRatio clamped to MinKeepRatio (0.7)
	cfg := defaultTestConfig()
	cfg.Rightsizer.MinKeepRatio = 0.7
	recommender := NewRecommender(cfg)

	gi := int64(1024 * 1024 * 1024)

	analysis := &PodAnalysis{
		PodInfo:         makePodInfo("huge-1", "prod", "Deployment", "huge", 4000, 32*gi),
		CPURequestMilli: 4000,
		MemRequestBytes: 32 * gi,
		CPUP50:          200,
		CPUP95:          400,  // keep-ratio = (400*1.2)/4000 = 0.12
		CPUP99:          500,
		CPUMax:          600,
		MemP50:          4 * gi,
		MemP95:          6 * gi, // keep-ratio = (6*1.2)/32 = 0.225
		MemP99:          8 * gi,
		MemMax:          10 * gi,
		IsOverProvCPU:   true,
		IsOverProvMem:   true,
		IsBothOverProv:  true,
		CPUUtilRatio:    0.10,
		MemUtilRatio:    0.1875,
		IsUnderProvCPU:  false,
		IsUnderProvMem:  false,
		DataPoints:      5000,
	}

	recs := recommender.Recommend(analysis)

	if len(recs) != 1 {
		t.Fatalf("expected 1 recommendation, got %d", len(recs))
	}

	rec := recs[0]
	// keepRatio should be clamped to 0.7 (both computed ratios < 0.7)
	if rec.Details["keepRatio"] != "0.700" {
		t.Errorf("keepRatio = %q, want %q", rec.Details["keepRatio"], "0.700")
	}

	// CPU: 4000 * 0.7 = 2800m
	if rec.Details["suggestedCPURequest"] != "2800m" {
		t.Errorf("suggestedCPURequest = %q, want %q", rec.Details["suggestedCPURequest"], "2800m")
	}

	// Memory: 32Gi * 0.7 = 22.4Gi → int64 truncation → 22Gi (formatBytes uses integer division)
	suggestedMem := int64(float64(32*gi) * 0.7)
	expectedMem := formatBytes(suggestedMem)
	if rec.Details["suggestedMemRequest"] != expectedMem {
		t.Errorf("suggestedMemRequest = %q, want %q", rec.Details["suggestedMemRequest"], expectedMem)
	}
}

func TestRecommender_ProportionalReduction_CPUDominant(t *testing.T) {
	// CPU has higher keep-ratio than memory → CPU drives both reductions
	cfg := defaultTestConfig()
	cfg.Rightsizer.MinKeepRatio = 0.3 // low floor so we can see natural ratio
	recommender := NewRecommender(cfg)

	gi := int64(1024 * 1024 * 1024)

	// CPU: P95=600m, request=1000m → keepRatio = (600*1.2)/1000 = 0.72
	// Mem: P95=1Gi, request=8Gi → keepRatio = (1*1.2)/8 = 0.15
	// max(0.72, 0.15) = 0.72 → CPU drives
	analysis := &PodAnalysis{
		PodInfo:         makePodInfo("svc-1", "prod", "Deployment", "svc", 1000, 8*gi),
		CPURequestMilli: 1000,
		MemRequestBytes: 8 * gi,
		CPUP50:          400,
		CPUP95:          600,
		CPUP99:          700,
		CPUMax:          800,
		MemP50:          512 * 1024 * 1024,
		MemP95:          1 * gi,
		MemP99:          int64(1.5 * float64(gi)),
		MemMax:          2 * gi,
		IsOverProvCPU:   true,
		IsOverProvMem:   true,
		IsBothOverProv:  true,
		IsUnderProvCPU:  false,
		IsUnderProvMem:  false,
		DataPoints:      1000,
	}

	recs := recommender.Recommend(analysis)

	if len(recs) != 1 {
		t.Fatalf("expected 1 recommendation, got %d", len(recs))
	}

	rec := recs[0]
	if rec.Details["keepRatio"] != "0.720" {
		t.Errorf("keepRatio = %q, want %q (CPU should drive)", rec.Details["keepRatio"], "0.720")
	}

	// CPU: 1000 * 0.72 = 720m
	if rec.Details["suggestedCPURequest"] != "720m" {
		t.Errorf("suggestedCPURequest = %q, want %q", rec.Details["suggestedCPURequest"], "720m")
	}

	// Memory: 8Gi * 0.72 = 5.76Gi → formatBytes → 5Gi (integer division)
	suggestedMem := int64(float64(8*gi) * 0.72)
	expectedMem := formatBytes(suggestedMem)
	if rec.Details["suggestedMemRequest"] != expectedMem {
		t.Errorf("suggestedMemRequest = %q, want %q", rec.Details["suggestedMemRequest"], expectedMem)
	}
}

func TestRecommender_ProportionalReduction_MemDominant(t *testing.T) {
	// Memory has higher keep-ratio than CPU → memory drives both reductions
	cfg := defaultTestConfig()
	cfg.Rightsizer.MinKeepRatio = 0.3 // low floor
	recommender := NewRecommender(cfg)

	gi := int64(1024 * 1024 * 1024)

	// CPU: P95=100m, request=2000m → keepRatio = (100*1.2)/2000 = 0.06
	// Mem: P95=5Gi, request=8Gi → keepRatio = (5*1.2)/8 = 0.75
	// max(0.06, 0.75) = 0.75 → Memory drives
	analysis := &PodAnalysis{
		PodInfo:         makePodInfo("worker-1", "prod", "Deployment", "worker", 2000, 8*gi),
		CPURequestMilli: 2000,
		MemRequestBytes: 8 * gi,
		CPUP50:          50,
		CPUP95:          100,
		CPUP99:          150,
		CPUMax:          200,
		MemP50:          3 * gi,
		MemP95:          5 * gi,
		MemP99:          6 * gi,
		MemMax:          7 * gi,
		IsOverProvCPU:   true,
		IsOverProvMem:   true,
		IsBothOverProv:  true,
		IsUnderProvCPU:  false,
		IsUnderProvMem:  false,
		DataPoints:      1000,
	}

	recs := recommender.Recommend(analysis)

	if len(recs) != 1 {
		t.Fatalf("expected 1 recommendation, got %d", len(recs))
	}

	rec := recs[0]
	if rec.Details["keepRatio"] != "0.750" {
		t.Errorf("keepRatio = %q, want %q (memory should drive)", rec.Details["keepRatio"], "0.750")
	}

	// CPU: 2000 * 0.75 = 1500m
	if rec.Details["suggestedCPURequest"] != "1500m" {
		t.Errorf("suggestedCPURequest = %q, want %q", rec.Details["suggestedCPURequest"], "1500m")
	}

	// Memory: 8Gi * 0.75 = 6Gi
	suggestedMem := int64(float64(8*gi) * 0.75)
	expectedMem := formatBytes(suggestedMem)
	if rec.Details["suggestedMemRequest"] != expectedMem {
		t.Errorf("suggestedMemRequest = %q, want %q", rec.Details["suggestedMemRequest"], expectedMem)
	}
}

func TestRecommender_ProportionalMinimumFloors(t *testing.T) {
	// Both resources so low that minimums (10m CPU, 32Mi memory) kick in
	cfg := defaultTestConfig()
	cfg.Rightsizer.MinKeepRatio = 0.01 // very low to test floors
	recommender := NewRecommender(cfg)

	mi := int64(1024 * 1024)

	analysis := &PodAnalysis{
		PodInfo:         makePodInfo("micro-1", "default", "Deployment", "micro", 100, 256*mi),
		CPURequestMilli: 100,
		MemRequestBytes: 256 * mi,
		CPUP50:          1,
		CPUP95:          2,   // keepRatio = (2*1.2)/100 = 0.024
		CPUP99:          3,
		CPUMax:          5,
		MemP50:          5 * mi,
		MemP95:          8 * mi, // keepRatio = (8*1.2)/256 = 0.0375
		MemP99:          10 * mi,
		MemMax:          12 * mi,
		IsOverProvCPU:   true,
		IsOverProvMem:   true,
		IsBothOverProv:  true,
		IsUnderProvCPU:  false,
		IsUnderProvMem:  false,
		DataPoints:      500,
	}

	recs := recommender.Recommend(analysis)

	if len(recs) != 1 {
		t.Fatalf("expected 1 recommendation, got %d", len(recs))
	}

	rec := recs[0]
	// With keepRatio = max(0.024, 0.0375) = 0.0375, clamped to 0.01 → stays 0.0375
	// suggestedCPU = 100 * 0.0375 = 3m → clamped to 10m
	if rec.Details["suggestedCPURequest"] != "10m" {
		t.Errorf("suggestedCPURequest = %q, want %q (floor)", rec.Details["suggestedCPURequest"], "10m")
	}

	// suggestedMem = 256Mi * 0.0375 = 9.6Mi → clamped to 32Mi
	if rec.Details["suggestedMemRequest"] != "32Mi" {
		t.Errorf("suggestedMemRequest = %q, want %q (floor)", rec.Details["suggestedMemRequest"], "32Mi")
	}
}

func TestRecommender_CombinedSavingsEstimate(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.Rightsizer.MinKeepRatio = 0.7
	recommender := NewRecommender(cfg)

	gi := int64(1024 * 1024 * 1024)

	analysis := &PodAnalysis{
		PodInfo:         makePodInfo("app-1", "prod", "Deployment", "app", 4000, 32*gi),
		CPURequestMilli: 4000,
		MemRequestBytes: 32 * gi,
		CPUP50:          200,
		CPUP95:          400,  // keepRatio = 0.12, clamped to 0.7
		CPUP99:          500,
		CPUMax:          600,
		MemP50:          4 * gi,
		MemP95:          6 * gi, // keepRatio = 0.225, clamped to 0.7
		MemP99:          8 * gi,
		MemMax:          10 * gi,
		IsOverProvCPU:   true,
		IsOverProvMem:   true,
		IsBothOverProv:  true,
		IsUnderProvCPU:  false,
		IsUnderProvMem:  false,
		DataPoints:      5000,
	}

	recs := recommender.Recommend(analysis)

	if len(recs) != 1 {
		t.Fatalf("expected 1 recommendation, got %d", len(recs))
	}

	rec := recs[0]

	// CPU: 4000 → 2800 (save 1200m = 1.2 vCPU)
	cpuSavings := 1.2 * 0.031611 * cost.HoursPerMonth
	// Memory: 32Gi → 22.4Gi (save ~9.6Gi)
	suggestedMem := int64(float64(32*gi) * 0.7)
	memSavedGiB := float64(32*gi-suggestedMem) / float64(gi)
	memSavings := memSavedGiB * 0.004237 * cost.HoursPerMonth

	expectedSavings := cpuSavings + memSavings

	if !approxEqual(rec.EstimatedSaving.MonthlySavingsUSD, expectedSavings, 1.0) {
		t.Errorf("MonthlySavingsUSD = %.2f, want ~%.2f (combined CPU+memory)",
			rec.EstimatedSaving.MonthlySavingsUSD, expectedSavings)
	}

	// Annual = monthly * 12
	if !approxEqual(rec.EstimatedSaving.AnnualSavingsUSD, expectedSavings*12, 12.0) {
		t.Errorf("AnnualSavingsUSD = %.2f, want ~%.2f",
			rec.EstimatedSaving.AnnualSavingsUSD, expectedSavings*12)
	}
}

// ---------------------------------------------------------------------------
// Existing behavior tests (unchanged)
// ---------------------------------------------------------------------------

func TestRecommender_UnderProvisionedCPU(t *testing.T) {
	cfg := defaultTestConfig()
	recommender := NewRecommender(cfg)

	analysis := &PodAnalysis{
		PodInfo:         makePodInfo("api-1", "prod", "Deployment", "api", 100, 1024*1024*1024),
		CPURequestMilli: 100,
		MemRequestBytes: 1024 * 1024 * 1024,
		CPUP50:          200,
		CPUP95:          400,
		CPUP99:          450,
		CPUMax:          500,
		IsOverProvCPU:   false,
		IsOverProvMem:   false,
		IsBothOverProv:  false,
		IsUnderProvCPU:  true,
		IsUnderProvMem:  false,
		DataPoints:      500,
	}

	recs := recommender.Recommend(analysis)

	if len(recs) != 1 {
		t.Fatalf("expected 1 recommendation, got %d", len(recs))
	}

	rec := recs[0]
	if rec.Type != optimizer.RecommendationPodRightsize {
		t.Errorf("Type = %q, want %q", rec.Type, optimizer.RecommendationPodRightsize)
	}
	if rec.Priority != optimizer.PriorityHigh {
		t.Errorf("Priority = %q, want %q (under-provisioned should be high)", rec.Priority, optimizer.PriorityHigh)
	}

	// Suggested = CPUMax * 1.3 = 500 * 1.3 = 650m
	if rec.Details["suggestedRequest"] != "650m" {
		t.Errorf("suggestedRequest = %q, want %q", rec.Details["suggestedRequest"], "650m")
	}
	if rec.Details["resource"] != "cpu" {
		t.Errorf("resource = %q, want %q", rec.Details["resource"], "cpu")
	}
}

func TestRecommender_UnderProvisionedMemory_NoRecommendation(t *testing.T) {
	// The current Recommender does not handle IsUnderProvMem, so it should
	// produce no recommendations when only memory is under-provisioned.
	cfg := defaultTestConfig()
	recommender := NewRecommender(cfg)

	analysis := &PodAnalysis{
		PodInfo:         makePodInfo("worker-1", "prod", "StatefulSet", "worker", 500, 512*1024*1024),
		CPURequestMilli: 500,
		MemRequestBytes: 512 * 1024 * 1024,
		MemP50:          400 * 1024 * 1024,
		MemP95:          500 * 1024 * 1024,
		MemP99:          510 * 1024 * 1024,
		MemMax:          520 * 1024 * 1024,
		IsOverProvCPU:   false,
		IsOverProvMem:   false,
		IsBothOverProv:  false,
		IsUnderProvCPU:  false,
		IsUnderProvMem:  true,
		DataPoints:      800,
	}

	recs := recommender.Recommend(analysis)

	if len(recs) != 0 {
		t.Errorf("expected 0 recommendations for under-provisioned memory (not yet implemented), got %d", len(recs))
	}
}

func TestRecommender_NeitherOverNorUnderProvisioned(t *testing.T) {
	cfg := defaultTestConfig()
	recommender := NewRecommender(cfg)

	analysis := &PodAnalysis{
		PodInfo:         makePodInfo("balanced-1", "default", "Deployment", "balanced", 500, 1024*1024*1024),
		CPURequestMilli: 500,
		MemRequestBytes: 1024 * 1024 * 1024,
		CPUP50:          300,
		CPUP95:          400,
		CPUP99:          450,
		CPUMax:          480,
		MemP50:          600 * 1024 * 1024,
		MemP95:          800 * 1024 * 1024,
		MemP99:          900 * 1024 * 1024,
		MemMax:          950 * 1024 * 1024,
		IsOverProvCPU:   false,
		IsOverProvMem:   false,
		IsBothOverProv:  false,
		IsUnderProvCPU:  false,
		IsUnderProvMem:  false,
		DataPoints:      1000,
	}

	recs := recommender.Recommend(analysis)

	if len(recs) != 0 {
		t.Errorf("expected 0 recommendations for well-provisioned pod, got %d", len(recs))
	}
}

func TestRecommender_ZeroP95_NoRecommendation(t *testing.T) {
	cfg := defaultTestConfig()
	recommender := NewRecommender(cfg)

	// P95 = 0 means we have no meaningful data.
	analysis := &PodAnalysis{
		PodInfo:         makePodInfo("no-data", "default", "Deployment", "no-data", 500, 1024*1024*1024),
		CPURequestMilli: 500,
		MemRequestBytes: 1024 * 1024 * 1024,
		CPUP50:          0,
		CPUP95:          0,
		CPUP99:          0,
		CPUMax:          0,
		MemP50:          0,
		MemP95:          0,
		MemP99:          0,
		MemMax:          0,
		IsOverProvCPU:   true,
		IsOverProvMem:   true,
		IsBothOverProv:  true,
		IsUnderProvCPU:  false,
		IsUnderProvMem:  false,
		DataPoints:      0,
	}

	recs := recommender.Recommend(analysis)

	if len(recs) != 0 {
		t.Errorf("expected 0 recommendations when P95 = 0, got %d", len(recs))
	}
}

func TestRecommender_TargetFieldsPopulated(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.Rightsizer.MinKeepRatio = 0.3 // low floor to allow combined rec
	recommender := NewRecommender(cfg)

	gi := int64(1024 * 1024 * 1024)

	analysis := &PodAnalysis{
		PodInfo:         makePodInfo("svc-pod", "staging", "StatefulSet", "my-svc", 1000, 4*gi),
		CPURequestMilli: 1000,
		MemRequestBytes: 4 * gi,
		CPUP50:          100,
		CPUP95:          200,
		CPUP99:          250,
		CPUMax:          300,
		MemP50:          512 * 1024 * 1024,
		MemP95:          1 * gi,
		MemP99:          int64(1.5 * float64(gi)),
		MemMax:          2 * gi,
		IsOverProvCPU:   true,
		IsOverProvMem:   true,
		IsBothOverProv:  true,
		IsUnderProvCPU:  false,
		IsUnderProvMem:  false,
		DataPoints:      1000,
	}

	recs := recommender.Recommend(analysis)

	if len(recs) != 1 {
		t.Fatalf("expected 1 recommendation, got %d", len(recs))
	}

	rec := recs[0]
	if rec.TargetKind != "StatefulSet" {
		t.Errorf("TargetKind = %q, want %q", rec.TargetKind, "StatefulSet")
	}
	if rec.TargetName != "my-svc" {
		t.Errorf("TargetName = %q, want %q", rec.TargetName, "my-svc")
	}
	if rec.TargetNamespace != "staging" {
		t.Errorf("TargetNamespace = %q, want %q", rec.TargetNamespace, "staging")
	}
	if !rec.AutoExecutable {
		t.Error("AutoExecutable should be true")
	}
	if rec.Details["resource"] != "cpu+memory" {
		t.Errorf("resource = %q, want %q", rec.Details["resource"], "cpu+memory")
	}
}

// ---------------------------------------------------------------------------
// Controller health filter tests
// ---------------------------------------------------------------------------

func TestAllContainersReady(t *testing.T) {
	tests := []struct {
		name     string
		pod      *corev1.Pod
		expected bool
	}{
		{
			name: "all ready",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{Ready: true},
						{Ready: true},
					},
				},
			},
			expected: true,
		},
		{
			name: "one not ready",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{Ready: true},
						{Ready: false},
					},
				},
			},
			expected: false,
		},
		{
			name: "no container statuses",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := allContainersReady(tt.pod)
			if got != tt.expected {
				t.Errorf("allContainersReady() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Savings estimate helper tests
// ---------------------------------------------------------------------------

func TestRecommender_CPUSavingsEstimate_GCP(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.Rightsizer.MinKeepRatio = 0.3
	recommender := NewRecommender(cfg)

	gi := int64(1024 * 1024 * 1024)

	// CPU: P95=200, request=1000 → keepRatio = (200*1.2)/1000 = 0.24
	// Mem: P95=1Gi, request=4Gi → keepRatio = (1*1.2)/4 = 0.30
	// max(0.24, 0.30) = 0.30
	analysis := &PodAnalysis{
		PodInfo:         makePodInfo("svc-1", "prod", "Deployment", "svc", 1000, 4*gi),
		CPURequestMilli: 1000,
		MemRequestBytes: 4 * gi,
		CPUP50:          100,
		CPUP95:          200,
		CPUP99:          300,
		CPUMax:          400,
		MemP50:          512 * 1024 * 1024,
		MemP95:          1 * gi,
		MemP99:          int64(1.5 * float64(gi)),
		MemMax:          2 * gi,
		IsOverProvCPU:   true,
		IsOverProvMem:   true,
		IsBothOverProv:  true,
		IsUnderProvCPU:  false,
		IsUnderProvMem:  false,
		DataPoints:      1000,
	}

	recs := recommender.Recommend(analysis)
	if len(recs) != 1 {
		t.Fatalf("expected 1 recommendation, got %d", len(recs))
	}

	rec := recs[0]
	// Savings should be positive and include both CPU and memory components
	if rec.EstimatedSaving.MonthlySavingsUSD <= 0 {
		t.Errorf("MonthlySavingsUSD = %.2f, expected positive combined savings",
			rec.EstimatedSaving.MonthlySavingsUSD)
	}
	if rec.EstimatedSaving.AnnualSavingsUSD <= 0 {
		t.Errorf("AnnualSavingsUSD = %.2f, expected positive", rec.EstimatedSaving.AnnualSavingsUSD)
	}
}

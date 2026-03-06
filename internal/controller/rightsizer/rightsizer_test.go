package rightsizer

import (
	"math"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/koptimizer/koptimizer/internal/config"
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

// ---------------------------------------------------------------------------
// CPU upsize tests — CPU upsizes are NEVER generated
// ---------------------------------------------------------------------------

func TestRecommender_UnderProvisionedCPU_NeverUpsize(t *testing.T) {
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

	if len(recs) != 0 {
		t.Fatalf("expected 0 recommendations (CPU upsizes are never generated), got %d", len(recs))
	}
}

// ---------------------------------------------------------------------------
// Downsize tests — node-ratio-aware
// ---------------------------------------------------------------------------

func TestRecommender_CPUOverProv_GeneratesRec(t *testing.T) {
	// CPU over-provisioned alone should now generate a recommendation
	cfg := defaultTestConfig()
	recommender := NewRecommender(cfg)

	gi := int64(1024 * 1024 * 1024)

	analysis := &PodAnalysis{
		PodInfo:         makePodInfo("web-1", "default", "Deployment", "web", 1000, 8*gi),
		CPURequestMilli: 1000,
		MemRequestBytes: 8 * gi,
		CPUP50:          100,
		CPUP95:          200,
		CPUP99:          300,
		CPUMax:          400,
		MemP95:          2 * gi,
		IsOverProvCPU:   true,
		IsOverProvMem:   false,
		IsBothOverProv:  false,
		IsUnderProvCPU:  false,
		IsUnderProvMem:  false,
		DataPoints:      1000,
	}

	recs := recommender.Recommend(analysis)

	if len(recs) != 1 {
		t.Fatalf("expected 1 recommendation (CPU over-prov), got %d", len(recs))
	}
	if recs[0].Details["resource"] != "cpu+memory" {
		t.Errorf("resource = %q, want %q", recs[0].Details["resource"], "cpu+memory")
	}
}

func TestRecommender_OverProvisionedMemoryOnly_NoRec(t *testing.T) {
	// Only memory is over-provisioned → no recommendation (requires CPU over-prov)
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
		t.Fatalf("expected 0 recommendations (only mem over-prov), got %d", len(recs))
	}
}

func TestRecommender_BothOverProv_WithNodeRatio(t *testing.T) {
	cfg := defaultTestConfig()
	recommender := NewRecommender(cfg)

	gi := int64(1024 * 1024 * 1024)

	// Node: 32000m CPU, 128Gi memory → 4Mi per millicore
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
		NodeCPUCapMilli: 32000,
		NodeMemCapBytes: 128 * gi,
	}

	recs := recommender.Recommend(analysis)

	if len(recs) != 1 {
		t.Fatalf("expected 1 recommendation, got %d", len(recs))
	}

	rec := recs[0]
	if rec.Details["resource"] != "cpu+memory" {
		t.Errorf("resource = %q, want %q", rec.Details["resource"], "cpu+memory")
	}
	if rec.Type != optimizer.RecommendationPodRightsize {
		t.Errorf("Type = %q, want %q", rec.Type, optimizer.RecommendationPodRightsize)
	}

	// CPU: minKeepRatio=0.7 → max(P95*1.2=360, 2000*0.7=1400) = 1400m
	if rec.Details["suggestedCPURequest"] != "1400m" {
		t.Errorf("suggestedCPURequest = %q, want %q", rec.Details["suggestedCPURequest"], "1400m")
	}

	// Memory: node ratio = 128Gi/32000m = 4Mi/m → 1400m * 4Mi = 5600Mi = 5Gi (formatBytes)
	// bytesPerMilli = 128*gi / 32000 = 4294967.296
	// suggestedMem = 1400 * 4294967.296 = 6012954214 ≈ 5Gi
	if rec.Details["suggestedMemRequest"] != "5Gi" {
		t.Errorf("suggestedMemRequest = %q, want %q", rec.Details["suggestedMemRequest"], "5Gi")
	}
}

func TestRecommender_NodeRatio_MemoryNeverIncreases(t *testing.T) {
	// Pod has too much CPU and not enough memory relative to node ratio.
	// CPU should decrease. Memory would increase to match node ratio,
	// but we fall back to proportional reduction instead.
	cfg := defaultTestConfig()
	cfg.Rightsizer.MinKeepRatio = 0.3
	recommender := NewRecommender(cfg)

	gi := int64(1024 * 1024 * 1024)

	// Node: 32000m CPU, 128Gi memory → 4Mi per millicore
	// Pod: 4000m CPU, 4Gi memory → CPU way over-provisioned
	// CPU P95=200m → floor = max(200*1.2=240, 4000*0.3=1200) = 1200m
	// Node-ratio memory for 1200m = 1200 * 4Mi = 4800Mi ≈ 4.7Gi
	// 4.7Gi > current 4Gi → proportional fallback: 4Gi * (1200/4000) = 1.2Gi
	// But memFloor = 1.5Gi * 1.2 = 1.8Gi → clamp to 1.8Gi (formatBytes → "1Gi")
	// memSaved = 4Gi - 1.8Gi = 2.2Gi > 2Gi ✓
	analysis := &PodAnalysis{
		PodInfo:         makePodInfo("skewed-1", "prod", "Deployment", "skewed", 4000, 4*gi),
		CPURequestMilli: 4000,
		MemRequestBytes: 4 * gi,
		CPUP50:          100,
		CPUP95:          200,
		CPUP99:          300,
		CPUMax:          400,
		MemP50:          1 * gi,
		MemP95:          int64(1.5 * float64(gi)),
		MemP99:          int64(1.8 * float64(gi)),
		MemMax:          2 * gi,
		IsOverProvCPU:   true,
		IsOverProvMem:   false,
		IsBothOverProv:  false,
		IsUnderProvCPU:  false,
		IsUnderProvMem:  false,
		DataPoints:      1000,
		NodeCPUCapMilli: 32000,
		NodeMemCapBytes: 128 * gi,
	}

	recs := recommender.Recommend(analysis)

	if len(recs) != 1 {
		t.Fatalf("expected 1 recommendation, got %d", len(recs))
	}

	rec := recs[0]
	// CPU should decrease
	if rec.Details["suggestedCPURequest"] != "1200m" {
		t.Errorf("suggestedCPURequest = %q, want %q", rec.Details["suggestedCPURequest"], "1200m")
	}

	// Memory: node ratio says 4.7Gi but current is 4Gi → proportional fallback
	// gives 1.2Gi, clamped to memFloor (1.8Gi) → formatBytes → "1Gi"
	suggestedMem := rec.Details["suggestedMemRequest"]
	if suggestedMem != "1Gi" {
		t.Errorf("suggestedMemRequest = %q, want %q (proportional fallback on highmem node)", suggestedMem, "1Gi")
	}
}

func TestRecommender_NoNodeInfo_FallsBackToProportional(t *testing.T) {
	// No node capacity info → falls back to proportional reduction
	cfg := defaultTestConfig()
	cfg.Rightsizer.MinKeepRatio = 0.7
	recommender := NewRecommender(cfg)

	gi := int64(1024 * 1024 * 1024)

	analysis := &PodAnalysis{
		PodInfo:         makePodInfo("app-1", "prod", "Deployment", "app", 4000, 32*gi),
		CPURequestMilli: 4000,
		MemRequestBytes: 32 * gi,
		CPUP50:          200,
		CPUP95:          400,
		CPUP99:          500,
		CPUMax:          600,
		MemP50:          4 * gi,
		MemP95:          6 * gi,
		MemP99:          8 * gi,
		MemMax:          10 * gi,
		IsOverProvCPU:   true,
		IsOverProvMem:   true,
		IsBothOverProv:  true,
		IsUnderProvCPU:  false,
		IsUnderProvMem:  false,
		DataPoints:      5000,
		// No node info
		NodeCPUCapMilli: 0,
		NodeMemCapBytes: 0,
	}

	recs := recommender.Recommend(analysis)

	if len(recs) != 1 {
		t.Fatalf("expected 1 recommendation, got %d", len(recs))
	}

	rec := recs[0]
	// CPU: max(400*1.2=480, 4000*0.7=2800) = 2800m
	if rec.Details["suggestedCPURequest"] != "2800m" {
		t.Errorf("suggestedCPURequest = %q, want %q", rec.Details["suggestedCPURequest"], "2800m")
	}

	// Memory: proportional = 32Gi * (2800/4000) = 32Gi * 0.7 = 22.4Gi
	// memFloor = max(6Gi*1.2=7.2Gi, 32Mi) = 7.2Gi
	// 22.4Gi > 7.2Gi → use 22.4Gi → formatBytes → 22Gi
	suggestedMem := int64(float64(32*gi) * (2800.0 / 4000.0))
	expectedMem := formatBytes(suggestedMem)
	if rec.Details["suggestedMemRequest"] != expectedMem {
		t.Errorf("suggestedMemRequest = %q, want %q", rec.Details["suggestedMemRequest"], expectedMem)
	}
}

func TestRecommender_MinKeepRatioClamped(t *testing.T) {
	// Both resources vastly over-provisioned → CPU clamped to MinKeepRatio
	cfg := defaultTestConfig()
	cfg.Rightsizer.MinKeepRatio = 0.7
	recommender := NewRecommender(cfg)

	gi := int64(1024 * 1024 * 1024)

	analysis := &PodAnalysis{
		PodInfo:         makePodInfo("huge-1", "prod", "Deployment", "huge", 4000, 32*gi),
		CPURequestMilli: 4000,
		MemRequestBytes: 32 * gi,
		CPUP50:          200,
		CPUP95:          400,  // cpuFloor = max(480, 2800) = 2800
		CPUP99:          500,
		CPUMax:          600,
		MemP50:          4 * gi,
		MemP95:          6 * gi,
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
	// CPU: max(400*1.2=480, 4000*0.7=2800) = 2800m
	if rec.Details["suggestedCPURequest"] != "2800m" {
		t.Errorf("suggestedCPURequest = %q, want %q", rec.Details["suggestedCPURequest"], "2800m")
	}
}

func TestRecommender_CPUFloor10m(t *testing.T) {
	// Very low usage → CPU clamped to 10m minimum
	// Uses 4Gi memory to exceed the 2Gi minimum for rightsizing
	cfg := defaultTestConfig()
	cfg.Rightsizer.MinKeepRatio = 0.01
	recommender := NewRecommender(cfg)

	gi := int64(1024 * 1024 * 1024)
	mi := int64(1024 * 1024)

	analysis := &PodAnalysis{
		PodInfo:         makePodInfo("micro-1", "default", "Deployment", "micro", 100, 4*gi),
		CPURequestMilli: 100,
		MemRequestBytes: 4 * gi,
		CPUP50:          1,
		CPUP95:          2,
		CPUP99:          3,
		CPUMax:          5,
		MemP50:          500 * mi,
		MemP95:          800 * mi,
		MemP99:          1 * gi,
		MemMax:          int64(1.2 * float64(gi)),
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
	// CPU: max(2*1.2=2, 100*0.01=1) = 2 → clamped to 10m
	if rec.Details["suggestedCPURequest"] != "10m" {
		t.Errorf("suggestedCPURequest = %q, want %q (floor)", rec.Details["suggestedCPURequest"], "10m")
	}
}

func TestRecommender_NeverIncreaseCPU(t *testing.T) {
	// CPU usage is high enough that the floor exceeds current request → no rec
	cfg := defaultTestConfig()
	recommender := NewRecommender(cfg)

	gi := int64(1024 * 1024 * 1024)

	analysis := &PodAnalysis{
		PodInfo:         makePodInfo("tight-1", "prod", "Deployment", "tight", 500, 4*gi),
		CPURequestMilli: 500,
		MemRequestBytes: 4 * gi,
		CPUP50:          350,
		CPUP95:          450, // floor = max(450*1.2=540, 500*0.7=350) = 540 > 500
		CPUP99:          480,
		CPUMax:          490,
		MemP50:          1 * gi,
		MemP95:          2 * gi,
		MemP99:          3 * gi,
		MemMax:          int64(3.5 * float64(gi)),
		IsOverProvCPU:   true, // analyzer says over-prov but usage is borderline
		IsOverProvMem:   true,
		IsBothOverProv:  true,
		IsUnderProvCPU:  false,
		IsUnderProvMem:  false,
		DataPoints:      1000,
	}

	recs := recommender.Recommend(analysis)

	if len(recs) != 0 {
		t.Fatalf("expected 0 recommendations (CPU floor exceeds current), got %d", len(recs))
	}
}

func TestRecommender_SavingsEstimate(t *testing.T) {
	cfg := defaultTestConfig()
	recommender := NewRecommender(cfg)

	gi := int64(1024 * 1024 * 1024)

	analysis := &PodAnalysis{
		PodInfo:         makePodInfo("svc-1", "prod", "Deployment", "svc", 1000, 8*gi),
		CPURequestMilli: 1000,
		MemRequestBytes: 8 * gi,
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
	if rec.EstimatedSaving.MonthlySavingsUSD <= 0 {
		t.Errorf("MonthlySavingsUSD = %.2f, expected positive", rec.EstimatedSaving.MonthlySavingsUSD)
	}
	if rec.EstimatedSaving.AnnualSavingsUSD <= 0 {
		t.Errorf("AnnualSavingsUSD = %.2f, expected positive", rec.EstimatedSaving.AnnualSavingsUSD)
	}
}

func TestRecommender_TargetFieldsPopulated(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.Rightsizer.MinKeepRatio = 0.3
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

func TestRecommender_DaemonSetSkipped(t *testing.T) {
	cfg := defaultTestConfig()
	recommender := NewRecommender(cfg)

	gi := int64(1024 * 1024 * 1024)

	analysis := &PodAnalysis{
		PodInfo:         makePodInfo("ds-1", "kube-system", "DaemonSet", "my-ds", 1000, 4*gi),
		CPURequestMilli: 1000,
		MemRequestBytes: 4 * gi,
		CPUP95:          100,
		MemP95:          1 * gi,
		IsOverProvCPU:   true,
		IsOverProvMem:   true,
		IsBothOverProv:  true,
		DataPoints:      1000,
	}

	recs := recommender.Recommend(analysis)
	if len(recs) != 0 {
		t.Errorf("expected 0 recommendations for DaemonSet, got %d", len(recs))
	}
}

func TestRecommender_InsufficientDataPoints(t *testing.T) {
	cfg := defaultTestConfig()
	recommender := NewRecommender(cfg)

	analysis := &PodAnalysis{
		PodInfo:         makePodInfo("new-1", "default", "Deployment", "new", 1000, 1024*1024*1024),
		CPURequestMilli: 1000,
		MemRequestBytes: 1024 * 1024 * 1024,
		CPUP95:          100,
		MemP95:          100 * 1024 * 1024,
		IsOverProvCPU:   true,
		IsBothOverProv:  true,
		DataPoints:      3, // < 6
	}

	recs := recommender.Recommend(analysis)
	if len(recs) != 0 {
		t.Errorf("expected 0 recommendations for insufficient data, got %d", len(recs))
	}
}

func TestRecommender_ZeroP95_NoRecommendation(t *testing.T) {
	cfg := defaultTestConfig()
	recommender := NewRecommender(cfg)

	analysis := &PodAnalysis{
		PodInfo:         makePodInfo("no-data", "default", "Deployment", "no-data", 500, 1024*1024*1024),
		CPURequestMilli: 500,
		MemRequestBytes: 1024 * 1024 * 1024,
		CPUP95:          0,
		MemP95:          0,
		IsOverProvCPU:   true,
		IsBothOverProv:  true,
		DataPoints:      0,
	}

	recs := recommender.Recommend(analysis)
	if len(recs) != 0 {
		t.Errorf("expected 0 recommendations when P95 = 0, got %d", len(recs))
	}
}

func TestRecommender_WellProvisioned_NoRec(t *testing.T) {
	cfg := defaultTestConfig()
	recommender := NewRecommender(cfg)

	analysis := &PodAnalysis{
		PodInfo:         makePodInfo("ok-1", "default", "Deployment", "ok", 500, 1024*1024*1024),
		CPURequestMilli: 500,
		MemRequestBytes: 1024 * 1024 * 1024,
		CPUP95:          400,
		MemP95:          800 * 1024 * 1024,
		IsOverProvCPU:   false,
		IsOverProvMem:   false,
		IsBothOverProv:  false,
		DataPoints:      1000,
	}

	recs := recommender.Recommend(analysis)
	if len(recs) != 0 {
		t.Errorf("expected 0 recommendations for well-provisioned pod, got %d", len(recs))
	}
}

func TestRecommender_UnderProvisionedMemory_NoRec(t *testing.T) {
	cfg := defaultTestConfig()
	recommender := NewRecommender(cfg)

	analysis := &PodAnalysis{
		PodInfo:         makePodInfo("worker-1", "prod", "StatefulSet", "worker", 500, 512*1024*1024),
		CPURequestMilli: 500,
		MemRequestBytes: 512 * 1024 * 1024,
		MemP95:          500 * 1024 * 1024,
		IsOverProvCPU:   false,
		IsUnderProvMem:  true,
		DataPoints:      800,
	}

	recs := recommender.Recommend(analysis)
	if len(recs) != 0 {
		t.Errorf("expected 0 recommendations for under-provisioned memory only, got %d", len(recs))
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

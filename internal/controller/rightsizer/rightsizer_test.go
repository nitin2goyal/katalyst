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
			CPUTargetUtilPct:    70.0,
			MemoryTargetUtilPct: 75.0,
			MinCPURequest:       "10m",
			MinMemoryRequest:    "32Mi",
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
// Recommender.Recommend tests
// ---------------------------------------------------------------------------

func TestRecommender_OverProvisionedCPU(t *testing.T) {
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
		IsUnderProvCPU:  false,
		IsUnderProvMem:  false,
		DataPoints:      1000,
	}

	recs := recommender.Recommend(analysis)

	if len(recs) != 1 {
		t.Fatalf("expected 1 recommendation, got %d", len(recs))
	}

	rec := recs[0]
	if rec.Type != optimizer.RecommendationPodRightsize {
		t.Errorf("Type = %q, want %q", rec.Type, optimizer.RecommendationPodRightsize)
	}
	if rec.Priority != optimizer.PriorityMedium {
		t.Errorf("Priority = %q, want %q", rec.Priority, optimizer.PriorityMedium)
	}

	// Suggested = P95 * 1.2 = 200 * 1.2 = 240m
	if rec.Details["suggestedRequest"] != "240m" {
		t.Errorf("suggestedRequest = %q, want %q", rec.Details["suggestedRequest"], "240m")
	}
	if rec.Details["resource"] != "cpu" {
		t.Errorf("resource = %q, want %q", rec.Details["resource"], "cpu")
	}
	if rec.TargetName != "web" {
		t.Errorf("TargetName = %q, want %q", rec.TargetName, "web")
	}
	if rec.TargetNamespace != "default" {
		t.Errorf("TargetNamespace = %q, want %q", rec.TargetNamespace, "default")
	}
}

func TestRecommender_OverProvisionedMemory(t *testing.T) {
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
		IsUnderProvCPU:  false,
		IsUnderProvMem:  false,
		DataPoints:      1000,
	}

	recs := recommender.Recommend(analysis)

	if len(recs) != 1 {
		t.Fatalf("expected 1 recommendation, got %d", len(recs))
	}

	rec := recs[0]
	if rec.Type != optimizer.RecommendationPodRightsize {
		t.Errorf("Type = %q, want %q", rec.Type, optimizer.RecommendationPodRightsize)
	}
	if rec.Priority != optimizer.PriorityMedium {
		t.Errorf("Priority = %q, want %q", rec.Priority, optimizer.PriorityMedium)
	}
	if rec.Details["resource"] != "memory" {
		t.Errorf("resource = %q, want %q", rec.Details["resource"], "memory")
	}

	// Suggested = P95 * 1.2 = 1Gi * 1.2 = 1.2Gi.
	// The suggested value should be less than the current 4Gi request.
	// The formatBytes function will show "1Gi" for int64(1.2*Gi) because of integer truncation.
	suggestedBytes := int64(float64(1*gi) * 1.2)
	expectedFormatted := formatBytes(suggestedBytes)
	if rec.Details["suggestedRequest"] != expectedFormatted {
		t.Errorf("suggestedRequest = %q, want %q", rec.Details["suggestedRequest"], expectedFormatted)
	}
}

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
		IsUnderProvCPU:  false,
		IsUnderProvMem:  true,
		DataPoints:      800,
	}

	recs := recommender.Recommend(analysis)

	if len(recs) != 0 {
		t.Errorf("expected 0 recommendations for under-provisioned memory (not yet implemented), got %d", len(recs))
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
		IsUnderProvCPU:  false,
		IsUnderProvMem:  false,
		DataPoints:      2000,
	}

	recs := recommender.Recommend(analysis)

	if len(recs) != 2 {
		t.Fatalf("expected 2 recommendations (CPU + Memory), got %d", len(recs))
	}

	cpuRec := findRecByResource(recs, "cpu")
	memRec := findRecByResource(recs, "memory")

	if cpuRec == nil {
		t.Fatal("missing CPU recommendation")
	}
	if memRec == nil {
		t.Fatal("missing Memory recommendation")
	}

	// CPU: suggested = 300 * 1.2 = 360m
	if cpuRec.Details["suggestedRequest"] != "360m" {
		t.Errorf("CPU suggestedRequest = %q, want %q", cpuRec.Details["suggestedRequest"], "360m")
	}

	// Memory: suggested = 1Gi * 1.2 = 1.2Gi
	suggestedMem := int64(float64(1*gi) * 1.2)
	expectedFormatted := formatBytes(suggestedMem)
	if memRec.Details["suggestedRequest"] != expectedFormatted {
		t.Errorf("Memory suggestedRequest = %q, want %q", memRec.Details["suggestedRequest"], expectedFormatted)
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
		IsUnderProvCPU:  false,
		IsUnderProvMem:  false,
		DataPoints:      1000,
	}

	recs := recommender.Recommend(analysis)

	if len(recs) != 0 {
		t.Errorf("expected 0 recommendations for well-provisioned pod, got %d", len(recs))
	}
}

func TestRecommender_MinCPUFloor(t *testing.T) {
	cfg := defaultTestConfig()
	recommender := NewRecommender(cfg)

	// P95 = 5m. Suggested = 5 * 1.2 = 6m, which is below the 10m floor.
	// The recommender should clamp to 10m.
	analysis := &PodAnalysis{
		PodInfo:         makePodInfo("tiny-1", "default", "Deployment", "tiny", 100, 128*1024*1024),
		CPURequestMilli: 100,
		MemRequestBytes: 128 * 1024 * 1024,
		CPUP50:          2,
		CPUP95:          5,
		CPUP99:          8,
		CPUMax:          10,
		IsOverProvCPU:   true,
		IsOverProvMem:   false,
		IsUnderProvCPU:  false,
		IsUnderProvMem:  false,
		DataPoints:      500,
	}

	recs := recommender.Recommend(analysis)

	if len(recs) != 1 {
		t.Fatalf("expected 1 recommendation, got %d", len(recs))
	}

	rec := recs[0]
	// Floor: max(P95*1.2, 10) = max(6, 10) = 10m
	if rec.Details["suggestedRequest"] != "10m" {
		t.Errorf("suggestedRequest = %q, want %q (minimum floor)", rec.Details["suggestedRequest"], "10m")
	}
}

func TestRecommender_OverProvCPU_SuggestedNotLessThanRequest_NoRec(t *testing.T) {
	// When P95 * 1.2 >= current request, no recommendation should be made
	// even if IsOverProvCPU is true.
	cfg := defaultTestConfig()
	recommender := NewRecommender(cfg)

	analysis := &PodAnalysis{
		PodInfo:         makePodInfo("edge-1", "default", "Deployment", "edge", 100, 128*1024*1024),
		CPURequestMilli: 100,
		MemRequestBytes: 128 * 1024 * 1024,
		CPUP50:          60,
		CPUP95:          90,  // 90 * 1.2 = 108 >= 100, so no reduction possible
		CPUP99:          95,
		CPUMax:          98,
		IsOverProvCPU:   true,
		IsOverProvMem:   false,
		IsUnderProvCPU:  false,
		IsUnderProvMem:  false,
		DataPoints:      500,
	}

	recs := recommender.Recommend(analysis)

	if len(recs) != 0 {
		t.Errorf("expected 0 recommendations (suggested >= current), got %d", len(recs))
	}
}

func TestRecommender_CPUSavingsEstimate(t *testing.T) {
	tests := []struct {
		name          string
		cloudProvider string
		cpuRequest    int64
		cpuP95        int64
		wantSavings   float64
	}{
		{
			name:          "GCP savings",
			cloudProvider: "gcp",
			cpuRequest:    1000,
			cpuP95:        200,
			// suggested = 240m, saved = (1000-240)/1000 = 0.76 vCPU
			// savings = 0.76 * 0.031611 * cost.HoursPerMonth
			wantSavings: 0.76 * 0.031611 * cost.HoursPerMonth,
		},
		{
			name:          "AWS savings",
			cloudProvider: "aws",
			cpuRequest:    1000,
			cpuP95:        200,
			wantSavings:   0.76 * 0.04 * cost.HoursPerMonth,
		},
		{
			name:          "Azure savings",
			cloudProvider: "azure",
			cpuRequest:    1000,
			cpuP95:        200,
			wantSavings:   0.76 * 0.043 * cost.HoursPerMonth,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := defaultTestConfig()
			cfg.CloudProvider = tt.cloudProvider
			recommender := NewRecommender(cfg)

			analysis := &PodAnalysis{
				PodInfo:         makePodInfo("svc-1", "prod", "Deployment", "svc", tt.cpuRequest, 1024*1024*1024),
				CPURequestMilli: tt.cpuRequest,
				MemRequestBytes: 1024 * 1024 * 1024,
				CPUP50:          100,
				CPUP95:          tt.cpuP95,
				CPUP99:          300,
				CPUMax:          400,
				IsOverProvCPU:   true,
				IsOverProvMem:   false,
				IsUnderProvCPU:  false,
				IsUnderProvMem:  false,
				DataPoints:      1000,
			}

			recs := recommender.Recommend(analysis)

			if len(recs) != 1 {
				t.Fatalf("expected 1 recommendation, got %d", len(recs))
			}

			if !approxEqual(recs[0].EstimatedSaving.MonthlySavingsUSD, tt.wantSavings, 0.5) {
				t.Errorf("MonthlySavingsUSD = %.2f, want ~%.2f",
					recs[0].EstimatedSaving.MonthlySavingsUSD, tt.wantSavings)
			}
		})
	}
}

func TestRecommender_MinMemoryFloor(t *testing.T) {
	cfg := defaultTestConfig()
	recommender := NewRecommender(cfg)

	// P95 memory = 10Mi. Suggested = 10Mi * 1.2 = 12Mi, below 32Mi floor.
	// The recommender should clamp to 32Mi.
	mi := int64(1024 * 1024)

	analysis := &PodAnalysis{
		PodInfo:         makePodInfo("tiny-mem", "default", "Deployment", "tiny-mem", 100, 256*mi),
		CPURequestMilli: 100,
		MemRequestBytes: 256 * mi,
		MemP50:          5 * mi,
		MemP95:          10 * mi,
		MemP99:          15 * mi,
		MemMax:          20 * mi,
		IsOverProvCPU:   false,
		IsOverProvMem:   true,
		IsUnderProvCPU:  false,
		IsUnderProvMem:  false,
		DataPoints:      500,
	}

	recs := recommender.Recommend(analysis)

	if len(recs) != 1 {
		t.Fatalf("expected 1 recommendation, got %d", len(recs))
	}

	rec := recs[0]
	// Floor: max(P95*1.2, 32Mi) = max(12Mi, 32Mi) = 32Mi
	if rec.Details["suggestedRequest"] != "32Mi" {
		t.Errorf("suggestedRequest = %q, want %q (minimum floor)", rec.Details["suggestedRequest"], "32Mi")
	}
}

func TestRecommender_ZeroP95_NoRecommendation(t *testing.T) {
	cfg := defaultTestConfig()
	recommender := NewRecommender(cfg)

	// P95 = 0 means we have no meaningful data, so even with IsOverProvCPU=true
	// the recommender should skip (guarded by CPUP95 > 0 check).
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
	recommender := NewRecommender(cfg)

	analysis := &PodAnalysis{
		PodInfo:         makePodInfo("svc-pod", "staging", "StatefulSet", "my-svc", 1000, 4*1024*1024*1024),
		CPURequestMilli: 1000,
		MemRequestBytes: 4 * 1024 * 1024 * 1024,
		CPUP50:          100,
		CPUP95:          200,
		CPUP99:          250,
		CPUMax:          300,
		IsOverProvCPU:   true,
		IsOverProvMem:   false,
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
}

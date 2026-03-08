package rightsizer

import (
	"fmt"
	"strconv"
	"strings"
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

func podInfo(name, ns, ownerKind, ownerName string, cpuReq, memReq int64) optimizer.PodInfo {
	return optimizer.PodInfo{
		Pod: &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		},
		CPURequest:    cpuReq,
		MemoryRequest: memReq,
		OwnerKind:     ownerKind,
		OwnerName:     ownerName,
	}
}

func defaultCfg() *config.Config {
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

// parseMilli parses "1400m" → 1400.
func parseMilli(s string) int64 {
	s = strings.TrimSuffix(s, "m")
	v, _ := strconv.ParseInt(s, 10, 64)
	return v
}

// parseMemBytes parses "5734Mi" → bytes, "8Gi" → bytes.
func parseMemBytes(s string) int64 {
	if strings.HasSuffix(s, "Gi") {
		v, _ := strconv.ParseInt(strings.TrimSuffix(s, "Gi"), 10, 64)
		return v * gi
	}
	if strings.HasSuffix(s, "Mi") {
		v, _ := strconv.ParseInt(strings.TrimSuffix(s, "Mi"), 10, 64)
		return v * mi
	}
	if strings.HasSuffix(s, "Ki") {
		v, _ := strconv.ParseInt(strings.TrimSuffix(s, "Ki"), 10, 64)
		return v * 1024
	}
	v, _ := strconv.ParseInt(s, 10, 64)
	return v
}

// assertInvariants checks every returned recommendation against safety
// invariants. Downsize recs (resource == "cpu+memory", no "direction" key)
// are checked against all downsize invariants. Upsize recs (direction ==
// "upsize") are checked against upsize-specific invariants.
func assertInvariants(t *testing.T, recs []optimizer.Recommendation, analysis *PodAnalysis) {
	t.Helper()
	for i, rec := range recs {
		prefix := fmt.Sprintf("rec[%d]", i)

		if rec.Details["direction"] == "upsize" {
			// Upsize invariants
			if rec.AutoExecutable {
				t.Errorf("%s: upsize rec MUST have AutoExecutable=false", prefix)
			}
			continue
		}

		// Downsize invariants below

		// INVARIANT: resource must be "cpu+memory" (ensures isDownsizeRec works)
		if rec.Details["resource"] != "cpu+memory" {
			t.Errorf("%s: resource = %q, MUST be 'cpu+memory'", prefix, rec.Details["resource"])
		}

		// INVARIANT: savings must be positive
		if rec.EstimatedSaving.MonthlySavingsUSD <= 0 {
			t.Errorf("%s: monthly savings = %.2f, MUST be > 0", prefix, rec.EstimatedSaving.MonthlySavingsUSD)
		}

		sugCPU := parseMilli(rec.Details["suggestedCPURequest"])
		sugMem := parseMemBytes(rec.Details["suggestedMemRequest"])

		// INVARIANT: CPU must decrease
		if sugCPU >= analysis.CPURequestMilli {
			t.Errorf("%s: CPU NOT DECREASING: %dm -> %dm", prefix, analysis.CPURequestMilli, sugCPU)
		}

		// INVARIANT: CPU must be >= 1 CPU floor
		if sugCPU < MinCPUFloorMilli {
			t.Errorf("%s: CPU %dm BELOW %dm floor", prefix, sugCPU, MinCPUFloorMilli)
		}

		// INVARIANT: memory must not increase
		if sugMem > analysis.MemRequestBytes {
			t.Errorf("%s: MEMORY INCREASED: %s -> %s",
				prefix, formatBytes(analysis.MemRequestBytes), rec.Details["suggestedMemRequest"])
		}

		// INVARIANT: memory reduction must be >= 2 GiB
		memDelta := analysis.MemRequestBytes - sugMem
		if memDelta < MinMemDeltaBytes {
			t.Errorf("%s: memory delta %s BELOW %s minimum",
				prefix, formatBytes(memDelta), formatBytes(MinMemDeltaBytes))
		}
	}
}

// ---------------------------------------------------------------------------
// Safety invariants — these MUST hold regardless of input
// ---------------------------------------------------------------------------

func TestRecommend_SafetyInvariants(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *config.Config
		analysis *PodAnalysis
		wantN    int
	}{
		// --- Never increase CPU ---
		{
			name: "under-provisioned CPU: no upsize recs generated",
			analysis: &PodAnalysis{
				PodInfo:         podInfo("a", "prod", "Deployment", "api", 100, gi),
				CPURequestMilli: 100, MemRequestBytes: gi,
				CPUP95: 400, CPUMax: 500,
				IsOverProvCPU: false, IsUnderProvCPU: true,
				DataPoints: 500,
			},
			wantN: 0,
		},
		{
			name: "CPU P95 near request: floor exceeds current",
			analysis: &PodAnalysis{
				PodInfo:         podInfo("a", "prod", "Deployment", "tight", 1500, 8*gi),
				CPURequestMilli: 1500, MemRequestBytes: 8 * gi,
				CPUP95: 1300, MemP95: 2 * gi, // floor = max(1300*1.2=1560, 1500*0.7=1050) = 1560 > 1500
				IsOverProvCPU: true,
				DataPoints: 1000,
			},
			wantN: 0,
		},
		{
			name: "CPU P95 above request: usage exceeds request",
			analysis: &PodAnalysis{
				PodInfo:         podInfo("a", "prod", "Deployment", "hot", 1500, 8*gi),
				CPURequestMilli: 1500, MemRequestBytes: 8 * gi,
				CPUP95: 1800, MemP95: 2 * gi, // floor = max(1800*1.2=2160, 1500*0.7=1050) = 2160 > 1500
				IsOverProvCPU: true,
				DataPoints: 1000,
			},
			wantN: 0,
		},

		// --- Never increase memory ---
		{
			name: "node ratio wants more memory than current: capped at current, delta < 2GiB",
			analysis: &PodAnalysis{
				PodInfo:         podInfo("a", "prod", "Deployment", "skewed", 4000, 4*gi),
				CPURequestMilli: 4000, MemRequestBytes: 4 * gi,
				CPUP95: 200, MemP95: gi, // ratio wants ~5.6GiB > 4GiB → capped → delta=0
				IsOverProvCPU:   true,
				DataPoints:      1000,
				NodeCPUCapMilli: 32000, NodeMemCapBytes: 128 * gi,
			},
			wantN: 0,
		},
		{
			name: "under-provisioned memory only: no upsize recs generated",
			analysis: &PodAnalysis{
				PodInfo:         podInfo("a", "prod", "StatefulSet", "worker", 500, 512*mi),
				CPURequestMilli: 500, MemRequestBytes: 512 * mi,
				MemP95:         500 * mi,
				IsOverProvCPU:  false, IsUnderProvMem: true,
				DataPoints: 800,
			},
			wantN: 0,
		},

		// --- 1 CPU (1000m) floor ---
		{
			name: "pod with 100m CPU: below 1 CPU floor",
			analysis: &PodAnalysis{
				PodInfo:         podInfo("a", "ns", "Deployment", "micro", 100, 4*gi),
				CPURequestMilli: 100, MemRequestBytes: 4 * gi,
				CPUP95: 2, MemP95: 500 * mi,
				IsOverProvCPU: true, IsBothOverProv: true,
				DataPoints: 500,
			},
			wantN: 0,
		},
		{
			name: "pod with 500m CPU: below 1 CPU floor",
			analysis: &PodAnalysis{
				PodInfo:         podInfo("a", "ns", "Deployment", "small", 500, 4*gi),
				CPURequestMilli: 500, MemRequestBytes: 4 * gi,
				CPUP95: 50, MemP95: 500 * mi,
				IsOverProvCPU: true,
				DataPoints: 500,
			},
			wantN: 0,
		},
		{
			name: "pod with exactly 1000m CPU: floor equals request",
			analysis: &PodAnalysis{
				PodInfo:         podInfo("a", "ns", "Deployment", "exact", 1000, 8*gi),
				CPURequestMilli: 1000, MemRequestBytes: 8 * gi,
				CPUP95: 100, MemP95: gi,
				IsOverProvCPU: true,
				DataPoints: 1000,
			},
			wantN: 0,
		},

		// --- 2 GiB minimum memory delta ---
		{
			name: "memory delta 0: CPU changes but memory stays same",
			analysis: &PodAnalysis{
				PodInfo:         podInfo("a", "ns", "Deployment", "cpuonly", 4000, 4*gi),
				CPURequestMilli: 4000, MemRequestBytes: 4 * gi,
				CPUP95: 200, MemP95: 3 * gi, // proportional mem = 2.8Gi, floor = 3.6Gi, cap = 4Gi; delta < 2Gi
				IsOverProvCPU: true,
				DataPoints: 1000,
			},
			wantN: 0,
		},
		{
			name: "memory delta 1 GiB: below 2 GiB minimum",
			analysis: &PodAnalysis{
				PodInfo:         podInfo("a", "ns", "Deployment", "smalldelta", 2000, 8*gi),
				CPURequestMilli: 2000, MemRequestBytes: 8 * gi,
				CPUP95: 200, MemP95: 6 * gi, // proportional mem = 5.6Gi, floor = 7.2Gi; delta = 8Gi - 7.2Gi = 0.8Gi
				IsOverProvCPU: true,
				DataPoints: 1000,
			},
			wantN: 0,
		},

		// --- DaemonSet ---
		{
			name: "DaemonSet always skipped regardless of over-provisioning",
			analysis: &PodAnalysis{
				PodInfo:         podInfo("a", "kube-system", "DaemonSet", "my-ds", 4000, 16*gi),
				CPURequestMilli: 4000, MemRequestBytes: 16 * gi,
				CPUP95: 100, MemP95: gi,
				IsOverProvCPU: true, IsBothOverProv: true,
				DataPoints: 1000,
			},
			wantN: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg
			if cfg == nil {
				cfg = defaultCfg()
			}
			recs := NewRecommender(cfg).Recommend(tt.analysis)
			assertInvariants(t, recs, tt.analysis)
			if len(recs) != tt.wantN {
				t.Fatalf("got %d recs, want %d", len(recs), tt.wantN)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Gating conditions — recs only when preconditions are met
// ---------------------------------------------------------------------------

func TestRecommend_GatingConditions(t *testing.T) {
	tests := []struct {
		name     string
		analysis *PodAnalysis
		wantN    int
	}{
		{
			name: "insufficient data points (5 < 6)",
			analysis: &PodAnalysis{
				PodInfo:         podInfo("a", "ns", "Deployment", "new", 2000, 8*gi),
				CPURequestMilli: 2000, MemRequestBytes: 8 * gi,
				CPUP95: 200, MemP95: gi,
				IsOverProvCPU: true,
				DataPoints: 5,
			},
			wantN: 0,
		},
		{
			name: "exactly 6 data points: sufficient",
			analysis: &PodAnalysis{
				PodInfo:         podInfo("a", "ns", "Deployment", "ok", 2000, 8*gi),
				CPURequestMilli: 2000, MemRequestBytes: 8 * gi,
				CPUP95: 200, MemP95: gi,
				IsOverProvCPU: true,
				DataPoints: 6,
			},
			wantN: 1,
		},
		{
			name: "zero CPUP95: no rec (gated by CPUP95 > 0)",
			analysis: &PodAnalysis{
				PodInfo:         podInfo("a", "ns", "Deployment", "nodata", 2000, 8*gi),
				CPURequestMilli: 2000, MemRequestBytes: 8 * gi,
				CPUP95: 0,
				IsOverProvCPU: true,
				DataPoints: 1000,
			},
			wantN: 0,
		},
		{
			name: "CPU not over-provisioned: no rec",
			analysis: &PodAnalysis{
				PodInfo:         podInfo("a", "ns", "Deployment", "wellsized", 2000, 8*gi),
				CPURequestMilli: 2000, MemRequestBytes: 8 * gi,
				CPUP95: 1800, MemP95: 6 * gi,
				IsOverProvCPU: false,
				DataPoints: 1000,
			},
			wantN: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recs := NewRecommender(defaultCfg()).Recommend(tt.analysis)
			assertInvariants(t, recs, tt.analysis)
			if len(recs) != tt.wantN {
				t.Fatalf("got %d recs, want %d", len(recs), tt.wantN)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Algorithm correctness — verify computed CPU/memory targets
// ---------------------------------------------------------------------------

func TestRecommend_AlgorithmCorrectness(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *config.Config
		analysis *PodAnalysis
		wantCPU string // expected suggestedCPURequest
		wantMem string // expected suggestedMemRequest
	}{
		{
			name: "node ratio: minKeepRatio clamps CPU, ratio sets memory",
			// CPU: max(300*1.2=360, 2000*0.7=1400, 1000) = 1400m
			// Mem: 1400 * (128Gi/32000) = 5734Mi
			analysis: &PodAnalysis{
				PodInfo:         podInfo("a", "jobs", "Deployment", "batch", 2000, 8*gi),
				CPURequestMilli: 2000, MemRequestBytes: 8 * gi,
				CPUP95: 300, MemP95: gi,
				IsOverProvCPU: true,
				DataPoints:      2000,
				NodeCPUCapMilli: 32000, NodeMemCapBytes: 128 * gi,
			},
			wantCPU: "1400m",
			wantMem: "5734Mi",
		},
		{
			name: "proportional fallback: no node info",
			// CPU: max(400*1.2=480, 4000*0.7=2800, 1000) = 2800m
			// Mem: 32Gi * (2800/4000) = 22.4Gi = 22937Mi
			analysis: &PodAnalysis{
				PodInfo:         podInfo("a", "prod", "Deployment", "app", 4000, 32*gi),
				CPURequestMilli: 4000, MemRequestBytes: 32 * gi,
				CPUP95: 400, MemP95: 6 * gi,
				IsOverProvCPU: true,
				DataPoints: 5000,
			},
			wantCPU: "2800m",
			wantMem: func() string {
				mem := float64(32*gi) * (2800.0 / 4000.0)
				return formatBytes(int64(mem))
			}(),
		},
		{
			name: "usage-based floor wins over minKeepRatio",
			cfg: func() *config.Config {
				c := defaultCfg()
				c.Rightsizer.MinKeepRatio = 0.3
				return c
			}(),
			// CPU: max(3000*1.2=3600, 5000*0.3=1500, 1000) = 3600m (usage wins)
			// Mem: 32Gi * (3600/5000) = 23.04Gi
			analysis: &PodAnalysis{
				PodInfo:         podInfo("a", "prod", "Deployment", "heavy", 5000, 32*gi),
				CPURequestMilli: 5000, MemRequestBytes: 32 * gi,
				CPUP95: 3000, MemP95: 6 * gi,
				IsOverProvCPU: true,
				DataPoints: 5000,
			},
			wantCPU: "3600m",
			wantMem: func() string {
				mem := float64(32*gi) * (3600.0 / 5000.0)
				return formatBytes(int64(mem))
			}(),
		},
		{
			name: "memory floor from P95 usage takes precedence over ratio",
			// CPU: max(200*1.2=240, 2000*0.7=1400, 1000) = 1400m
			// Ratio mem: 1400 * (128Gi/32000) = 5734Mi
			// Mem floor: 5Gi * 1.2 = 6Gi = 6144Mi
			// 5734Mi < 6144Mi → floor wins → 6144Mi
			analysis: &PodAnalysis{
				PodInfo:         podInfo("a", "ns", "Deployment", "w", 2000, 16*gi),
				CPURequestMilli: 2000, MemRequestBytes: 16 * gi,
				CPUP95: 200, MemP95: 5 * gi,
				IsOverProvCPU: true,
				DataPoints: 1000,
				NodeCPUCapMilli: 32000, NodeMemCapBytes: 128 * gi,
			},
			wantCPU: "1400m",
			wantMem: formatBytes(int64(float64(5*gi) * UsageHeadroom)),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg
			if cfg == nil {
				cfg = defaultCfg()
			}
			recs := NewRecommender(cfg).Recommend(tt.analysis)
			assertInvariants(t, recs, tt.analysis)
			if len(recs) != 1 {
				t.Fatalf("got %d recs, want 1", len(recs))
			}
			rec := recs[0]
			if rec.Details["suggestedCPURequest"] != tt.wantCPU {
				t.Errorf("suggestedCPU = %q, want %q", rec.Details["suggestedCPURequest"], tt.wantCPU)
			}
			if rec.Details["suggestedMemRequest"] != tt.wantMem {
				t.Errorf("suggestedMem = %q, want %q", rec.Details["suggestedMemRequest"], tt.wantMem)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Recommendation metadata
// ---------------------------------------------------------------------------

func TestRecommend_MetadataFields(t *testing.T) {
	analysis := &PodAnalysis{
		PodInfo:         podInfo("svc-pod", "staging", "StatefulSet", "my-svc", 4000, 16*gi),
		CPURequestMilli: 4000, MemRequestBytes: 16 * gi,
		CPUP95: 200, MemP95: gi,
		IsOverProvCPU: true,
		DataPoints: 1000,
	}

	recs := NewRecommender(defaultCfg()).Recommend(analysis)
	if len(recs) != 1 {
		t.Fatalf("got %d recs, want 1", len(recs))
	}

	rec := recs[0]
	if rec.TargetKind != "StatefulSet" {
		t.Errorf("TargetKind = %q, want StatefulSet", rec.TargetKind)
	}
	if rec.TargetName != "my-svc" {
		t.Errorf("TargetName = %q, want my-svc", rec.TargetName)
	}
	if rec.TargetNamespace != "staging" {
		t.Errorf("TargetNamespace = %q, want staging", rec.TargetNamespace)
	}
	if rec.AutoExecutable {
		t.Error("AutoExecutable should be false — all recs require manual approval")
	}
	if rec.Type != optimizer.RecommendationPodRightsize {
		t.Errorf("Type = %q, want %q", rec.Type, optimizer.RecommendationPodRightsize)
	}
	if rec.EstimatedSaving.MonthlySavingsUSD <= 0 {
		t.Errorf("MonthlySavingsUSD = %.2f, want > 0", rec.EstimatedSaving.MonthlySavingsUSD)
	}
	if rec.EstimatedSaving.AnnualSavingsUSD <= 0 {
		t.Errorf("AnnualSavingsUSD = %.2f, want > 0", rec.EstimatedSaving.AnnualSavingsUSD)
	}
}

// ---------------------------------------------------------------------------
// Replica count scaling
// ---------------------------------------------------------------------------

func TestRecommend_ReplicaCountScalesSavings(t *testing.T) {
	makeAnalysis := func(replicas int) *PodAnalysis {
		p := podInfo("a", "ns", "Deployment", "app", 4000, 32*gi)
		p.ReplicaCount = replicas
		return &PodAnalysis{
			PodInfo:         p,
			CPURequestMilli: 4000, MemRequestBytes: 32 * gi,
			CPUP95: 400, MemP95: 6 * gi,
			IsOverProvCPU: true,
			DataPoints: 5000,
		}
	}

	recs1 := NewRecommender(defaultCfg()).Recommend(makeAnalysis(1))
	recs5 := NewRecommender(defaultCfg()).Recommend(makeAnalysis(5))

	if len(recs1) != 1 || len(recs5) != 1 {
		t.Fatalf("expected 1 rec each, got %d and %d", len(recs1), len(recs5))
	}

	savings1 := recs1[0].EstimatedSaving.MonthlySavingsUSD
	savings5 := recs5[0].EstimatedSaving.MonthlySavingsUSD

	if savings5 < savings1*4.9 || savings5 > savings1*5.1 {
		t.Errorf("5-replica savings = %.2f, want ~5x of 1-replica %.2f", savings5, savings1)
	}
}

// ---------------------------------------------------------------------------
// Production regression tests — real scenarios from audit log
// ---------------------------------------------------------------------------

func TestRecommend_ProductionRegressions(t *testing.T) {
	tests := []struct {
		name     string
		analysis *PodAnalysis
		wantN    int
	}{
		{
			// Audit: "Upsize spr-apps/spr-confluence-plugin: CPU 23m→270m"
			// Upsize recs are no longer generated
			name: "prod: 23m CPU pod — no upsize rec",
			analysis: &PodAnalysis{
				PodInfo:         podInfo("a", "spr-apps", "ReplicaSet", "spr-confluence", 23, 4*gi),
				CPURequestMilli: 23, MemRequestBytes: 4 * gi,
				CPUP95: 200, MemP95: gi,
				IsOverProvCPU: false, IsUnderProvCPU: true,
				DataPoints: 500,
			},
			wantN: 0,
		},
		{
			// Audit: "Upsize spr-apps/standalone-monitoring-log: CPU 200m→282m"
			name: "prod: 200m CPU pod must not be upsized",
			analysis: &PodAnalysis{
				PodInfo:         podInfo("a", "spr-apps", "ReplicaSet", "monitoring-log", 200, 4*gi),
				CPURequestMilli: 200, MemRequestBytes: 4 * gi,
				CPUP95: 190, MemP95: 2 * gi,
				IsOverProvCPU: true,
				DataPoints: 500,
			},
			wantN: 0, // 200m < 1000m floor
		},
		{
			// Audit: "Upsize spr-apps/standalone-dialer: CPU 460m→5624m"
			// Upsize recs are no longer generated
			name: "prod: 460m CPU pod — no upsize rec",
			analysis: &PodAnalysis{
				PodInfo:         podInfo("a", "spr-apps", "ReplicaSet", "dialer", 460, 16*gi),
				CPURequestMilli: 460, MemRequestBytes: 16 * gi,
				CPUP95: 4000, MemP95: 8 * gi,
				IsOverProvCPU: false, IsUnderProvCPU: true,
				DataPoints: 500,
			},
			wantN: 0,
		},
		{
			// Audit: "Upsize spr-apps/icx-vxml-browser: memory 6724Mi→15413Mi"
			name: "prod: memory-only upsize must not happen",
			analysis: &PodAnalysis{
				PodInfo:         podInfo("a", "spr-apps", "ReplicaSet", "icx-vxml", 2000, 6724*mi),
				CPURequestMilli: 2000, MemRequestBytes: 6724 * mi,
				CPUP95: 100, MemP95: 12000 * mi, // mem usage > request
				IsOverProvCPU:   true,
				DataPoints:      500,
				NodeCPUCapMilli: 32000, NodeMemCapBytes: 128 * gi,
			},
			wantN: 0, // memory would need to increase → capped → delta negative → skip
		},
		{
			// Audit: "Upsize spr-apps/automation-test-tracker: CPU 10m→694m"
			// Upsize recs are no longer generated
			name: "prod: 10m CPU pod — no upsize rec",
			analysis: &PodAnalysis{
				PodInfo:         podInfo("a", "spr-apps", "ReplicaSet", "test-tracker", 10, 4*gi),
				CPURequestMilli: 10, MemRequestBytes: 4 * gi,
				CPUP95: 500, MemP95: 2 * gi,
				IsOverProvCPU: false, IsUnderProvCPU: true,
				DataPoints: 500,
			},
			wantN: 0,
		},
		{
			// Audit: "Upsize monitoring/prometheus-server: CPU 300m→2132m, memory 16640Mi→49197Mi"
			// Upsize recs are no longer generated
			name: "prod: prometheus — no upsize rec",
			analysis: &PodAnalysis{
				PodInfo:         podInfo("a", "monitoring", "ReplicaSet", "prometheus-server", 300, 16640*mi),
				CPURequestMilli: 300, MemRequestBytes: 16640 * mi,
				CPUP95: 1800, MemP95: 40000 * mi,
				IsOverProvCPU: false, IsUnderProvCPU: true, IsUnderProvMem: true,
				DataPoints: 500,
			},
			wantN: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recs := NewRecommender(defaultCfg()).Recommend(tt.analysis)
			assertInvariants(t, recs, tt.analysis)
			if len(recs) != tt.wantN {
				for _, r := range recs {
					t.Logf("  rec: %s (CPU %s→%s, Mem %s→%s)",
						r.Summary,
						r.Details["currentCPURequest"], r.Details["suggestedCPURequest"],
						r.Details["currentMemRequest"], r.Details["suggestedMemRequest"])
				}
				t.Fatalf("got %d recs, want %d", len(recs), tt.wantN)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ValidateDownsizeTargets — direct unit tests
// ---------------------------------------------------------------------------

func TestValidateDownsizeTargets(t *testing.T) {
	tests := []struct {
		name                                     string
		currentCPU, sugCPU, currentMem, sugMem   int64
		wantErr                                  bool
	}{
		{"valid downsize", 2000, 1400, 8 * gi, 5 * gi, false},
		{"CPU not decreasing (equal)", 1000, 1000, 8 * gi, 5 * gi, true},
		{"CPU increasing", 1000, 1500, 8 * gi, 5 * gi, true},
		{"CPU below 1000m floor", 2000, 500, 8 * gi, 5 * gi, true},
		{"memory increasing", 2000, 1400, 4 * gi, 5 * gi, true},
		{"memory delta < 2 GiB", 2000, 1400, 8 * gi, 7 * gi, true},
		{"memory delta exactly 2 GiB", 2000, 1400, 8 * gi, 6 * gi, false},
		{"memory delta 0", 2000, 1400, 8 * gi, 8 * gi, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateDownsizeTargets(tt.currentCPU, tt.sugCPU, tt.currentMem, tt.sugMem)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateDownsizeTargets() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Extracted computation functions — unit tests
// ---------------------------------------------------------------------------

func TestComputeCPUTarget(t *testing.T) {
	tests := []struct {
		name         string
		p95, request int64
		keepRatio    float64
		want         int64
	}{
		{"minKeepRatio wins", 300, 2000, 0.7, 1400},      // max(360, 1400, 1000) = 1400
		{"usage wins", 3000, 5000, 0.3, 3600},             // max(3600, 1500, 1000) = 3600
		{"1 CPU floor wins", 100, 500, 0.7, 1000},         // max(120, 350, 1000) = 1000
		{"all equal", 833, 1428, 0.7, 1000},               // max(999, 999, 1000) = 1000
		{"zero P95", 0, 2000, 0.7, 1400},                  // max(0, 1400, 1000) = 1400
		{"very large values", 50000, 100000, 0.7, 70000},  // max(60000, 70000, 1000) = 70000
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeCPUTarget(tt.p95, tt.request, tt.keepRatio)
			if got != tt.want {
				t.Errorf("computeCPUTarget(%d, %d, %.1f) = %d, want %d",
					tt.p95, tt.request, tt.keepRatio, got, tt.want)
			}
		})
	}
}

func TestComputeMemFloor(t *testing.T) {
	tests := []struct {
		name string
		p95  int64
		want int64
	}{
		{"normal", 5 * gi, int64(float64(5*gi) * UsageHeadroom)},
		{"small usage", 10 * mi, MinMemFloorBytes}, // 10Mi * 1.2 = 12Mi < 32Mi
		{"zero usage", 0, MinMemFloorBytes},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeMemFloor(tt.p95)
			if got != tt.want {
				t.Errorf("computeMemFloor(%d) = %d, want %d", tt.p95, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// formatBytes
// ---------------------------------------------------------------------------

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		input int64
		want  string
	}{
		{0, "0"},
		{1023, "1023"},
		{1024, "1Ki"},
		{1024 * 1024, "1Mi"},
		{500 * 1024 * 1024, "500Mi"},
		{gi, "1Gi"},
		{4 * gi, "4Gi"},
		{gi + mi, "1025Mi"}, // not exact GiB → use Mi
		{int64(4.5 * float64(gi)), "4608Mi"}, // 4.5Gi → not exact → Mi
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := formatBytes(tt.input)
			if got != tt.want {
				t.Errorf("formatBytes(%d) = %q, want %q", tt.input, got, tt.want)
			}
		})
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
// No upsize ever — verify under-provisioned pods produce zero recs
// ---------------------------------------------------------------------------

func TestRecommend_NoUpsizeEver(t *testing.T) {
	tests := []struct {
		name     string
		analysis *PodAnalysis
	}{
		{
			name: "under-provisioned CPU only",
			analysis: &PodAnalysis{
				PodInfo:         podInfo("a", "prod", "Deployment", "api", 100, gi),
				CPURequestMilli: 100, MemRequestBytes: gi,
				CPUP95: 400, CPUMax: 500,
				IsOverProvCPU: false, IsUnderProvCPU: true,
				DataPoints: 500,
			},
		},
		{
			name: "under-provisioned memory only",
			analysis: &PodAnalysis{
				PodInfo:         podInfo("a", "prod", "StatefulSet", "worker", 500, 512*mi),
				CPURequestMilli: 500, MemRequestBytes: 512 * mi,
				MemP95: 500 * mi,
				IsOverProvCPU: false, IsUnderProvMem: true,
				DataPoints: 800,
			},
		},
		{
			name: "under-provisioned CPU and memory",
			analysis: &PodAnalysis{
				PodInfo:         podInfo("a", "monitoring", "Deployment", "prometheus", 300, 16640*mi),
				CPURequestMilli: 300, MemRequestBytes: 16640 * mi,
				CPUP95: 1800, MemP95: 40000 * mi,
				IsOverProvCPU: false, IsUnderProvCPU: true, IsUnderProvMem: true,
				DataPoints: 500,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recs := NewRecommender(defaultCfg()).Recommend(tt.analysis)
			if len(recs) != 0 {
				t.Fatalf("got %d recs, want 0 (no upsize recs should be generated)", len(recs))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Never auto-executable — all downsize recs must have AutoExecutable=false
// ---------------------------------------------------------------------------

func TestRecommend_NeverAutoExecutable(t *testing.T) {
	tests := []struct {
		name     string
		analysis *PodAnalysis
	}{
		{
			name: "standard downsize",
			analysis: &PodAnalysis{
				PodInfo:         podInfo("a", "prod", "Deployment", "app", 4000, 32*gi),
				CPURequestMilli: 4000, MemRequestBytes: 32 * gi,
				CPUP95: 400, MemP95: 6 * gi,
				IsOverProvCPU: true,
				DataPoints: 5000,
			},
		},
		{
			name: "node-ratio downsize",
			analysis: &PodAnalysis{
				PodInfo:         podInfo("a", "jobs", "Deployment", "batch", 2000, 8*gi),
				CPURequestMilli: 2000, MemRequestBytes: 8 * gi,
				CPUP95: 300, MemP95: gi,
				IsOverProvCPU: true,
				DataPoints:      2000,
				NodeCPUCapMilli: 32000, NodeMemCapBytes: 128 * gi,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recs := NewRecommender(defaultCfg()).Recommend(tt.analysis)
			if len(recs) != 1 {
				t.Fatalf("got %d recs, want 1", len(recs))
			}
			if recs[0].AutoExecutable {
				t.Error("AutoExecutable should be false — all recs require manual approval")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// isDownsizeRec — verify upsize recs are rejected
// ---------------------------------------------------------------------------

func TestIsDownsizeRec_RejectsUpsize(t *testing.T) {
	tests := []struct {
		name    string
		rec     optimizer.Recommendation
		want    bool
	}{
		{
			name: "downsize rec (cpu+memory, no direction)",
			rec: optimizer.Recommendation{
				Details: map[string]string{"resource": "cpu+memory"},
			},
			want: true,
		},
		{
			name: "upsize rec (cpu+memory, direction=upsize)",
			rec: optimizer.Recommendation{
				Details: map[string]string{"resource": "cpu+memory", "direction": "upsize"},
			},
			want: false,
		},
		{
			name: "OOM rec (resource=memory)",
			rec: optimizer.Recommendation{
				Details: map[string]string{"resource": "memory"},
			},
			want: false,
		},
		{
			name: "cpu-only upsize",
			rec: optimizer.Recommendation{
				Details: map[string]string{"resource": "cpu", "direction": "upsize"},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isDownsizeRec(tt.rec)
			if got != tt.want {
				t.Errorf("isDownsizeRec() = %v, want %v", got, tt.want)
			}
		})
	}
}

package rightsizer

import (
	"fmt"
	"time"

	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/pkg/cost"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// Safety invariant constants — single source of truth for all thresholds.
// Tests reference these directly so changes are automatically consistent.
const (
	// MinCPUFloorMilli is the absolute minimum CPU target. Pods with requests
	// at or below this are never rightsized — sub-1-CPU savings aren't worth
	// the disruption.
	MinCPUFloorMilli = 1000

	// MinMemDeltaBytes is the minimum memory reduction required to emit a
	// recommendation. Restarting pods for less than 2 GiB of memory savings
	// isn't worth the disruption.
	MinMemDeltaBytes = 2 * 1024 * 1024 * 1024

	// MinMemFloorBytes is the absolute minimum memory target (32Mi).
	MinMemFloorBytes = 32 * 1024 * 1024

	// DefaultMinKeepRatio is the fallback minimum fraction of current resources
	// to keep per cycle (70% = max 30% reduction).
	DefaultMinKeepRatio = 0.7

	// UsageHeadroom is the multiplier applied to P95 usage to compute the
	// minimum safe resource level (20% headroom above P95).
	UsageHeadroom = 1.2

	// MinCPUAbsolute is the smallest CPU value we'd ever compute (cosmetic floor).
	MinCPUAbsolute = 10
)

// Recommender generates CPU/memory rightsizing recommendations.
type Recommender struct {
	config *config.Config
}

func NewRecommender(cfg *config.Config) *Recommender {
	return &Recommender{config: cfg}
}

// Recommend generates recommendations based on pod analysis.
//
// All returned recommendations are validated against safety invariants before
// being returned. Even if a bug in the computation produces an unsafe result,
// the validation catches it.
func (r *Recommender) Recommend(analysis *PodAnalysis) []optimizer.Recommendation {
	if analysis.DataPoints < 6 {
		return nil
	}

	pod := analysis.PodInfo

	// Never generate any recommendations for DaemonSets. DaemonSets run on
	// every node, so any resource change multiplies across the entire cluster.
	if pod.OwnerKind == "DaemonSet" {
		return nil
	}

	replicaCount := int64(1)
	if pod.ReplicaCount > 1 {
		replicaCount = int64(pod.ReplicaCount)
	}

	var recs []optimizer.Recommendation

	// Only generate downsizing recommendations when CPU is over-provisioned.
	if analysis.IsOverProvCPU && analysis.CPUP95 > 0 {
		rec := r.computeDownsize(analysis, replicaCount)
		if rec != nil {
			recs = append(recs, *rec)
		}
	}

	// Upsize when under-provisioned, but only if usage is near the limit.
	// If the limit is high, the pod can burst beyond its request — no need
	// to increase the request (and cost) when burst headroom exists.
	if analysis.IsUnderProvCPU || analysis.IsUnderProvMem {
		rec := r.recommendUpsize(analysis, replicaCount)
		if rec != nil {
			recs = append(recs, *rec)
		}
	}

	return recs
}

// computeDownsize generates a combined CPU+memory recommendation that aligns
// the pod's CPU:memory ratio to the node's ratio for optimal bin-packing.
//
// Algorithm:
//  1. Compute CPU target from usage (P95 * headroom, clamped by MinKeepRatio
//     and 1 CPU floor)
//  2. Compute memory to match node's CPU:memory ratio for the target CPU
//  3. Clamp memory: never below usage floor, never above current request
//  4. Validate all safety invariants before emitting
func (r *Recommender) computeDownsize(analysis *PodAnalysis, replicaCount int64) *optimizer.Recommendation {
	pod := analysis.PodInfo

	minKeepRatio := r.config.Rightsizer.MinKeepRatio
	if minKeepRatio <= 0 {
		minKeepRatio = DefaultMinKeepRatio
	}

	// --- CPU target ---
	suggestedCPU := computeCPUTarget(analysis.CPUP95, analysis.CPURequestMilli, minKeepRatio)

	// --- Memory target ---
	memFloor := computeMemFloor(analysis.MemP95)
	suggestedMem := computeMemTarget(suggestedCPU, analysis, memFloor)

	// Cap memory at current — never increase memory during a cost-saving
	// downsize. Restarting pods to increase memory while reducing CPU is
	// confusing and rarely worth it.
	if suggestedMem > analysis.MemRequestBytes {
		suggestedMem = analysis.MemRequestBytes
	}

	// --- Safety validation (catches bugs in the computation above) ---
	if err := ValidateDownsizeTargets(analysis.CPURequestMilli, suggestedCPU, analysis.MemRequestBytes, suggestedMem); err != nil {
		return nil
	}

	// --- Savings ---
	cpuSavings := estimateCPUSavings(analysis.CPURequestMilli, suggestedCPU, r.config.CloudProvider)
	memSavings := estimateMemorySavings(analysis.MemRequestBytes, suggestedMem, r.config.CloudProvider)
	totalSavings := (cpuSavings + memSavings) * float64(replicaCount)

	if totalSavings <= 0 {
		return nil
	}

	return &optimizer.Recommendation{
		ID:              fmt.Sprintf("rightsize-combined-%s-%s-%d", pod.Pod.Namespace, pod.Pod.Name, time.Now().Unix()),
		Type:            optimizer.RecommendationPodRightsize,
		Priority:        optimizer.PriorityMedium,
		AutoExecutable:  true,
		TargetKind:      pod.OwnerKind,
		TargetName:      pod.OwnerName,
		TargetNamespace: pod.Pod.Namespace,
		Summary: fmt.Sprintf("Rightsize %s/%s: CPU %dm→%dm, memory %s→%s (%d replicas)",
			pod.Pod.Namespace, pod.OwnerName,
			analysis.CPURequestMilli, suggestedCPU,
			formatBytes(analysis.MemRequestBytes), formatBytes(suggestedMem),
			replicaCount),
		ActionSteps: []string{
			fmt.Sprintf("Patch CPU request from %dm to %dm and memory from %s to %s",
				analysis.CPURequestMilli, suggestedCPU,
				formatBytes(analysis.MemRequestBytes), formatBytes(suggestedMem)),
		},
		EstimatedSaving: optimizer.SavingEstimate{
			MonthlySavingsUSD: totalSavings,
			AnnualSavingsUSD:  totalSavings * 12,
			Currency:          "USD",
		},
		Details: map[string]string{
			"resource":            "cpu+memory",
			"currentCPURequest":   fmt.Sprintf("%dm", analysis.CPURequestMilli),
			"suggestedCPURequest": fmt.Sprintf("%dm", suggestedCPU),
			"currentMemRequest":   formatBytes(analysis.MemRequestBytes),
			"suggestedMemRequest": formatBytes(suggestedMem),
			"p95CPU":              fmt.Sprintf("%dm", analysis.CPUP95),
			"p95Mem":              formatBytes(analysis.MemP95),
			"replicaCount":        fmt.Sprintf("%d", replicaCount),
		},
		CreatedAt: time.Now(),
	}
}

// recommendUpsize generates a request increase recommendation for under-provisioned pods.
// It skips the increase if the pod has a high limit that provides burst headroom — the pod
// can already burst beyond its request without needing a permanent request increase.
func (r *Recommender) recommendUpsize(analysis *PodAnalysis, replicaCount int64) *optimizer.Recommendation {
	pod := analysis.PodInfo

	needCPUUpsize := analysis.IsUnderProvCPU
	needMemUpsize := analysis.IsUnderProvMem

	// Skip CPU upsize if limit provides burst headroom (usage < 70% of limit).
	if needCPUUpsize && analysis.CPULimitMilli > 0 {
		usageToLimit := float64(analysis.CPUP95) / float64(analysis.CPULimitMilli)
		if usageToLimit < 0.7 {
			needCPUUpsize = false
		}
	}

	// Skip memory upsize if limit provides headroom (usage < 70% of limit).
	if needMemUpsize && analysis.MemLimitBytes > 0 {
		usageToLimit := float64(analysis.MemP95) / float64(analysis.MemLimitBytes)
		if usageToLimit < 0.7 {
			needMemUpsize = false
		}
	}

	if !needCPUUpsize && !needMemUpsize {
		return nil
	}

	suggestedCPU := analysis.CPURequestMilli
	suggestedMem := analysis.MemRequestBytes

	if needCPUUpsize {
		// Set request to P95 * 1.2 headroom
		suggestedCPU = int64(float64(analysis.CPUP95) * 1.2)
		if suggestedCPU <= analysis.CPURequestMilli {
			suggestedCPU = analysis.CPURequestMilli // no-op, don't decrease
			needCPUUpsize = false
		}
	}

	if needMemUpsize {
		suggestedMem = int64(float64(analysis.MemP95) * 1.2)
		if suggestedMem <= analysis.MemRequestBytes {
			suggestedMem = analysis.MemRequestBytes
			needMemUpsize = false
		}
	}

	if !needCPUUpsize && !needMemUpsize {
		return nil
	}

	var summary string
	resource := ""
	if needCPUUpsize && needMemUpsize {
		resource = "cpu+memory"
		summary = fmt.Sprintf("Upsize %s/%s: CPU %dm→%dm, memory %s→%s (%d replicas)",
			pod.Pod.Namespace, pod.OwnerName,
			analysis.CPURequestMilli, suggestedCPU,
			formatBytes(analysis.MemRequestBytes), formatBytes(suggestedMem),
			replicaCount)
	} else if needCPUUpsize {
		resource = "cpu"
		summary = fmt.Sprintf("Upsize %s/%s: CPU %dm→%dm (%d replicas)",
			pod.Pod.Namespace, pod.OwnerName,
			analysis.CPURequestMilli, suggestedCPU, replicaCount)
	} else {
		resource = "memory"
		summary = fmt.Sprintf("Upsize %s/%s: memory %s→%s (%d replicas)",
			pod.Pod.Namespace, pod.OwnerName,
			formatBytes(analysis.MemRequestBytes), formatBytes(suggestedMem),
			replicaCount)
	}

	return &optimizer.Recommendation{
		ID:              fmt.Sprintf("rightsize-upsize-%s-%s-%d", pod.Pod.Namespace, pod.Pod.Name, time.Now().Unix()),
		Type:            optimizer.RecommendationPodRightsize,
		Priority:        optimizer.PriorityHigh,
		AutoExecutable:  false, // upsizing should be reviewed, not auto-applied
		TargetKind:      pod.OwnerKind,
		TargetName:      pod.OwnerName,
		TargetNamespace: pod.Pod.Namespace,
		Summary:         summary,
		ActionSteps: []string{
			fmt.Sprintf("Increase requests: CPU %dm→%dm, memory %s→%s",
				analysis.CPURequestMilli, suggestedCPU,
				formatBytes(analysis.MemRequestBytes), formatBytes(suggestedMem)),
		},
		Details: map[string]string{
			"resource":            resource,
			"direction":           "upsize",
			"currentCPURequest":   fmt.Sprintf("%dm", analysis.CPURequestMilli),
			"suggestedCPURequest": fmt.Sprintf("%dm", suggestedCPU),
			"currentMemRequest":   formatBytes(analysis.MemRequestBytes),
			"suggestedMemRequest": formatBytes(suggestedMem),
			"cpuLimit":            fmt.Sprintf("%dm", analysis.CPULimitMilli),
			"memLimit":            formatBytes(analysis.MemLimitBytes),
			"p95CPU":              fmt.Sprintf("%dm", analysis.CPUP95),
			"p95Mem":              formatBytes(analysis.MemP95),
			"replicaCount":        fmt.Sprintf("%d", replicaCount),
		},
		CreatedAt: time.Now(),
	}
}

// computeCPUTarget determines the suggested CPU in millicores.
// Result is clamped to: max(P95 * headroom, request * minKeepRatio, 1 CPU floor).
func computeCPUTarget(cpuP95, cpuRequest int64, minKeepRatio float64) int64 {
	usageBased := int64(float64(cpuP95) * UsageHeadroom)
	ratioBased := int64(float64(cpuRequest) * minKeepRatio)

	target := usageBased
	if target < ratioBased {
		target = ratioBased
	}
	if target < MinCPUAbsolute {
		target = MinCPUAbsolute
	}
	if target < MinCPUFloorMilli {
		target = MinCPUFloorMilli
	}
	return target
}

// computeMemFloor returns the minimum safe memory target based on P95 usage.
func computeMemFloor(memP95 int64) int64 {
	floor := int64(float64(memP95) * UsageHeadroom)
	if floor < MinMemFloorBytes {
		floor = MinMemFloorBytes
	}
	return floor
}

// computeMemTarget computes the suggested memory based on node ratio or
// proportional fallback. The result is clamped to at least memFloor but
// NOT capped at current — the caller handles that.
func computeMemTarget(suggestedCPU int64, analysis *PodAnalysis, memFloor int64) int64 {
	var mem int64

	if analysis.NodeCPUCapMilli > 0 && analysis.NodeMemCapBytes > 0 {
		// Match the node's CPU:memory ratio for optimal bin-packing.
		bytesPerMilli := float64(analysis.NodeMemCapBytes) / float64(analysis.NodeCPUCapMilli)
		mem = int64(float64(suggestedCPU) * bytesPerMilli)
	} else if analysis.CPURequestMilli > 0 {
		// No node info — reduce memory proportionally to CPU.
		cpuKeepRatio := float64(suggestedCPU) / float64(analysis.CPURequestMilli)
		mem = int64(float64(analysis.MemRequestBytes) * cpuKeepRatio)
	} else {
		mem = memFloor
	}

	if mem < memFloor {
		mem = memFloor
	}
	return mem
}

// ValidateDownsizeTargets checks all safety invariants on computed targets.
// Returns nil if valid, or an error describing the violation.
//
// This is the safety net — even if the computation has a bug, this function
// prevents unsafe recommendations from being emitted. Tests also call this
// directly to verify invariants.
func ValidateDownsizeTargets(currentCPU, suggestedCPU, currentMem, suggestedMem int64) error {
	if suggestedCPU >= currentCPU {
		return fmt.Errorf("CPU not decreasing: %dm -> %dm", currentCPU, suggestedCPU)
	}
	if suggestedMem > currentMem {
		return fmt.Errorf("memory increasing: %s -> %s", formatBytes(currentMem), formatBytes(suggestedMem))
	}
	if suggestedCPU < MinCPUFloorMilli {
		return fmt.Errorf("CPU %dm below %dm floor", suggestedCPU, MinCPUFloorMilli)
	}
	memDelta := currentMem - suggestedMem
	if memDelta < MinMemDeltaBytes {
		return fmt.Errorf("memory delta %s below %s minimum", formatBytes(memDelta), formatBytes(MinMemDeltaBytes))
	}
	return nil
}

// --- Cost estimation ---

// vCPUHourlyCostByCloud returns an approximate per-vCPU hourly cost for the cloud provider.
// AWS uses 3-year convertible reserved pricing (~50% of on-demand).
func vCPUHourlyCostByCloud(cloudProvider string) float64 {
	switch cloudProvider {
	case "gcp":
		return 0.031611
	case "azure":
		return 0.043
	default: // aws
		return 0.02 // m5 rate at ~50% of on-demand (3yr convertible reserved)
	}
}

func estimateCPUSavings(currentMilli, suggestedMilli int64, cloudProvider string) float64 {
	cpuSaved := float64(currentMilli-suggestedMilli) / 1000.0
	return cpuSaved * vCPUHourlyCostByCloud(cloudProvider) * cost.HoursPerMonth
}

// memGiBHourlyCostByCloud returns an approximate per-GiB-RAM hourly cost.
// AWS uses 3-year convertible reserved pricing (~50% of on-demand).
func memGiBHourlyCostByCloud(cloudProvider string) float64 {
	switch cloudProvider {
	case "gcp":
		return 0.004237
	case "azure":
		return 0.005
	default: // aws
		return 0.00322 // m5 rate at ~50% of on-demand (3yr convertible reserved)
	}
}

func estimateMemorySavings(currentBytes, suggestedBytes int64, cloudProvider string) float64 {
	memSavedGiB := float64(currentBytes-suggestedBytes) / (1024 * 1024 * 1024)
	return memSavedGiB * memGiBHourlyCostByCloud(cloudProvider) * cost.HoursPerMonth
}

func formatBytes(b int64) string {
	const (
		ki = 1024
		mi = ki * 1024
		gi = mi * 1024
	)
	switch {
	case b >= gi && b%gi == 0:
		return fmt.Sprintf("%dGi", b/gi)
	case b >= mi:
		return fmt.Sprintf("%dMi", b/mi)
	case b >= ki:
		return fmt.Sprintf("%dKi", b/ki)
	default:
		return fmt.Sprintf("%d", b)
	}
}

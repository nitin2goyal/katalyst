package rightsizer

import (
	"fmt"
	"time"

	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/pkg/cost"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// Recommender generates CPU/memory rightsizing recommendations.
type Recommender struct {
	config *config.Config
}

func NewRecommender(cfg *config.Config) *Recommender {
	return &Recommender{config: cfg}
}

// Recommend generates recommendations based on pod analysis.
func (r *Recommender) Recommend(analysis *PodAnalysis) []optimizer.Recommendation {
	var recs []optimizer.Recommendation

	// Require at least 6 data points before making recommendations to avoid
	// acting on insufficient data (e.g., a pod that just started).
	if analysis.DataPoints < 6 {
		return nil
	}

	pod := analysis.PodInfo

	// Never generate any recommendations for DaemonSets. DaemonSets run on
	// every node, so any resource change multiplies across the entire cluster.
	if pod.OwnerKind == "DaemonSet" {
		return nil
	}

	// Determine replica count for scaling per-pod savings
	replicaCount := int64(1)
	if pod.ReplicaCount > 1 {
		replicaCount = int64(pod.ReplicaCount)
	}

	// Downsize when CPU is over-provisioned. Memory is adjusted to match the
	// node's CPU:memory ratio for optimal bin-packing. Memory may increase if
	// needed to maintain the ratio — that's acceptable.
	if analysis.IsOverProvCPU && analysis.CPUP95 > 0 {
		rec := r.recommendNodeRatioDownsize(analysis, replicaCount)
		if rec != nil {
			recs = append(recs, *rec)
		}
	}

	return recs
}

// recommendNodeRatioDownsize generates a combined CPU+memory recommendation that
// aligns the pod's CPU:memory ratio to the node's ratio for optimal bin-packing.
//
// Algorithm:
//  1. Compute CPU floor from usage (P95 * 1.2, clamped by MinKeepRatio)
//  2. If node capacity is known, compute memory to match node's CPU:memory ratio
//  3. If node capacity is unknown, fall back to proportional reduction
//  4. Never increase CPU (only decrease or hold). Memory may increase to match ratio.
//  5. Only emit if CPU actually decreases (the main savings driver).
func (r *Recommender) recommendNodeRatioDownsize(analysis *PodAnalysis, replicaCount int64) *optimizer.Recommendation {
	pod := analysis.PodInfo

	minKeepRatio := r.config.Rightsizer.MinKeepRatio
	if minKeepRatio <= 0 {
		minKeepRatio = 0.7 // fallback default
	}

	// CPU floor: max of usage-based and minKeepRatio-based
	cpuFloor := int64(float64(analysis.CPUP95) * 1.2)
	cpuMinKeep := int64(float64(analysis.CPURequestMilli) * minKeepRatio)
	if cpuFloor < cpuMinKeep {
		cpuFloor = cpuMinKeep
	}
	if cpuFloor < 10 {
		cpuFloor = 10
	}

	suggestedCPU := cpuFloor

	// Never increase CPU
	if suggestedCPU >= analysis.CPURequestMilli {
		return nil
	}

	// Memory floor: ensure we don't go below usage
	memFloor := int64(float64(analysis.MemP95) * 1.2)
	minMem := int64(32 * 1024 * 1024) // 32Mi
	if memFloor < minMem {
		memFloor = minMem
	}

	var suggestedMem int64

	if analysis.NodeCPUCapMilli > 0 && analysis.NodeMemCapBytes > 0 {
		// Node ratio: bytes of memory per millicore of CPU
		bytesPerMilli := float64(analysis.NodeMemCapBytes) / float64(analysis.NodeCPUCapMilli)

		// Set memory to match node ratio for the target CPU
		suggestedMem = int64(float64(suggestedCPU) * bytesPerMilli)

		// Ensure memory doesn't go below usage floor
		if suggestedMem < memFloor {
			suggestedMem = memFloor
		}

		// Never increase memory beyond current request — the goal is to free
		// capacity, not consume more. On highmem nodes the ratio can push
		// memory well above the current request which is counterproductive.
		if suggestedMem > analysis.MemRequestBytes {
			suggestedMem = analysis.MemRequestBytes
		}
	} else {
		// No node info — fall back to proportional reduction using same keep-ratio as CPU
		cpuKeepRatio := float64(suggestedCPU) / float64(analysis.CPURequestMilli)
		suggestedMem = int64(float64(analysis.MemRequestBytes) * cpuKeepRatio)
		if suggestedMem < memFloor {
			suggestedMem = memFloor
		}
	}

	// CPU must actually decrease to be worth emitting
	if suggestedCPU >= analysis.CPURequestMilli {
		return nil
	}

	cpuSavings := estimateCPUSavings(analysis.CPURequestMilli, suggestedCPU, r.config.CloudProvider)
	memSavings := estimateMemorySavings(analysis.MemRequestBytes, suggestedMem, r.config.CloudProvider)
	perPodSavings := cpuSavings + memSavings
	totalSavings := perPodSavings * float64(replicaCount)

	memDirection := "→"
	if suggestedMem > analysis.MemRequestBytes {
		memDirection = "↑"
	}

	return &optimizer.Recommendation{
		ID:              fmt.Sprintf("rightsize-combined-%s-%s-%d", pod.Pod.Namespace, pod.Pod.Name, time.Now().Unix()),
		Type:            optimizer.RecommendationPodRightsize,
		Priority:        optimizer.PriorityMedium,
		AutoExecutable:  true,
		TargetKind:      pod.OwnerKind,
		TargetName:      pod.OwnerName,
		TargetNamespace: pod.Pod.Namespace,
		Summary: fmt.Sprintf("Rightsize %s/%s: CPU %dm→%dm, memory %s%s%s (%d replicas)",
			pod.Pod.Namespace, pod.OwnerName,
			analysis.CPURequestMilli, suggestedCPU,
			formatBytes(analysis.MemRequestBytes), memDirection, formatBytes(suggestedMem),
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

// vCPUHourlyCostByCloud returns an approximate per-vCPU hourly cost for the cloud provider.
func vCPUHourlyCostByCloud(cloudProvider string) float64 {
	switch cloudProvider {
	case "gcp":
		return 0.031611 // n2 rate (most common GCP family)
	case "azure":
		return 0.043 // Standard_D rate (most common Azure family)
	default: // aws
		return 0.04 // m5 rate (most common AWS family)
	}
}

func estimateCPUSavings(currentMilli, suggestedMilli int64, cloudProvider string) float64 {
	cpuSaved := float64(currentMilli-suggestedMilli) / 1000.0
	return cpuSaved * vCPUHourlyCostByCloud(cloudProvider) * cost.HoursPerMonth
}

// memGiBHourlyCostByCloud returns an approximate per-GiB-RAM hourly cost.
func memGiBHourlyCostByCloud(cloudProvider string) float64 {
	switch cloudProvider {
	case "gcp":
		return 0.004237 // n2 rate
	case "azure":
		return 0.005 // Standard_D rate
	default: // aws
		return 0.00643 // m5 rate
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
	case b >= gi:
		return fmt.Sprintf("%dGi", b/gi)
	case b >= mi:
		return fmt.Sprintf("%dMi", b/mi)
	case b >= ki:
		return fmt.Sprintf("%dKi", b/ki)
	default:
		return fmt.Sprintf("%d", b)
	}
}

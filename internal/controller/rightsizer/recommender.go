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

	// Determine replica count for scaling per-pod savings
	replicaCount := int64(1)
	if pod.ReplicaCount > 1 {
		replicaCount = int64(pod.ReplicaCount)
	}

	// CPU rightsizing
	if analysis.IsOverProvCPU && analysis.CPUP95 > 0 {
		// Recommend P95 + 20% headroom
		suggestedCPU := int64(float64(analysis.CPUP95) * 1.2)
		if suggestedCPU < 10 { // minimum 10m
			suggestedCPU = 10
		}

		if suggestedCPU < analysis.CPURequestMilli {
			perPodSavings := estimateCPUSavings(analysis.CPURequestMilli, suggestedCPU, r.config.CloudProvider)
			savings := perPodSavings * float64(replicaCount)
			recs = append(recs, optimizer.Recommendation{
				ID:              fmt.Sprintf("rightsize-cpu-%s-%s-%d", pod.Pod.Namespace, pod.Pod.Name, time.Now().Unix()),
				Type:            optimizer.RecommendationPodRightsize,
				Priority:        optimizer.PriorityMedium,
				AutoExecutable:  true,
				TargetKind:      pod.OwnerKind,
				TargetName:      pod.OwnerName,
				TargetNamespace: pod.Pod.Namespace,
				Summary:         fmt.Sprintf("Reduce CPU request for %s/%s from %dm to %dm (%d replicas)", pod.Pod.Namespace, pod.OwnerName, analysis.CPURequestMilli, suggestedCPU, replicaCount),
				ActionSteps: []string{
					fmt.Sprintf("Patch CPU request from %dm to %dm", analysis.CPURequestMilli, suggestedCPU),
				},
				EstimatedSaving: optimizer.SavingEstimate{
					MonthlySavingsUSD: savings,
					AnnualSavingsUSD:  savings * 12,
					Currency:          "USD",
				},
				Details: map[string]string{
					"resource":         "cpu",
					"currentRequest":   fmt.Sprintf("%dm", analysis.CPURequestMilli),
					"suggestedRequest": fmt.Sprintf("%dm", suggestedCPU),
					"p95Usage":         fmt.Sprintf("%dm", analysis.CPUP95),
					"replicaCount":     fmt.Sprintf("%d", replicaCount),
				},
				CreatedAt: time.Now(),
			})
		}
	}

	// Memory rightsizing
	if analysis.IsOverProvMem && analysis.MemP95 > 0 {
		suggestedMem := int64(float64(analysis.MemP95) * 1.2)
		minMem := int64(32 * 1024 * 1024) // 32Mi minimum
		if suggestedMem < minMem {
			suggestedMem = minMem
		}

		if suggestedMem < analysis.MemRequestBytes {
			perPodSavings := estimateMemorySavings(analysis.MemRequestBytes, suggestedMem, r.config.CloudProvider)
			savings := perPodSavings * float64(replicaCount)
			recs = append(recs, optimizer.Recommendation{
				ID:              fmt.Sprintf("rightsize-mem-%s-%s-%d", pod.Pod.Namespace, pod.Pod.Name, time.Now().Unix()),
				Type:            optimizer.RecommendationPodRightsize,
				Priority:        optimizer.PriorityMedium,
				AutoExecutable:  true,
				TargetKind:      pod.OwnerKind,
				TargetName:      pod.OwnerName,
				TargetNamespace: pod.Pod.Namespace,
				Summary:         fmt.Sprintf("Reduce memory request for %s/%s from %s to %s (%d replicas)", pod.Pod.Namespace, pod.OwnerName, formatBytes(analysis.MemRequestBytes), formatBytes(suggestedMem), replicaCount),
				ActionSteps: []string{
					fmt.Sprintf("Patch memory request from %s to %s", formatBytes(analysis.MemRequestBytes), formatBytes(suggestedMem)),
				},
				EstimatedSaving: optimizer.SavingEstimate{
					MonthlySavingsUSD: savings,
					AnnualSavingsUSD:  savings * 12,
					Currency:          "USD",
				},
				Details: map[string]string{
					"resource":         "memory",
					"currentRequest":   formatBytes(analysis.MemRequestBytes),
					"suggestedRequest": formatBytes(suggestedMem),
					"p95Usage":         formatBytes(analysis.MemP95),
					"replicaCount":     fmt.Sprintf("%d", replicaCount),
				},
				CreatedAt: time.Now(),
			})
		}
	}

	// Under-provisioned CPU
	if analysis.IsUnderProvCPU {
		suggestedCPU := int64(float64(analysis.CPUMax) * 1.3)
		recs = append(recs, optimizer.Recommendation{
			ID:              fmt.Sprintf("rightsize-cpuup-%s-%s-%d", pod.Pod.Namespace, pod.Pod.Name, time.Now().Unix()),
			Type:            optimizer.RecommendationPodRightsize,
			Priority:        optimizer.PriorityHigh,
			AutoExecutable:  true,
			TargetKind:      pod.OwnerKind,
			TargetName:      pod.OwnerName,
			TargetNamespace: pod.Pod.Namespace,
			Summary:         fmt.Sprintf("Increase CPU request for %s/%s from %dm to %dm (CPU throttled)", pod.Pod.Namespace, pod.OwnerName, analysis.CPURequestMilli, suggestedCPU),
			ActionSteps: []string{
				fmt.Sprintf("Patch CPU request from %dm to %dm", analysis.CPURequestMilli, suggestedCPU),
			},
			Details: map[string]string{
				"resource":         "cpu",
				"currentRequest":   fmt.Sprintf("%dm", analysis.CPURequestMilli),
				"suggestedRequest": fmt.Sprintf("%dm", suggestedCPU),
				"maxUsage":         fmt.Sprintf("%dm", analysis.CPUMax),
			},
			CreatedAt: time.Now(),
		})
	}

	return recs
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

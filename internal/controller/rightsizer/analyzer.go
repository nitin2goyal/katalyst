package rightsizer

import (
	"context"
	"time"

	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/internal/metrics"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// PodAnalysis contains the resource analysis results for a pod.
type PodAnalysis struct {
	PodInfo         optimizer.PodInfo
	CPURequestMilli int64
	MemRequestBytes int64
	CPUP50          int64
	CPUP95          int64
	CPUP99          int64
	MemP50          int64
	MemP95          int64
	MemP99          int64
	CPUMax          int64
	MemMax          int64
	DataPoints      int
	IsOverProvCPU   bool
	IsOverProvMem   bool
	IsUnderProvCPU  bool
	IsUnderProvMem  bool
}

// Analyzer performs usage pattern analysis on pod metrics.
type Analyzer struct {
	config *config.Config
	store  *metrics.Store
}

func NewAnalyzer(cfg *config.Config, store *metrics.Store) *Analyzer {
	return &Analyzer{config: cfg, store: store}
}

// AnalyzePod analyzes resource usage patterns for a single pod.
func (a *Analyzer) AnalyzePod(ctx context.Context, pod optimizer.PodInfo) *PodAnalysis {
	if pod.CPURequest == 0 && pod.MemoryRequest == 0 {
		return nil
	}

	analysis := &PodAnalysis{
		PodInfo:         pod,
		CPURequestMilli: pod.CPURequest,
		MemRequestBytes: pod.MemoryRequest,
	}

	// Try to get real percentile data from the metrics store for each container.
	// Aggregate across all containers since PodAnalysis is pod-level.
	gotWindowData := false
	if a.store != nil && pod.Pod != nil {
		lookback := a.config.Rightsizer.LookbackWindow
		if lookback == 0 {
			lookback = 7 * 24 * time.Hour
		}
		for _, container := range pod.Pod.Spec.Containers {
			window := a.store.GetPodContainerWindow(pod.Pod.Namespace, pod.Pod.Name, container.Name, lookback)
			if window != nil {
				gotWindowData = true
				analysis.CPUP50 += window.P50CPU
				analysis.CPUP95 += window.P95CPU
				analysis.CPUP99 += window.P99CPU
				analysis.CPUMax += window.MaxCPU
				analysis.MemP50 += window.P50Memory
				analysis.MemP95 += window.P95Memory
				analysis.MemP99 += window.P99Memory
				analysis.MemMax += window.MaxMemory
				// Use the max DataPoints across containers
				if window.DataPoints > analysis.DataPoints {
					analysis.DataPoints = window.DataPoints
				}
			}
		}
	}

	// Fall back to point-in-time values if the store had no data
	if !gotWindowData {
		analysis.CPUP50 = pod.CPUUsage
		analysis.CPUP95 = pod.CPUUsage
		analysis.CPUP99 = pod.CPUUsage
		analysis.CPUMax = pod.CPUUsage
		analysis.MemP50 = pod.MemoryUsage
		analysis.MemP95 = pod.MemoryUsage
		analysis.MemP99 = pod.MemoryUsage
		analysis.MemMax = pod.MemoryUsage
	}

	// Determine if over/under provisioned using P95 values
	cpuUtil := float64(0)
	if pod.CPURequest > 0 {
		cpuUtil = float64(analysis.CPUP95) / float64(pod.CPURequest) * 100
	}
	memUtil := float64(0)
	if pod.MemoryRequest > 0 {
		memUtil = float64(analysis.MemP95) / float64(pod.MemoryRequest) * 100
	}

	analysis.IsOverProvCPU = cpuUtil < a.config.Rightsizer.CPUTargetUtilPct*0.5
	analysis.IsOverProvMem = memUtil < a.config.Rightsizer.MemoryTargetUtilPct*0.5
	analysis.IsUnderProvCPU = cpuUtil > 95
	analysis.IsUnderProvMem = memUtil > 95

	return analysis
}

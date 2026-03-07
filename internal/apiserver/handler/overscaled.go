package handler

import (
	"fmt"
	"math"
	"net/http"
	"sort"

	"github.com/koptimizer/koptimizer/internal/state"
	"github.com/koptimizer/koptimizer/pkg/cost"
)

// OverscaledHandler detects workloads that have been scaled up by HPAs
// but have very low actual CPU/memory utilization, wasting resources.
type OverscaledHandler struct {
	state *state.ClusterState
}

func NewOverscaledHandler(st *state.ClusterState) *OverscaledHandler {
	return &OverscaledHandler{state: st}
}

// Get returns over-scaled workloads — workloads with autoscalers where
// current replicas far exceed what actual usage requires.
func (h *OverscaledHandler) Get(w http.ResponseWriter, r *http.Request) {
	pods := h.state.GetAllPods()
	nodes := h.state.GetAllNodes()

	// Build node cost map
	nodeCostMap := make(map[string]float64)
	nodeCPUCapMap := make(map[string]int64)
	nodeMemCapMap := make(map[string]int64)
	for _, n := range nodes {
		nodeCostMap[n.Node.Name] = n.HourlyCostUSD * cost.HoursPerMonth
		nodeCPUCapMap[n.Node.Name] = n.CPUCapacity
		nodeMemCapMap[n.Node.Name] = n.MemoryCapacity
	}

	// Group pods by owner
	type wlInfo struct {
		Namespace   string
		Kind        string
		Name        string
		Replicas    int
		CPUReqTotal int64 // total CPU request (all replicas)
		CPUUsed     int64 // total CPU usage
		MemReqTotal int64
		MemUsed     int64
		CPUReqPod   int64 // per-pod CPU request
		MemReqPod   int64 // per-pod mem request
		MonthlyCost float64
		HasMetrics  bool
	}
	workloads := make(map[string]*wlInfo)

	for _, p := range pods {
		if p.Pod.Status.Phase != "Running" {
			continue
		}
		ownerKind, ownerName := resolveOwner(p)
		key := p.Namespace + "/" + ownerKind + "/" + ownerName
		wl, ok := workloads[key]
		if !ok {
			wl = &wlInfo{
				Namespace: p.Namespace,
				Kind:      ownerKind,
				Name:      ownerName,
			}
			workloads[key] = wl
		}
		wl.Replicas++
		wl.CPUReqTotal += p.CPURequest
		wl.CPUUsed += p.CPUUsage
		wl.MemReqTotal += p.MemoryRequest
		wl.MemUsed += p.MemoryUsage
		if p.CPUUsage > 0 || p.MemoryUsage > 0 {
			wl.HasMetrics = true
		}

		// Cost allocation
		fraction := 0.0
		if cpuCap := nodeCPUCapMap[p.NodeName]; cpuCap > 0 && p.CPURequest > 0 {
			fraction += 0.5 * float64(p.CPURequest) / float64(cpuCap)
		}
		if memCap := nodeMemCapMap[p.NodeName]; memCap > 0 && p.MemoryRequest > 0 {
			fraction += 0.5 * float64(p.MemoryRequest) / float64(memCap)
		}
		if fraction > 0 {
			wl.MonthlyCost += nodeCostMap[p.NodeName] * fraction
		}
	}

	// Compute per-pod requests
	for _, wl := range workloads {
		if wl.Replicas > 0 {
			wl.CPUReqPod = wl.CPUReqTotal / int64(wl.Replicas)
			wl.MemReqPod = wl.MemReqTotal / int64(wl.Replicas)
		}
	}

	type overscaledEntry struct {
		Namespace       string  `json:"namespace"`
		Kind            string  `json:"kind"`
		Name            string  `json:"name"`
		CurrentReplicas int     `json:"currentReplicas"`
		MinReplicas     int32   `json:"minReplicas"`
		MaxReplicas     int32   `json:"maxReplicas"`
		OptimalReplicas int     `json:"optimalReplicas"`
		ExcessReplicas  int     `json:"excessReplicas"`
		Autoscaler      string  `json:"autoscaler"`
		AutoscalerName  string  `json:"autoscalerName"`
		CPUReqPerPod    string  `json:"cpuRequestPerPod"`
		MemReqPerPod    string  `json:"memRequestPerPod"`
		TotalCPUReq     string  `json:"totalCPURequest"`
		TotalCPUUsage   string  `json:"totalCPUUsage"`
		TotalMemReq     string  `json:"totalMemRequest"`
		TotalMemUsage   string  `json:"totalMemUsage"`
		CPUEffPct       float64 `json:"cpuEfficiencyPct"`
		MemEffPct       float64 `json:"memEfficiencyPct"`
		MonthlyCostUSD  float64 `json:"monthlyCostUSD"`
		WastedCostUSD   float64 `json:"wastedCostUSD"`
		Reason          string  `json:"reason"`
		Severity        string  `json:"severity"`
	}

	var result []overscaledEntry
	totalExcess := 0
	totalWastedCost := 0.0

	for _, wl := range workloads {
		// Must have autoscaler and metrics
		as, hasAS := h.state.GetAutoscaler(wl.Namespace, wl.Kind, wl.Name)
		if !hasAS || !wl.HasMetrics {
			continue
		}
		// Must have at least 2 replicas
		if wl.Replicas < 2 {
			continue
		}

		cpuEff := 0.0
		if wl.CPUReqTotal > 0 {
			cpuEff = float64(wl.CPUUsed) / float64(wl.CPUReqTotal) * 100
		}
		memEff := 0.0
		if wl.MemReqTotal > 0 {
			memEff = float64(wl.MemUsed) / float64(wl.MemReqTotal) * 100
		}

		// Calculate optimal replicas: how many pods needed at 70% target utilization
		targetUtil := 0.7
		optimalByCPU := 1
		if wl.CPUReqPod > 0 {
			optimalByCPU = int(math.Ceil(float64(wl.CPUUsed) / float64(wl.CPUReqPod) / targetUtil))
		}
		optimalByMem := 1
		if wl.MemReqPod > 0 {
			optimalByMem = int(math.Ceil(float64(wl.MemUsed) / float64(wl.MemReqPod) / targetUtil))
		}
		optimal := optimalByCPU
		if optimalByMem > optimal {
			optimal = optimalByMem
		}
		// Floor at min replicas from HPA
		if optimal < int(as.MinReplicas) {
			optimal = int(as.MinReplicas)
		}
		// Floor at 1
		if optimal < 1 {
			optimal = 1
		}

		excess := wl.Replicas - optimal
		if excess <= 0 {
			continue
		}

		// Only flag if at least 30% excess (> current * 0.7 is wasted)
		if float64(excess) < float64(wl.Replicas)*0.3 {
			continue
		}

		// Wasted cost: proportional to excess replicas
		wastedCost := 0.0
		if wl.Replicas > 0 {
			wastedCost = wl.MonthlyCost * float64(excess) / float64(wl.Replicas)
		}

		severity := "info"
		if cpuEff < 5 {
			severity = "critical"
		} else if cpuEff < 20 {
			severity = "warning"
		}

		reason := fmt.Sprintf("%d replicas but %.1f%% CPU utilization — could run on %d replicas",
			wl.Replicas, cpuEff, optimal)

		result = append(result, overscaledEntry{
			Namespace:       wl.Namespace,
			Kind:            wl.Kind,
			Name:            wl.Name,
			CurrentReplicas: wl.Replicas,
			MinReplicas:     as.MinReplicas,
			MaxReplicas:     as.MaxReplicas,
			OptimalReplicas: optimal,
			ExcessReplicas:  excess,
			Autoscaler:      as.Kind,
			AutoscalerName:  as.Name,
			CPUReqPerPod:    formatCPU(wl.CPUReqPod),
			MemReqPerPod:    formatMem(wl.MemReqPod),
			TotalCPUReq:     formatCPU(wl.CPUReqTotal),
			TotalCPUUsage:   formatCPU(wl.CPUUsed),
			TotalMemReq:     formatMem(wl.MemReqTotal),
			TotalMemUsage:   formatMem(wl.MemUsed),
			CPUEffPct:       cpuEff,
			MemEffPct:       memEff,
			MonthlyCostUSD:  wl.MonthlyCost,
			WastedCostUSD:   wastedCost,
			Reason:          reason,
			Severity:        severity,
		})

		totalExcess += excess
		totalWastedCost += wastedCost
	}

	// Sort by wasted cost descending
	sort.Slice(result, func(i, j int) bool {
		return result[i].WastedCostUSD > result[j].WastedCostUSD
	})

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"summary": map[string]interface{}{
			"overscaledCount":    len(result),
			"totalExcessReplicas": totalExcess,
			"totalWastedCostUSD": totalWastedCost,
		},
		"workloads": result,
	})
}

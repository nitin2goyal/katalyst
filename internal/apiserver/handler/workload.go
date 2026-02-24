package handler

import (
	"fmt"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"

	"github.com/koptimizer/koptimizer/internal/state"
	"github.com/koptimizer/koptimizer/pkg/cost"
)

type WorkloadHandler struct {
	state *state.ClusterState
}

func NewWorkloadHandler(st *state.ClusterState) *WorkloadHandler {
	return &WorkloadHandler{state: st}
}

func (h *WorkloadHandler) List(w http.ResponseWriter, r *http.Request) {
	pods := h.state.GetAllPods()
	// Group by owner
	workloads := make(map[string]map[string]interface{})
	for _, p := range pods {
		key := p.Namespace + "/" + p.OwnerKind + "/" + p.OwnerName
		if _, ok := workloads[key]; !ok {
			workloads[key] = map[string]interface{}{
				"namespace": p.Namespace,
				"kind":      p.OwnerKind,
				"name":      p.OwnerName,
				"replicas":  0,
				"totalCPU":  int64(0),
				"totalMem":  int64(0),
			}
		}
		workloads[key]["replicas"] = workloads[key]["replicas"].(int) + 1
		workloads[key]["totalCPU"] = workloads[key]["totalCPU"].(int64) + p.CPURequest
		workloads[key]["totalMem"] = workloads[key]["totalMem"].(int64) + p.MemoryRequest
	}

	var result []map[string]interface{}
	for _, wl := range workloads {
		result = append(result, wl)
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *WorkloadHandler) Get(w http.ResponseWriter, r *http.Request) {
	ns := chi.URLParam(r, "ns")
	kind := chi.URLParam(r, "kind")
	name := chi.URLParam(r, "name")

	pods := h.state.GetAllPods()
	type podDetail struct {
		Name          string `json:"name"`
		Namespace     string `json:"namespace"`
		NodeName      string `json:"nodeName"`
		CPURequest    int64  `json:"cpuRequestMilli"`
		MemoryRequest int64  `json:"memoryRequestBytes"`
		CPUUsage      int64  `json:"cpuUsageMilli"`
		MemoryUsage   int64  `json:"memoryUsageBytes"`
	}
	var matchedPods []podDetail
	totalCPUReq := int64(0)
	totalMemReq := int64(0)

	for _, p := range pods {
		if p.Namespace == ns && p.OwnerKind == kind && p.OwnerName == name {
			matchedPods = append(matchedPods, podDetail{
				Name:          p.Name,
				Namespace:     p.Namespace,
				NodeName:      p.NodeName,
				CPURequest:    p.CPURequest,
				MemoryRequest: p.MemoryRequest,
				CPUUsage:      p.CPUUsage,
				MemoryUsage:   p.MemoryUsage,
			})
			totalCPUReq += p.CPURequest
			totalMemReq += p.MemoryRequest
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"namespace":            ns,
		"kind":                 kind,
		"name":                 name,
		"replicas":             len(matchedPods),
		"totalCPURequestMilli": totalCPUReq,
		"totalMemRequestBytes": totalMemReq,
		"pods":                 matchedPods,
	})
}

func (h *WorkloadHandler) GetRightsizing(w http.ResponseWriter, r *http.Request) {
	ns := chi.URLParam(r, "ns")
	kind := chi.URLParam(r, "kind")
	name := chi.URLParam(r, "name")

	pods := h.state.GetAllPods()
	type podUtilization struct {
		Name          string  `json:"name"`
		CPURequest    int64   `json:"cpuRequestMilli"`
		MemoryRequest int64   `json:"memoryRequestBytes"`
		CPUUsage      int64   `json:"cpuUsageMilli"`
		MemoryUsage   int64   `json:"memoryUsageBytes"`
		CPUUtilPct    float64 `json:"cpuUtilizationPct"`
		MemoryUtilPct float64 `json:"memoryUtilizationPct"`
	}
	var result []podUtilization

	for _, p := range pods {
		if p.Namespace == ns && p.OwnerKind == kind && p.OwnerName == name {
			cpuUtil := float64(0)
			if p.CPURequest > 0 {
				cpuUtil = float64(p.CPUUsage) / float64(p.CPURequest) * 100
			}
			memUtil := float64(0)
			if p.MemoryRequest > 0 {
				memUtil = float64(p.MemoryUsage) / float64(p.MemoryRequest) * 100
			}
			result = append(result, podUtilization{
				Name:          p.Name,
				CPURequest:    p.CPURequest,
				MemoryRequest: p.MemoryRequest,
				CPUUsage:      p.CPUUsage,
				MemoryUsage:   p.MemoryUsage,
				CPUUtilPct:    cpuUtil,
				MemoryUtilPct: memUtil,
			})
		}
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *WorkloadHandler) GetScaling(w http.ResponseWriter, r *http.Request) {
	ns := chi.URLParam(r, "ns")
	kind := chi.URLParam(r, "kind")
	name := chi.URLParam(r, "name")

	pods := h.state.GetAllPods()
	replicaCount := 0
	totalCPUUsage := int64(0)
	totalCPUReq := int64(0)

	for _, p := range pods {
		if p.Namespace == ns && p.OwnerKind == kind && p.OwnerName == name {
			replicaCount++
			totalCPUUsage += p.CPUUsage
			totalCPUReq += p.CPURequest
		}
	}

	avgCPUUtil := float64(0)
	if totalCPUReq > 0 {
		avgCPUUtil = float64(totalCPUUsage) / float64(totalCPUReq) * 100
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"namespace":            ns,
		"kind":                 kind,
		"name":                 name,
		"currentReplicas":      replicaCount,
		"avgCPUUtilizationPct": avgCPUUtil,
	})
}

// GetEfficiency returns per-workload efficiency analysis with waste identification.
func (h *WorkloadHandler) GetEfficiency(w http.ResponseWriter, r *http.Request) {
	pods := h.state.GetAllPods()
	nodes := h.state.GetAllNodes()

	// Build node cost map for cost allocation
	nodeCostMap := make(map[string]float64)
	nodeCPUReqMap := make(map[string]int64)
	for _, n := range nodes {
		nodeCostMap[n.Node.Name] = n.HourlyCostUSD * cost.HoursPerMonth
		nodeCPUReqMap[n.Node.Name] = n.CPURequested
	}

	// Group pods by owner
	type workloadInfo struct {
		Namespace  string
		Kind       string
		Name       string
		Replicas   int
		CPUReq     int64
		CPUUsed    int64
		MemReq     int64
		MemUsed    int64
		MonthlyCost float64
	}
	workloads := make(map[string]*workloadInfo)

	for _, p := range pods {
		ownerKind, ownerName := p.OwnerKind, p.OwnerName
		if ownerName == "" {
			ownerName = p.Name
			ownerKind = "Pod"
		}
		key := p.Namespace + "/" + ownerKind + "/" + ownerName
		wl, ok := workloads[key]
		if !ok {
			wl = &workloadInfo{
				Namespace: p.Namespace,
				Kind:      ownerKind,
				Name:      ownerName,
			}
			workloads[key] = wl
		}
		wl.Replicas++
		wl.CPUReq += p.CPURequest
		wl.CPUUsed += p.CPUUsage
		wl.MemReq += p.MemoryRequest
		wl.MemUsed += p.MemoryUsage

		// Allocate cost from node proportionally
		nodeTotalReq := nodeCPUReqMap[p.NodeName]
		if nodeTotalReq > 0 && p.CPURequest > 0 {
			fraction := float64(p.CPURequest) / float64(nodeTotalReq)
			wl.MonthlyCost += nodeCostMap[p.NodeName] * fraction
		}
	}

	type workloadEfficiency struct {
		Namespace        string  `json:"namespace"`
		Kind             string  `json:"kind"`
		Name             string  `json:"name"`
		Replicas         int     `json:"replicas"`
		CPURequest       string  `json:"cpuRequest"`
		CPUUsed          string  `json:"cpuUsed"`
		MemRequest       string  `json:"memRequest"`
		MemUsed          string  `json:"memUsed"`
		CPUEfficiencyPct float64 `json:"cpuEfficiencyPct"`
		MemEfficiencyPct float64 `json:"memEfficiencyPct"`
		WastedCPU        string  `json:"wastedCPU"`
		WastedMem        string  `json:"wastedMem"`
		MonthlyCostUSD   float64 `json:"monthlyCostUSD"`
		WastedCostUSD    float64 `json:"wastedCostUSD"`
	}

	var result []workloadEfficiency
	totalWastedCost := 0.0
	sumCPUEff := 0.0
	sumMemEff := 0.0
	count := 0

	for _, wl := range workloads {
		cpuEff := 0.0
		if wl.CPUReq > 0 {
			cpuEff = float64(wl.CPUUsed) / float64(wl.CPUReq) * 100
		}
		memEff := 0.0
		if wl.MemReq > 0 {
			memEff = float64(wl.MemUsed) / float64(wl.MemReq) * 100
		}

		wastedCPU := wl.CPUReq - wl.CPUUsed
		if wastedCPU < 0 {
			wastedCPU = 0
		}
		wastedMem := wl.MemReq - wl.MemUsed
		if wastedMem < 0 {
			wastedMem = 0
		}

		// Wasted cost: proportional to unused CPU fraction
		wastedCost := 0.0
		if wl.CPUReq > 0 {
			wastedCost = wl.MonthlyCost * (1.0 - float64(wl.CPUUsed)/float64(wl.CPUReq))
			if wastedCost < 0 {
				wastedCost = 0
			}
		}
		totalWastedCost += wastedCost

		result = append(result, workloadEfficiency{
			Namespace:        wl.Namespace,
			Kind:             wl.Kind,
			Name:             wl.Name,
			Replicas:         wl.Replicas,
			CPURequest:       formatCPU(wl.CPUReq),
			CPUUsed:          formatCPU(wl.CPUUsed),
			MemRequest:       formatMem(wl.MemReq),
			MemUsed:          formatMem(wl.MemUsed),
			CPUEfficiencyPct: cpuEff,
			MemEfficiencyPct: memEff,
			WastedCPU:        formatCPU(wastedCPU),
			WastedMem:        formatMem(wastedMem),
			MonthlyCostUSD:   wl.MonthlyCost,
			WastedCostUSD:    wastedCost,
		})
		sumCPUEff += cpuEff
		sumMemEff += memEff
		count++
	}

	// Sort by wasted cost descending
	sort.Slice(result, func(i, j int) bool {
		return result[i].WastedCostUSD > result[j].WastedCostUSD
	})

	avgCPUEff := 0.0
	avgMemEff := 0.0
	if count > 0 {
		avgCPUEff = sumCPUEff / float64(count)
		avgMemEff = sumMemEff / float64(count)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"summary": map[string]interface{}{
			"avgCPUEfficiency":   avgCPUEff,
			"avgMemEfficiency":   avgMemEff,
			"totalWastedCostUSD": totalWastedCost,
		},
		"workloads": result,
	})
}

// formatCPU formats millicores to K8s-style string (e.g., 100 -> "100m").
func formatCPU(millis int64) string {
	return fmt.Sprintf("%dm", millis)
}

// formatMem formats bytes to human-readable K8s-style string.
func formatMem(bytes int64) string {
	const (
		gi = 1024 * 1024 * 1024
		mi = 1024 * 1024
	)
	if bytes >= gi && bytes%gi == 0 {
		return fmt.Sprintf("%dGi", bytes/gi)
	}
	return fmt.Sprintf("%dMi", bytes/mi)
}

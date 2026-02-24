package handler

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	koptv1alpha1 "github.com/koptimizer/koptimizer/api/v1alpha1"
	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/internal/state"
	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
	"github.com/koptimizer/koptimizer/pkg/cost"
)

type ClusterHandler struct {
	state    *state.ClusterState
	provider cloudprovider.CloudProvider
	config   *config.Config
	client   client.Client
}

func NewClusterHandler(st *state.ClusterState, provider cloudprovider.CloudProvider, cfg *config.Config, c client.Client) *ClusterHandler {
	return &ClusterHandler{state: st, provider: provider, config: cfg, client: c}
}

func (h *ClusterHandler) GetSummary(w http.ResponseWriter, r *http.Request) {
	nodes := h.state.GetAllNodes()
	pods := h.state.GetAllPods()
	nodeGroups := h.state.GetNodeGroups().GetAll()

	var totalCPU, totalMem, usedCPU, usedMem, reqCPU, reqMem int64
	totalCost := 0.0

	for _, n := range nodes {
		totalCPU += n.CPUCapacity
		totalMem += n.MemoryCapacity
		usedCPU += n.CPUUsed
		usedMem += n.MemoryUsed
		reqCPU += n.CPURequested
		reqMem += n.MemoryRequested
		totalCost += n.HourlyCostUSD
	}

	cpuUtil := safePct(usedCPU, totalCPU)
	memUtil := safePct(usedMem, totalMem)
	cpuAlloc := safePct(reqCPU, totalCPU)
	memAlloc := safePct(reqMem, totalMem)

	resp := map[string]interface{}{
		"mode":              h.config.Mode,
		"cloudProvider":     h.config.CloudProvider,
		"nodeCount":         len(nodes),
		"podCount":          len(pods),
		"nodeGroupCount":    len(nodeGroups),
		"cpuUtilizationPct": cpuUtil,
		"memUtilizationPct": memUtil,
		"cpuAllocationPct":  cpuAlloc,
		"memAllocationPct":  memAlloc,
		"monthlyCostUSD":    totalCost * cost.HoursPerMonth,
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *ClusterHandler) GetHealth(w http.ResponseWriter, r *http.Request) {
	resp := map[string]interface{}{
		"status": "healthy",
		"mode":   h.config.Mode,
		"controllers": map[string]string{
			"costMonitor":    boolToStatus(h.config.CostMonitor.Enabled),
			"nodeAutoscaler": boolToStatus(h.config.NodeAutoscaler.Enabled),
			"nodegroupMgr":   boolToStatus(h.config.NodeGroupMgr.Enabled),
			"rightsizer":     boolToStatus(h.config.Rightsizer.Enabled),
			"workloadScaler": boolToStatus(h.config.WorkloadScaler.Enabled),
			"evictor":        boolToStatus(h.config.Evictor.Enabled),
			"rebalancer":     boolToStatus(h.config.Rebalancer.Enabled),
			"gpu":            boolToStatus(h.config.GPU.Enabled),
			"commitments":    boolToStatus(h.config.Commitments.Enabled),
			"aiGate":         boolToStatus(h.config.AIGate.Enabled),
		},
	}
	writeJSON(w, http.StatusOK, resp)
}

// GetEfficiency returns a cluster efficiency score and breakdown.
func (h *ClusterHandler) GetEfficiency(w http.ResponseWriter, r *http.Request) {
	nodes := h.state.GetAllNodes()

	totalCPU := int64(0)
	totalMem := int64(0)
	usedCPU := int64(0)
	usedMem := int64(0)

	for _, n := range nodes {
		totalCPU += n.CPUCapacity
		totalMem += n.MemoryCapacity
		usedCPU += n.CPUUsed
		usedMem += n.MemoryUsed
	}

	cpuUtil := 0.0
	if totalCPU > 0 {
		cpuUtil = float64(usedCPU) / float64(totalCPU) * 100
	}
	memUtil := 0.0
	if totalMem > 0 {
		memUtil = float64(usedMem) / float64(totalMem) * 100
	}

	// Spot savings score: ratio of spot nodes (more spot = better savings capture).
	spotNodes := 0
	for _, n := range nodes {
		if n.IsSpot {
			spotNodes++
		}
	}
	savingsScore := 0.0
	if len(nodes) > 0 {
		spotRatio := float64(spotNodes) / float64(len(nodes))
		// Score: 70% spot penetration = 100 score, linear
		savingsScore = spotRatio / 0.70 * 100
		if savingsScore > 100 {
			savingsScore = 100
		}
	}

	// Commitment utilization: based on node group utilization as a proxy.
	// High overall resource utilization means commitments (if any) are well-used.
	commitScore := 0.0
	if cpuUtil > 0 || memUtil > 0 {
		commitScore = (cpuUtil + memUtil) / 2
	}

	score := cpuUtil*0.30 + memUtil*0.30 + savingsScore*0.20 + commitScore*0.20

	resp := map[string]interface{}{
		"score": score,
		"breakdown": map[string]interface{}{
			"cpu":         cpuUtil,
			"memory":      memUtil,
			"savings":     savingsScore,
			"commitments": commitScore,
		},
	}
	writeJSON(w, http.StatusOK, resp)
}

// GetScore returns a cluster optimization score with category breakdown,
// computed from real cluster state data.
func (h *ClusterHandler) GetScore(w http.ResponseWriter, r *http.Request) {
	nodes := h.state.GetAllNodes()
	pods := h.state.GetAllPods()
	totalNodes := len(nodes)

	// Aggregate cluster-wide metrics from real node state.
	var totalCPU, totalMem, usedCPU, usedMem, reqCPU, reqMem int64
	var spotCount, underutilCount int

	for _, n := range nodes {
		totalCPU += n.CPUCapacity
		totalMem += n.MemoryCapacity
		usedCPU += n.CPUUsed
		usedMem += n.MemoryUsed
		reqCPU += n.CPURequested
		reqMem += n.MemoryRequested
		if n.IsSpot {
			spotCount++
		}
		if n.IsUnderutilized(50.0) {
			underutilCount++
		}
	}

	cpuUtil := safePct(usedCPU, totalCPU)
	memUtil := safePct(usedMem, totalMem)
	cpuAlloc := safePct(reqCPU, totalCPU)
	memAlloc := safePct(reqMem, totalMem)
	spotPct := safePctInt(spotCount, totalNodes)

	// Count over-provisioned pods (using <50% of their requests).
	var overProvCPU, overProvMem, rightsizable int
	for _, p := range pods {
		cpuOver := p.CPURequest > 0 && p.CPUEfficiency() < 0.5
		memOver := p.MemoryRequest > 0 && p.MemoryEfficiency() < 0.5
		if cpuOver {
			overProvCPU++
		}
		if memOver {
			overProvMem++
		}
		if cpuOver && memOver {
			rightsizable++
		}
	}

	// ── 1. Provisioning Score (0-10) ──
	provScore := 10.0
	var provFindings []string
	if totalNodes > 0 {
		underutilRatio := float64(underutilCount) / float64(totalNodes)
		if underutilCount > 0 {
			provScore -= underutilRatio * 4
			provFindings = append(provFindings,
				fmt.Sprintf("%d of %d nodes have CPU utilization below 50%%", underutilCount, totalNodes))
		}
		if spotPct < 30 {
			provScore -= (30 - spotPct) / 30 * 1.5
			provFindings = append(provFindings,
				fmt.Sprintf("Spot instances cover %.0f%% of nodes (target: 30%%+)", spotPct))
		}
	}
	provScore = clampScore(provScore)
	provDetails := "Node groups are well-sized with good utilization"
	if underutilCount > 0 {
		provDetails = fmt.Sprintf("%d nodes underutilized, spot coverage at %.0f%%", underutilCount, spotPct)
	}

	// ── 2. Workload Optimization Score (0-10) ──
	wlScore := 10.0
	var wlFindings []string
	totalPods := len(pods)
	if totalPods > 0 {
		overProvCPURatio := float64(overProvCPU) / float64(totalPods)
		overProvMemRatio := float64(overProvMem) / float64(totalPods)
		wlScore -= overProvCPURatio * 3
		wlScore -= overProvMemRatio * 2
		if overProvCPU > 0 {
			wlFindings = append(wlFindings,
				fmt.Sprintf("%d workloads have CPU requests >2x actual usage", overProvCPU))
		}
		if overProvMem > 0 {
			wlFindings = append(wlFindings,
				fmt.Sprintf("%d workloads have memory requests >2x actual usage", overProvMem))
		}
		if rightsizable > 0 {
			wlFindings = append(wlFindings,
				fmt.Sprintf("%d pods are candidates for rightsizing", rightsizable))
		}
	}
	wlScore = clampScore(wlScore)
	wlDetails := "Workload resource requests are well-tuned"
	if overProvCPU > 0 || overProvMem > 0 {
		wlDetails = "Several workloads have significant gaps between requests and actual usage"
	}

	// ── 3. Cost Efficiency Score (0-10) ──
	ceScore := 10.0
	var ceFindings []string
	avgUtil := (cpuUtil + memUtil) / 2
	if avgUtil < 70 {
		ceScore -= (70 - avgUtil) / 70 * 3
	}
	if spotPct < 20 {
		ceScore -= (20 - spotPct) / 20 * 2
		ceFindings = append(ceFindings,
			fmt.Sprintf("Only %.0f%% spot nodes — increasing spot coverage reduces costs", spotPct))
	}
	ceScore = clampScore(ceScore)
	ceDetails := fmt.Sprintf("Average resource utilization at %.1f%%", avgUtil)

	// ── 4. Resource Allocation Score (0-10) ──
	raScore := 10.0
	var raFindings []string
	cpuGap := cpuAlloc - cpuUtil
	memGap := memAlloc - memUtil
	if cpuGap > 5 {
		raScore -= cpuGap / 30 * 4
		raFindings = append(raFindings,
			fmt.Sprintf("CPU allocation at %.0f%% but utilization only %.0f%% — %.0f%% over-provisioned",
				cpuAlloc, cpuUtil, cpuGap))
	}
	if memGap > 5 {
		raScore -= memGap / 30 * 3
		raFindings = append(raFindings,
			fmt.Sprintf("Memory allocation at %.0f%% but utilization only %.0f%% — %.0f%% over-provisioned",
				memAlloc, memUtil, memGap))
	}
	if cpuGap > 10 || memGap > 10 {
		raFindings = append(raFindings, "Tighter resource requests could free capacity and reduce costs")
	}
	raScore = clampScore(raScore)
	raDetails := "Resource allocation closely matches actual usage"
	if cpuGap > 10 || memGap > 10 {
		raDetails = "Significant gap between allocated resources and actual usage indicates over-provisioning"
	}

	overallScore := (provScore + wlScore + ceScore + raScore) / 4

	resp := map[string]interface{}{
		"overallScore": roundTo1(overallScore),
		"maxScore":     10.0,
		"categories": map[string]interface{}{
			"provisioning": map[string]interface{}{
				"score": roundTo1(provScore), "maxScore": 10,
				"details": provDetails, "findings": provFindings,
			},
			"workloadOptimization": map[string]interface{}{
				"score": roundTo1(wlScore), "maxScore": 10,
				"details": wlDetails, "findings": wlFindings,
			},
			"costEfficiency": map[string]interface{}{
				"score": roundTo1(ceScore), "maxScore": 10,
				"details": ceDetails, "findings": ceFindings,
			},
			"resourceAllocation": map[string]interface{}{
				"score": roundTo1(raScore), "maxScore": 10,
				"details": raDetails, "findings": raFindings,
			},
		},
	}
	writeJSON(w, http.StatusOK, resp)
}

func boolToStatus(b bool) string {
	if b {
		return "enabled"
	}
	return "disabled"
}

func safePct(used, total int64) float64 {
	if total == 0 {
		return 0
	}
	return float64(used) / float64(total) * 100
}

func safePctInt(count, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(count) / float64(total) * 100
}

func clampScore(s float64) float64 {
	if s < 0 {
		return 0
	}
	if s > 10 {
		return 10
	}
	return s
}

func roundTo1(f float64) float64 {
	return math.Round(f*10) / 10
}

// GetClusters returns cluster info wrapped in {"clusters": [...]}.
func (h *ClusterHandler) GetClusters(w http.ResponseWriter, r *http.Request) {
	nodes := h.state.GetAllNodes()
	pods := h.state.GetAllPods()

	var totalCPU, totalMem, usedCPU, usedMem int64
	totalCost := 0.0

	// Get K8s version from first node
	k8sVersion := "unknown"
	for _, n := range nodes {
		totalCPU += n.CPUCapacity
		totalMem += n.MemoryCapacity
		usedCPU += n.CPUUsed
		usedMem += n.MemoryUsed
		totalCost += n.HourlyCostUSD
		if k8sVersion == "unknown" && n.Node.Status.NodeInfo.KubeletVersion != "" {
			k8sVersion = n.Node.Status.NodeInfo.KubeletVersion
		}
	}

	cpuUtil := safePct(usedCPU, totalCPU)
	memUtil := safePct(usedMem, totalMem)
	efficiencyScore := (cpuUtil + memUtil) / 2

	// Fetch potential savings from Recommendation CRDs
	potentialSavings := 0.0
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var recList koptv1alpha1.RecommendationList
	if err := h.client.List(ctx, &recList, client.InNamespace("koptimizer-system")); err == nil {
		for _, rec := range recList.Items {
			if rec.Status.State == "pending" || rec.Status.State == "approved" || rec.Status.State == "" {
				potentialSavings += rec.Spec.EstimatedSaving.MonthlySavingsUSD
			}
		}
	}

	clusterName := h.config.ClusterName
	if clusterName == "" {
		clusterName = "default"
	}

	cluster := map[string]interface{}{
		"id":               clusterName,
		"name":             clusterName,
		"provider":         h.config.CloudProvider,
		"region":           h.config.Region,
		"version":          k8sVersion,
		"nodeCount":        len(nodes),
		"podCount":         len(pods),
		"monthlyCostUSD":   totalCost * cost.HoursPerMonth,
		"potentialSavings": potentialSavings,
		"efficiencyScore":  roundTo1(efficiencyScore),
		"status":           "healthy",
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"clusters": []map[string]interface{}{cluster},
	})
}

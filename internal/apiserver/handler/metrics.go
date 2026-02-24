package handler

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	koptv1alpha1 "github.com/koptimizer/koptimizer/api/v1alpha1"
	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/internal/state"
	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
	"github.com/koptimizer/koptimizer/pkg/cost"
)

// MetricsHandler exposes Prometheus-format metrics from real cluster state.
type MetricsHandler struct {
	state    *state.ClusterState
	provider cloudprovider.CloudProvider
	client   client.Client
	cfg      *config.Config
}

// NewMetricsHandler creates a new MetricsHandler.
func NewMetricsHandler(st *state.ClusterState, provider cloudprovider.CloudProvider, c client.Client, cfg *config.Config) *MetricsHandler {
	return &MetricsHandler{state: st, provider: provider, client: c, cfg: cfg}
}

// Get writes Prometheus text format metrics.
func (h *MetricsHandler) Get(w http.ResponseWriter, r *http.Request) {
	nodes := h.state.GetAllNodes()
	pods := h.state.GetAllPods()
	nodeGroups := h.state.GetNodeGroups().GetAll()

	var b strings.Builder

	// Cluster-level metrics
	var totalCPU, totalMem, usedCPU, usedMem, reqCPU, reqMem int64
	totalHourlyCost := 0.0
	spotNodes := 0
	gpuNodes := 0
	var totalGPUUtil float64
	gpuNodeCount := 0

	for _, n := range nodes {
		totalCPU += n.CPUCapacity
		totalMem += n.MemoryCapacity
		usedCPU += n.CPUUsed
		usedMem += n.MemoryUsed
		reqCPU += n.CPURequested
		reqMem += n.MemoryRequested
		totalHourlyCost += n.HourlyCostUSD
		if n.IsSpot {
			spotNodes++
		}
		if n.IsGPUNode {
			gpuNodes++
			if n.GPUCapacity > 0 {
				totalGPUUtil += float64(n.GPUsUsed) / float64(n.GPUCapacity) * 100
				gpuNodeCount++
			}
		}
	}

	writeProm(&b, "koptimizer_cluster_nodes_total", "gauge", "Total number of nodes in the cluster", float64(len(nodes)))
	writeProm(&b, "koptimizer_cluster_pods_total", "gauge", "Total number of running pods", float64(len(pods)))
	writeProm(&b, "koptimizer_cluster_nodegroups_total", "gauge", "Total number of node groups", float64(len(nodeGroups)))
	writeProm(&b, "koptimizer_cluster_spot_nodes_total", "gauge", "Total number of spot nodes", float64(spotNodes))
	writeProm(&b, "koptimizer_cluster_gpu_nodes_total", "gauge", "Total number of GPU nodes", float64(gpuNodes))

	// Cost metrics
	monthlyCost := totalHourlyCost * cost.HoursPerMonth
	writeProm(&b, "koptimizer_cluster_monthly_cost_usd", "gauge", "Projected monthly cost in USD", monthlyCost)

	// Potential savings from recommendations
	potentialSavings := 0.0
	recByStatus := make(map[string]int)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var recList koptv1alpha1.RecommendationList
	if err := h.client.List(ctx, &recList, client.InNamespace("koptimizer-system")); err != nil {
		slog.Warn("metrics: failed to list recommendations", "error", err)
	} else {
		for _, rec := range recList.Items {
			potentialSavings += rec.Spec.EstimatedSaving.MonthlySavingsUSD
			st := rec.Status.State
			if st == "" {
				st = "pending"
			}
			recByStatus[st]++
		}
	}
	writeProm(&b, "koptimizer_cluster_potential_savings_usd", "gauge", "Identified potential monthly savings in USD", potentialSavings)

	// CPU & memory utilization
	cpuUtilPct := 0.0
	memUtilPct := 0.0
	if totalCPU > 0 {
		cpuUtilPct = float64(usedCPU) / float64(totalCPU) * 100
	}
	if totalMem > 0 {
		memUtilPct = float64(usedMem) / float64(totalMem) * 100
	}
	writeProm(&b, "koptimizer_cluster_cpu_utilization_pct", "gauge", "Cluster CPU utilization percentage", cpuUtilPct)
	writeProm(&b, "koptimizer_cluster_memory_utilization_pct", "gauge", "Cluster memory utilization percentage", memUtilPct)

	// Efficiency and optimization scores
	efficiencyScore := (cpuUtilPct + memUtilPct) / 2
	writeProm(&b, "koptimizer_cluster_efficiency_score", "gauge", "Cluster efficiency score 0-100", efficiencyScore)

	// Optimization score (0-10)
	optScore := efficiencyScore / 10
	if optScore > 10 {
		optScore = 10
	}
	if optScore < 0 {
		optScore = 0
	}
	writeProm(&b, "koptimizer_cluster_score", "gauge", "Cluster optimization score 0-10", optScore)

	// Recommendations by status
	fmt.Fprintf(&b, "# HELP koptimizer_recommendations_total Total recommendations by status\n")
	fmt.Fprintf(&b, "# TYPE koptimizer_recommendations_total gauge\n")
	for status, count := range recByStatus {
		fmt.Fprintf(&b, "koptimizer_recommendations_total{status=\"%s\"} %d\n", status, count)
	}

	// Per-nodegroup metrics
	fmt.Fprintf(&b, "# HELP koptimizer_nodegroup_nodes Node count per node group\n")
	fmt.Fprintf(&b, "# TYPE koptimizer_nodegroup_nodes gauge\n")
	for _, ng := range nodeGroups {
		fmt.Fprintf(&b, "koptimizer_nodegroup_nodes{name=\"%s\"} %d\n", ng.Name, len(ng.Nodes))
	}

	fmt.Fprintf(&b, "# HELP koptimizer_nodegroup_cpu_utilization_pct CPU utilization per node group\n")
	fmt.Fprintf(&b, "# TYPE koptimizer_nodegroup_cpu_utilization_pct gauge\n")
	for _, ng := range nodeGroups {
		fmt.Fprintf(&b, "koptimizer_nodegroup_cpu_utilization_pct{name=\"%s\"} %g\n", ng.Name, ng.CPUUtilization())
	}

	// Commitment utilization
	commitments := collectCommitments(ctx, h.provider)
	if len(commitments) > 0 {
		fmt.Fprintf(&b, "# HELP koptimizer_commitment_utilization_pct Commitment utilization percentage\n")
		fmt.Fprintf(&b, "# TYPE koptimizer_commitment_utilization_pct gauge\n")
		for _, c := range commitments {
			fmt.Fprintf(&b, "koptimizer_commitment_utilization_pct{id=\"%s\",type=\"%s\"} %g\n", c.ID, c.Type, c.UtilizationPct)
		}
	}

	// Network cross-AZ cost estimate
	crossAZNodes := 0
	azs := make(map[string]bool)
	for _, n := range nodes {
		az := "unknown"
		if n.Node.Labels != nil {
			if z, ok := n.Node.Labels["topology.kubernetes.io/zone"]; ok {
				az = z
			}
		}
		azs[az] = true
	}
	if len(azs) > 1 {
		crossAZNodes = len(nodes)
	}
	// Use config values matching network handler logic
	trafficPerNode := h.cfg.NetworkMonitor.TrafficPerNodeGBPerHour
	if trafficPerNode == 0 {
		trafficPerNode = 5.0
	}
	crossAZCostPerGB := h.cfg.NetworkMonitor.CrossAZCostPerGBUSD
	if crossAZCostPerGB == 0 {
		crossAZCostPerGB = 0.01
	}
	crossAZCost := float64(crossAZNodes) * trafficPerNode * cost.HoursPerMonth * crossAZCostPerGB
	writeProm(&b, "koptimizer_network_cross_az_cost_usd", "gauge", "Monthly cross-AZ traffic cost", crossAZCost)

	// GPU utilization
	gpuUtil := 0.0
	if gpuNodeCount > 0 {
		gpuUtil = totalGPUUtil / float64(gpuNodeCount)
	}
	writeProm(&b, "koptimizer_gpu_utilization_pct", "gauge", "GPU utilization percentage", gpuUtil)

	// Spot ratio
	if len(nodes) > 0 {
		writeProm(&b, "koptimizer_spot_ratio", "gauge", "Ratio of spot nodes to total nodes", float64(spotNodes)/float64(len(nodes)))
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, b.String())
}

// collectCommitments gathers all commitment types from the cloud provider.
func collectCommitments(ctx context.Context, provider cloudprovider.CloudProvider) []*cloudprovider.Commitment {
	var all []*cloudprovider.Commitment
	if ris, err := provider.GetReservedInstances(ctx); err == nil {
		all = append(all, ris...)
	}
	if sps, err := provider.GetSavingsPlans(ctx); err == nil {
		all = append(all, sps...)
	}
	if cuds, err := provider.GetCommittedUseDiscounts(ctx); err == nil {
		all = append(all, cuds...)
	}
	if res, err := provider.GetReservations(ctx); err == nil {
		all = append(all, res...)
	}
	return all
}

func writeProm(b *strings.Builder, name, metricType, help string, value float64) {
	fmt.Fprintf(b, "# HELP %s %s\n", name, help)
	fmt.Fprintf(b, "# TYPE %s %s\n", name, metricType)
	fmt.Fprintf(b, "%s %g\n", name, value)
}

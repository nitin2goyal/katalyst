package handler

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/internal/state"
	"github.com/koptimizer/koptimizer/pkg/cost"
)

type NetworkHandler struct {
	state *state.ClusterState
	cfg   *config.Config
}

func NewNetworkHandler(st *state.ClusterState, cfg *config.Config) *NetworkHandler {
	return &NetworkHandler{state: st, cfg: cfg}
}

func (h *NetworkHandler) GetSummary(w http.ResponseWriter, r *http.Request) {
	nodes := h.state.GetAllNodes()

	// Build AZ distribution
	azNodes := make(map[string]int)
	for _, n := range nodes {
		az := nodeAZ(n)
		azNodes[az]++
	}

	// Estimate cross-AZ potential: if pods are spread across AZs,
	// there's potential for cross-AZ traffic optimization
	azCount := len(azNodes)
	crossAZRisk := "low"
	if azCount >= 3 {
		crossAZRisk = "high"
	} else if azCount >= 2 {
		crossAZRisk = "medium"
	}

	crossAZCostPerGB := h.cfg.NetworkMonitor.CrossAZCostPerGBUSD
	if crossAZCostPerGB == 0 {
		crossAZCostPerGB = 0.01
	}
	trafficPerNode := h.cfg.NetworkMonitor.TrafficPerNodeGBPerHour
	if trafficPerNode == 0 {
		trafficPerNode = 5.0
	}

	estimatedCrossAZMonthly := float64(len(nodes)) * trafficPerNode * cost.HoursPerMonth * crossAZCostPerGB

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"availabilityZones":       azNodes,
		"azCount":                 azCount,
		"crossAZRisk":             crossAZRisk,
		"estimatedCrossAZCostUSD": estimatedCrossAZMonthly,
		"recommendation":          fmt.Sprintf("Cluster spans %d AZs. Enable topology-aware routing to reduce cross-AZ traffic.", azCount),
	})
}

// GetCost returns detailed cross-AZ network traffic cost estimates.
func (h *NetworkHandler) GetCost(w http.ResponseWriter, r *http.Request) {
	nodes := h.state.GetAllNodes()
	pods := h.state.GetAllPods()

	crossAZCostPerGB := h.cfg.NetworkMonitor.CrossAZCostPerGBUSD
	if crossAZCostPerGB == 0 {
		crossAZCostPerGB = 0.01
	}
	trafficPerNode := h.cfg.NetworkMonitor.TrafficPerNodeGBPerHour
	if trafficPerNode == 0 {
		trafficPerNode = 5.0
	}

	// Build node→AZ mapping
	nodeAZMap := make(map[string]string, len(nodes))
	for _, n := range nodes {
		nodeAZMap[n.Node.Name] = nodeAZ(n)
	}

	// Group pods by namespace/owner and track AZ spread
	type workloadAZInfo struct {
		Namespace string
		Kind      string
		Name      string
		AZPods    map[string]int // az -> pod count
	}
	workloads := make(map[string]*workloadAZInfo)
	for _, p := range pods {
		ownerKind, ownerName := p.OwnerKind, p.OwnerName
		if ownerName == "" {
			ownerName = p.Name
			ownerKind = "Pod"
		}
		key := p.Namespace + "/" + ownerKind + "/" + ownerName
		wl, ok := workloads[key]
		if !ok {
			wl = &workloadAZInfo{
				Namespace: p.Namespace,
				Kind:      ownerKind,
				Name:      ownerName,
				AZPods:    make(map[string]int),
			}
			workloads[key] = wl
		}
		az := nodeAZMap[p.NodeName]
		if az == "" {
			az = "unknown"
		}
		wl.AZPods[az]++
	}

	// Identify cross-AZ workloads and estimate per-workload flows
	type azFlow struct {
		Namespace      string  `json:"namespace"`
		Workload       string  `json:"workload"`
		SourceAZ       string  `json:"sourceAZ"`
		DestAZ         string  `json:"destAZ"`
		TrafficGB      float64 `json:"trafficGB"`
		MonthlyCostUSD float64 `json:"monthlyCostUSD"`
	}

	var flows []azFlow
	totalCrossAZCost := 0.0
	totalInAZTrafficGB := 0.0

	for _, wl := range workloads {
		totalPods := 0
		for _, count := range wl.AZPods {
			totalPods += count
		}

		if len(wl.AZPods) <= 1 {
			// Single-AZ workload: all traffic is in-AZ (free on most cloud providers)
			totalInAZTrafficGB += float64(totalPods) * trafficPerNode * cost.HoursPerMonth
			continue
		}

		// Workload spans multiple AZs — estimate cross-AZ traffic
		azList := make([]string, 0, len(wl.AZPods))
		for az := range wl.AZPods {
			azList = append(azList, az)
		}
		sort.Strings(azList)

		workloadName := wl.Name
		if wl.Kind != "" && wl.Kind != "Pod" {
			workloadName = wl.Name
		}

		for i := 0; i < len(azList); i++ {
			for j := i + 1; j < len(azList); j++ {
				srcAZ, dstAZ := azList[i], azList[j]
				srcPods, dstPods := wl.AZPods[srcAZ], wl.AZPods[dstAZ]
				pairFraction := float64(srcPods*dstPods) / float64(totalPods*totalPods)
				estimatedGB := trafficPerNode * cost.HoursPerMonth * float64(srcPods+dstPods) * pairFraction
				flowCost := estimatedGB * crossAZCostPerGB

				flows = append(flows, azFlow{
					Namespace:      wl.Namespace,
					Workload:       workloadName,
					SourceAZ:       srcAZ,
					DestAZ:         dstAZ,
					TrafficGB:      estimatedGB,
					MonthlyCostUSD: flowCost,
				})
				totalCrossAZCost += flowCost
			}
		}
	}

	// Sort flows by cost descending
	sort.Slice(flows, func(i, j int) bool {
		return flows[i].MonthlyCostUSD > flows[j].MonthlyCostUSD
	})

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"totalMonthlyCostUSD": totalCrossAZCost,
		"crossAZCostUSD":      totalCrossAZCost,
		"inAZCostUSD":         0.0, // In-AZ traffic is free on AWS/GCP/Azure
		"inAZTrafficGB":       totalInAZTrafficGB,
		"flows":               flows,
	})
}

// nodeAZ extracts the availability zone from a node's labels.
func nodeAZ(n *state.NodeState) string {
	if n.Node.Labels == nil {
		return "unknown"
	}
	if z, ok := n.Node.Labels["topology.kubernetes.io/zone"]; ok {
		return z
	}
	if z, ok := n.Node.Labels["failure-domain.beta.kubernetes.io/zone"]; ok {
		return z
	}
	return "unknown"
}

// isInternalLabel returns true for Kubernetes internal labels.
func isInternalLabel(key string) bool {
	return strings.HasPrefix(key, "kubernetes.io/") ||
		strings.HasPrefix(key, "k8s.io/") ||
		strings.Contains(key, ".kubernetes.io/") ||
		strings.Contains(key, ".k8s.io/")
}

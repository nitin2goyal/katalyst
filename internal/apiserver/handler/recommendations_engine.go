package handler

import (
	"crypto/sha256"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"

	intmetrics "github.com/koptimizer/koptimizer/internal/metrics"
	"github.com/koptimizer/koptimizer/internal/state"
	"github.com/koptimizer/koptimizer/pkg/cost"
)

// ComputedRecommendation is the on-the-fly recommendation shape matching frontend expectations.
type ComputedRecommendation struct {
	ID               string  `json:"id"`
	Type             string  `json:"type"`
	Target           string  `json:"target"`
	Description      string  `json:"description"`
	EstimatedSavings float64 `json:"estimatedSavings"`
	Status           string  `json:"status"`
	Priority         string  `json:"priority"`
	CreatedAt        string  `json:"createdAt"`
	Confidence       float64 `json:"confidence"`
}

// ComputedOpportunity is the savings opportunity shape for the cost/savings endpoint.
type ComputedOpportunity struct {
	Type             string  `json:"type"`
	Name             string  `json:"name"`
	Description      string  `json:"description"`
	EstimatedSavings float64 `json:"estimatedSavings"`
}

const (
	minSavingsThreshold = 5.0 // $5/mo minimum to surface a recommendation
	cacheTTL            = 5 * time.Minute

	// Minimum data points required for historical analysis
	minNodeDataPoints = 360  // 6h at 60s intervals
	minPodDataPoints  = 1440 // 24h at 60s intervals

	// Default estimated spot discount (varies 40-90% by instance family).
	// Used when no cloud-provider-specific estimator is available.
	defaultSpotDiscount = 0.60
)

var systemNamespaces = map[string]bool{
	"kube-system":     true,
	"kube-public":     true,
	"kube-node-lease": true,
}

// recsCache caches computed recommendations to avoid recomputation across
// concurrent API calls (recommendations, summary, savings all hit on page load).
var recsCache struct {
	sync.Mutex
	recs   []ComputedRecommendation
	expiry time.Time
}

// computedID returns a deterministic ID from type+target.
func computedID(recType, target string) string {
	h := sha256.Sum256([]byte(recType + ":" + target))
	return fmt.Sprintf("computed-%x", h[:8])
}

// ComputeRecommendations generates recommendations from live ClusterState.
// Results are cached for 5 minutes to avoid redundant computation across endpoints.
func ComputeRecommendations(cs *state.ClusterState, metricsStore *intmetrics.Store) []ComputedRecommendation {
	recsCache.Lock()
	defer recsCache.Unlock()
	if time.Now().Before(recsCache.expiry) && recsCache.recs != nil {
		return recsCache.recs
	}
	recs := computeFromData(cs.GetAllNodes(), cs.GetAllPods(), cs.GetNodeGroups().GetAll(), metricsStore)
	recsCache.recs = recs
	recsCache.expiry = time.Now().Add(cacheTTL)
	return recs
}

// computeFromData is the core computation, separated for testability.
func computeFromData(nodes []*state.NodeState, pods []*state.PodState, nodeGroups []*state.NodeGroupInfo, metricsStore *intmetrics.Store) []ComputedRecommendation {
	now := time.Now().UTC().Format(time.RFC3339)
	var recs []ComputedRecommendation

	recs = appendEmptyNodeRecs(recs, nodes, now)
	recs = appendUnderutilizedNodeRecs(recs, nodes, now, metricsStore)
	recs = appendSpotAdoptionRecs(recs, nodes, now)
	recs = appendPodRightsizingRecs(recs, nodes, pods, now, metricsStore)
	recs = appendNodeGroupRightsizingRecs(recs, nodeGroups, now, metricsStore)

	sort.Slice(recs, func(i, j int) bool {
		return recs[i].EstimatedSavings > recs[j].EstimatedSavings
	})

	return recs
}

// --- Algorithm 1: Empty nodes ---

func appendEmptyNodeRecs(recs []ComputedRecommendation, nodes []*state.NodeState, now string) []ComputedRecommendation {
	for _, n := range nodes {
		if n.IsGPUNode || !n.IsEmpty() {
			continue
		}
		savings := n.HourlyCostUSD * cost.HoursPerMonth
		if savings < minSavingsThreshold {
			continue
		}
		recs = append(recs, ComputedRecommendation{
			ID:               computedID("consolidation", n.Node.Name),
			Type:             "consolidation",
			Target:           n.Node.Name,
			Description:      fmt.Sprintf("Node %s is empty (no non-DaemonSet pods). Remove to save $%.0f/mo.", n.Node.Name, savings),
			EstimatedSavings: roundCents(savings),
			Status:           "pending",
			Priority:         "critical",
			CreatedAt:        now,
			Confidence:       0.95,
		})
	}
	return recs
}

// --- Algorithm 2: Underutilized nodes (both CPU & mem < 20%) ---
// Uses P95 over 6h when historical data is available.

func appendUnderutilizedNodeRecs(recs []ComputedRecommendation, nodes []*state.NodeState, now string, metricsStore *intmetrics.Store) []ComputedRecommendation {
	for _, n := range nodes {
		if n.IsGPUNode || n.IsEmpty() {
			continue
		}
		// Skip nodes where usage is 0 but pods exist — indicates missing metrics, not true idle
		if n.CPUUsed == 0 && n.MemoryUsed == 0 && len(n.Pods) > 0 {
			continue
		}

		var cpuUtil, memUtil float64
		confidence := 0.70

		// Try historical P95 over 6h
		if metricsStore != nil {
			if window := metricsStore.GetNodeWindow(n.Node.Name, 6*time.Hour); window != nil && window.DataPoints >= minNodeDataPoints {
				if n.CPUCapacity > 0 {
					cpuUtil = float64(window.P95CPU) / float64(n.CPUCapacity) * 100
				}
				if n.MemoryCapacity > 0 {
					memUtil = float64(window.P95Memory) / float64(n.MemoryCapacity) * 100
				}
				confidence = 0.90
			} else {
				// Fall back to point-in-time
				cpuUtil = n.CPUUtilization()
				memUtil = n.MemoryUtilization()
			}
		} else {
			cpuUtil = n.CPUUtilization()
			memUtil = n.MemoryUtilization()
		}

		if cpuUtil >= 20 || memUtil >= 20 {
			continue
		}

		savings := n.HourlyCostUSD * cost.HoursPerMonth
		if savings < minSavingsThreshold {
			continue
		}
		priority := "medium"
		if cpuUtil < 10 && memUtil < 10 {
			priority = "high"
		}
		recs = append(recs, ComputedRecommendation{
			ID:               computedID("consolidation-underutil", n.Node.Name),
			Type:             "consolidation",
			Target:           n.Node.Name,
			Description:      fmt.Sprintf("Node %s is underutilized (CPU: %.1f%%, Mem: %.1f%%). Drain and remove to save $%.0f/mo.", n.Node.Name, cpuUtil, memUtil, savings),
			EstimatedSavings: roundCents(savings),
			Status:           "pending",
			Priority:         priority,
			CreatedAt:        now,
			Confidence:       confidence,
		})
	}
	return recs
}

// --- Algorithm 3: Spot adoption ---

func appendSpotAdoptionRecs(recs []ComputedRecommendation, nodes []*state.NodeState, now string) []ComputedRecommendation {
	type spotGroup struct {
		totalHourly float64
		count       int
		groupName   string
	}
	groups := make(map[string]*spotGroup)
	for _, n := range nodes {
		if n.IsGPUNode || n.IsSpot {
			continue
		}
		gid := n.NodeGroupID
		groupName := n.NodeGroupName
		if gid == "" {
			gid = "ungrouped-" + n.InstanceType
			groupName = n.InstanceType + " (ungrouped)"
		}
		if groupName == "" {
			groupName = gid
		}
		sg, ok := groups[gid]
		if !ok {
			sg = &spotGroup{groupName: groupName}
			groups[gid] = sg
		}
		sg.totalHourly += n.HourlyCostUSD
		sg.count++
	}
	for gid, sg := range groups {
		savings := sg.totalHourly * defaultSpotDiscount * cost.HoursPerMonth
		if savings < minSavingsThreshold {
			continue
		}
		discountPct := int(defaultSpotDiscount * 100)
		recs = append(recs, ComputedRecommendation{
			ID:               computedID("spot", gid),
			Type:             "spot",
			Target:           sg.groupName,
			Description:      fmt.Sprintf("Convert %d on-demand nodes (%s) to spot instances to save $%.0f/mo (est. %d%% discount).", sg.count, sg.groupName, savings, discountPct),
			EstimatedSavings: roundCents(savings),
			Status:           "pending",
			Priority:         "medium",
			CreatedAt:        now,
			Confidence:       0.70,
		})
	}
	return recs
}

// --- Algorithm 4: Pod rightsizing ---
// Uses P95 over 24h when historical data is available.

func appendPodRightsizingRecs(recs []ComputedRecommendation, nodes []*state.NodeState, pods []*state.PodState, now string, metricsStore *intmetrics.Store) []ComputedRecommendation {
	nodeCostMap := make(map[string]float64, len(nodes))
	nodeCPUReqMap := make(map[string]int64, len(nodes))
	for _, n := range nodes {
		nodeCostMap[n.Node.Name] = n.HourlyCostUSD
		nodeCPUReqMap[n.Node.Name] = n.CPURequested
	}

	// Check if pod-level metrics are available. Without metrics, we cannot
	// make accurate rightsizing recommendations — skip entirely.
	metricsCount := 0
	for _, p := range pods {
		if p.CPUUsage > 0 || p.MemoryUsage > 0 {
			metricsCount++
		}
	}
	hasMetrics := metricsCount > len(pods)/10
	if !hasMetrics {
		// No metrics data available — cannot compute rightsizing recommendations.
		return recs
	}

	type ownerGroup struct {
		namespace     string
		ownerKind     string
		ownerName     string
		allocated     float64
		wasted        float64
		podCount      int
		sumCPUEff     float64
		sumMemEff     float64
		hasHistorical bool
	}
	owners := make(map[string]*ownerGroup)
	for _, p := range pods {
		if systemNamespaces[p.Namespace] || p.CPURequest == 0 {
			continue
		}
		// Only consider running pods — completed/failed Job pods still have
		// spec.nodeName set but are NOT counted in the node's CPURequested,
		// which causes fraction > 1.0 and inflated savings.
		if p.Pod != nil && p.Pod.Status.Phase != corev1.PodRunning {
			continue
		}
		// Skip pods younger than 10 minutes — insufficient metrics history
		if p.Pod != nil && p.Pod.Status.StartTime != nil {
			if time.Since(p.Pod.Status.StartTime.Time) < 10*time.Minute {
				continue
			}
		}

		kind, name := p.OwnerKind, p.OwnerName
		if name == "" {
			kind, name = "Pod", p.Name
		}
		ownerKey := p.Namespace + "/" + kind + "/" + name

		// Try historical P95 over 24h for each pod's containers
		var cpuEff, memEff float64
		usedHistorical := false
		if metricsStore != nil && p.Pod != nil {
			var totalP95CPU, totalP95Mem int64
			allContainersHaveData := true
			for _, c := range p.Pod.Spec.Containers {
				window := metricsStore.GetPodContainerWindow(p.Namespace, p.Name, c.Name, 24*time.Hour)
				if window != nil && window.DataPoints >= minPodDataPoints {
					totalP95CPU += window.P95CPU
					totalP95Mem += window.P95Memory
				} else {
					allContainersHaveData = false
					break
				}
			}
			if allContainersHaveData && p.CPURequest > 0 {
				cpuEff = float64(totalP95CPU) / float64(p.CPURequest)
				if p.MemoryRequest > 0 {
					memEff = float64(totalP95Mem) / float64(p.MemoryRequest)
				}
				usedHistorical = true
			}
		}

		if !usedHistorical {
			// Fall back to point-in-time
			if !p.IsOverProvisioned(0.5) {
				continue
			}
			cpuEff = p.CPUEfficiency()
			memEff = p.MemoryEfficiency()
		} else {
			// With historical data, check overprovisioning using P95 efficiency
			maxEff := math.Max(cpuEff, memEff)
			if maxEff >= 0.5 {
				continue
			}
		}

		nodeHourly := nodeCostMap[p.NodeName]
		nodeCPUReq := nodeCPUReqMap[p.NodeName]
		if nodeCPUReq == 0 {
			continue
		}
		fraction := float64(p.CPURequest) / float64(nodeCPUReq)
		allocatedMonthly := nodeHourly * cost.HoursPerMonth * fraction

		maxEff := math.Max(cpuEff, memEff)
		wastedMonthly := allocatedMonthly * (1 - maxEff)

		og, ok := owners[ownerKey]
		if !ok {
			og = &ownerGroup{namespace: p.Namespace, ownerKind: kind, ownerName: name}
			owners[ownerKey] = og
		}
		og.allocated += allocatedMonthly
		og.wasted += wastedMonthly
		og.podCount++
		og.sumCPUEff += cpuEff
		og.sumMemEff += memEff
		if usedHistorical {
			og.hasHistorical = true
		}
	}

	for key, og := range owners {
		if og.wasted < minSavingsThreshold {
			continue
		}
		avgCPU := og.sumCPUEff / float64(og.podCount) * 100
		avgMem := og.sumMemEff / float64(og.podCount) * 100
		priority := "low"
		if og.wasted > 100 {
			priority = "high"
		} else if og.wasted > 20 {
			priority = "medium"
		}
		target := og.namespace + "/" + og.ownerKind + "/" + og.ownerName

		confidence := 0.70
		if og.hasHistorical {
			confidence = 0.90
		}

		recs = append(recs, ComputedRecommendation{
			ID:               computedID("rightsizing", key),
			Type:             "rightsizing",
			Target:           target,
			Description:      fmt.Sprintf("%s %s/%s has %d pod(s) using only %.0f%% CPU, %.0f%% memory. Right-size to save $%.0f/mo.", og.ownerKind, og.namespace, og.ownerName, og.podCount, avgCPU, avgMem, og.wasted),
			EstimatedSavings: roundCents(og.wasted),
			Status:           "pending",
			Priority:         priority,
			CreatedAt:        now,
			Confidence:       confidence,
		})
	}
	return recs
}

// --- Algorithm 5: Node group rightsizing ---
// Uses P95 over 6h when historical data is available for ALL nodes in the group.

func appendNodeGroupRightsizingRecs(recs []ComputedRecommendation, nodeGroups []*state.NodeGroupInfo, now string, metricsStore *intmetrics.Store) []ComputedRecommendation {
	for _, ng := range nodeGroups {
		if len(ng.Nodes) < 2 {
			continue
		}
		hasGPU := false
		for _, n := range ng.Nodes {
			if n.IsGPUNode {
				hasGPU = true
				break
			}
		}
		if hasGPU {
			continue
		}

		var cpuUtil, memUtil float64
		confidence := 0.70

		// Try historical P95 over 6h — require ALL nodes in group to have data
		if metricsStore != nil {
			var totalP95CPU, totalP95Mem int64
			var totalCPUCap, totalMemCap int64
			allHaveData := true
			for _, n := range ng.Nodes {
				window := metricsStore.GetNodeWindow(n.Node.Name, 6*time.Hour)
				if window != nil && window.DataPoints >= minNodeDataPoints {
					totalP95CPU += window.P95CPU
					totalP95Mem += window.P95Memory
				} else {
					allHaveData = false
					break
				}
				totalCPUCap += n.CPUCapacity
				totalMemCap += n.MemoryCapacity
			}
			if allHaveData && totalCPUCap > 0 && totalMemCap > 0 {
				cpuUtil = float64(totalP95CPU) / float64(totalCPUCap) * 100
				memUtil = float64(totalP95Mem) / float64(totalMemCap) * 100
				confidence = 0.90
			} else {
				cpuUtil = ng.CPUUtilization()
				memUtil = ng.MemoryUtilization()
			}
		} else {
			cpuUtil = ng.CPUUtilization()
			memUtil = ng.MemoryUtilization()
		}

		// Skip groups with 0% utilization — likely missing metrics, not truly idle
		if cpuUtil == 0 && memUtil == 0 {
			continue
		}
		if cpuUtil >= 25 || memUtil >= 25 {
			continue
		}

		maxUtil := math.Max(cpuUtil, memUtil)
		if maxUtil == 0 {
			maxUtil = 1
		}
		targetCount := int(math.Ceil(float64(len(ng.Nodes)) * maxUtil / 50))
		if targetCount < 1 {
			targetCount = 1
		}
		removable := len(ng.Nodes) - targetCount
		if removable <= 0 {
			continue
		}

		// Sum hourly costs directly from nodes to avoid 730/730.5 drift
		totalHourly := 0.0
		for _, n := range ng.Nodes {
			totalHourly += n.HourlyCostUSD
		}
		avgHourly := totalHourly / float64(len(ng.Nodes))
		savings := float64(removable) * avgHourly * cost.HoursPerMonth
		if savings < minSavingsThreshold {
			continue
		}

		groupName := ng.Name
		if groupName == "" {
			groupName = ng.ID
		}
		recs = append(recs, ComputedRecommendation{
			ID:               computedID("consolidation-ng", ng.ID),
			Type:             "consolidation",
			Target:           groupName,
			Description:      fmt.Sprintf("Node group %s has %d nodes at %.0f%% CPU, %.0f%% mem. Reduce by %d nodes to save $%.0f/mo.", groupName, len(ng.Nodes), cpuUtil, memUtil, removable, savings),
			EstimatedSavings: roundCents(savings),
			Status:           "pending",
			Priority:         "medium",
			CreatedAt:        now,
			Confidence:       confidence,
		})
	}
	return recs
}

// --- Helpers ---

func roundCents(v float64) float64 {
	return math.Round(v*100) / 100
}

// ComputeSavingsOpportunities converts recommendations to savings opportunity format.
func ComputeSavingsOpportunities(recs []ComputedRecommendation) []ComputedOpportunity {
	opps := make([]ComputedOpportunity, 0, len(recs))
	for _, r := range recs {
		opps = append(opps, ComputedOpportunity{
			Type:             r.Type,
			Name:             r.Target,
			Description:      r.Description,
			EstimatedSavings: r.EstimatedSavings,
		})
	}
	return opps
}

// ComputeTotalPotentialSavings sums estimated savings across all recommendations,
// deduplicating by target to avoid double-counting. For example, a node group can't
// be both consolidated AND converted to spot — we take the higher savings.
// Individual node recs that overlap with a nodegroup rec are excluded.
func ComputeTotalPotentialSavings(recs []ComputedRecommendation) float64 {
	// Dedup by target — keep highest savings per target regardless of type.
	// This prevents consolidation + spot for the same nodegroup being summed.
	bestByTarget := make(map[string]float64)
	for _, r := range recs {
		if r.EstimatedSavings > bestByTarget[r.Target] {
			bestByTarget[r.Target] = r.EstimatedSavings
		}
	}

	// Identify nodegroup-level targets to avoid double-counting
	// individual nodes that are covered by a nodegroup recommendation.
	nodegroupTargets := make(map[string]bool)
	for _, r := range recs {
		if r.Type == "consolidation" || r.Type == "spot" {
			// Nodegroup targets don't contain "/" (individual nodes/workloads do)
			if !strings.Contains(r.Target, "/") && !strings.HasPrefix(r.Target, "gke-") && !strings.HasPrefix(r.Target, "eks-") && !strings.HasPrefix(r.Target, "aks-") {
				nodegroupTargets[r.Target] = true
			}
		}
	}

	// Sum savings, skipping individual node recs whose node belongs
	// to a nodegroup that already has a recommendation (the nodegroup rec
	// already accounts for removing those nodes).
	total := 0.0
	for target, savings := range bestByTarget {
		skip := false
		for ng := range nodegroupTargets {
			if target != ng && strings.Contains(target, ng) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		total += savings
	}

	return total
}

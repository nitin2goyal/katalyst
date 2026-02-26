package handler

import (
	"crypto/sha256"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

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
// Results are cached for 60s to avoid redundant computation across endpoints.
func ComputeRecommendations(cs *state.ClusterState) []ComputedRecommendation {
	recsCache.Lock()
	defer recsCache.Unlock()
	if time.Now().Before(recsCache.expiry) && recsCache.recs != nil {
		return recsCache.recs
	}
	recs := computeFromData(cs.GetAllNodes(), cs.GetAllPods(), cs.GetNodeGroups().GetAll())
	recsCache.recs = recs
	recsCache.expiry = time.Now().Add(cacheTTL)
	return recs
}

// computeFromData is the core computation, separated for testability.
func computeFromData(nodes []*state.NodeState, pods []*state.PodState, nodeGroups []*state.NodeGroupInfo) []ComputedRecommendation {
	now := time.Now().UTC().Format(time.RFC3339)
	var recs []ComputedRecommendation

	recs = appendEmptyNodeRecs(recs, nodes, now)
	recs = appendUnderutilizedNodeRecs(recs, nodes, now)
	recs = appendSpotAdoptionRecs(recs, nodes, now)
	recs = appendPodRightsizingRecs(recs, nodes, pods, now)
	recs = appendNodeGroupRightsizingRecs(recs, nodeGroups, now)

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

func appendUnderutilizedNodeRecs(recs []ComputedRecommendation, nodes []*state.NodeState, now string) []ComputedRecommendation {
	for _, n := range nodes {
		if n.IsGPUNode || n.IsEmpty() {
			continue
		}
		// Skip nodes where usage is 0 but pods exist — indicates missing metrics, not true idle
		if n.CPUUsed == 0 && n.MemoryUsed == 0 && len(n.Pods) > 0 {
			continue
		}
		if !n.IsUnderutilized(20) {
			continue
		}
		savings := n.HourlyCostUSD * cost.HoursPerMonth
		if savings < minSavingsThreshold {
			continue
		}
		cpuUtil := n.CPUUtilization()
		memUtil := n.MemoryUtilization()
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
			Confidence:       0.85,
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
		savings := sg.totalHourly * 0.65 * cost.HoursPerMonth
		if savings < minSavingsThreshold {
			continue
		}
		recs = append(recs, ComputedRecommendation{
			ID:               computedID("spot", gid),
			Type:             "spot",
			Target:           sg.groupName,
			Description:      fmt.Sprintf("Convert %d on-demand nodes (%s) to spot instances to save $%.0f/mo (est. 65%% discount).", sg.count, sg.groupName, savings),
			EstimatedSavings: roundCents(savings),
			Status:           "pending",
			Priority:         "medium",
			CreatedAt:        now,
			Confidence:       0.75,
		})
	}
	return recs
}

// --- Algorithm 4: Pod rightsizing ---

func appendPodRightsizingRecs(recs []ComputedRecommendation, nodes []*state.NodeState, pods []*state.PodState, now string) []ComputedRecommendation {
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
		namespace string
		ownerKind string
		ownerName string
		allocated float64
		wasted    float64
		podCount  int
		sumCPUEff float64
		sumMemEff float64
	}
	owners := make(map[string]*ownerGroup)
	for _, p := range pods {
		if systemNamespaces[p.Namespace] || p.CPURequest == 0 {
			continue
		}
		if !p.IsOverProvisioned(0.5) {
			continue
		}

		kind, name := p.OwnerKind, p.OwnerName
		if name == "" {
			kind, name = "Pod", p.Name
		}
		ownerKey := p.Namespace + "/" + kind + "/" + name

		nodeHourly := nodeCostMap[p.NodeName]
		nodeCPUReq := nodeCPUReqMap[p.NodeName]
		if nodeCPUReq == 0 {
			continue
		}
		fraction := float64(p.CPURequest) / float64(nodeCPUReq)
		allocatedMonthly := nodeHourly * cost.HoursPerMonth * fraction

		cpuEff := p.CPUEfficiency()
		memEff := p.MemoryEfficiency()
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

		recs = append(recs, ComputedRecommendation{
			ID:               computedID("rightsizing", key),
			Type:             "rightsizing",
			Target:           target,
			Description:      fmt.Sprintf("%s %s/%s has %d pod(s) using only %.0f%% CPU, %.0f%% memory. Right-size to save $%.0f/mo.", og.ownerKind, og.namespace, og.ownerName, og.podCount, avgCPU, avgMem, og.wasted),
			EstimatedSavings: roundCents(og.wasted),
			Status:           "pending",
			Priority:         priority,
			CreatedAt:        now,
			Confidence:       0.80,
		})
	}
	return recs
}

// --- Algorithm 5: Node group rightsizing ---

func appendNodeGroupRightsizingRecs(recs []ComputedRecommendation, nodeGroups []*state.NodeGroupInfo, now string) []ComputedRecommendation {
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

		cpuUtil := ng.CPUUtilization()
		memUtil := ng.MemoryUtilization()
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
			Confidence:       0.80,
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

// ComputeTotalPotentialSavings sums estimated savings across all recommendations.
func ComputeTotalPotentialSavings(recs []ComputedRecommendation) float64 {
	total := 0.0
	for _, r := range recs {
		total += r.EstimatedSavings
	}
	return total
}

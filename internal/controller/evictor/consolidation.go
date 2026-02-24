package evictor

import (
	"fmt"
	"sort"
	"time"

	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/pkg/cost"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// Consolidator plans multi-node consolidation.
type Consolidator struct {
	config *config.Config
}

func NewConsolidator(cfg *config.Config) *Consolidator {
	return &Consolidator{config: cfg}
}

// Plan creates a consolidation plan based on fragmentation scores.
func (c *Consolidator) Plan(snapshot *optimizer.ClusterSnapshot, scores []NodeScore) ([]optimizer.Recommendation, error) {
	var recs []optimizer.Recommendation

	// Sort by score descending (most empty first)
	sort.Slice(scores, func(i, j int) bool {
		return scores[i].Score > scores[j].Score
	})

	// Find candidate nodes for consolidation
	var candidates []NodeScore
	for _, s := range scores {
		if s.IsCandidate && s.PodCount > 0 {
			candidates = append(candidates, s)
		}
	}

	if len(candidates) == 0 {
		return nil, nil
	}

	// Calculate total free capacity on non-candidate nodes
	totalFreeCPU := int64(0)
	totalFreeMem := int64(0)
	for _, s := range scores {
		if !s.IsCandidate {
			totalFreeCPU += s.CPUFree
			totalFreeMem += s.MemFree
		}
	}

	// Build node lookup map for O(1) access
	nodeMap := make(map[string]*optimizer.NodeInfo, len(snapshot.Nodes))
	for i := range snapshot.Nodes {
		nodeMap[snapshot.Nodes[i].Node.Name] = &snapshot.Nodes[i]
	}

	// Check if candidate pods can fit on remaining nodes
	for _, candidate := range candidates {
		if len(recs) >= c.config.Evictor.MaxConcurrentEvictions {
			break
		}

		// Can the pods (used resources) from this node fit on other nodes?
		// Used = Capacity - Free. We need the used resources to be <= total free elsewhere.
		_ = candidate.CPUFree // CPUFree is the free capacity of the candidate
		// We need to compute the USED capacity: what the pods on this node are requesting.
		// From fragmentation.go: CPUFree = CPUCapacity - CPURequested, so CPURequested = CPUCapacity - CPUFree
		// Since we don't have CPUCapacity in NodeScore, we check if the free space elsewhere
		// exceeds what the pods use. The pods' total request = (capacity - free).
		// But we can approximate: if a candidate has high score (very empty), the used portion is small.
		// The correct check: (node.CPUCapacity - candidate.CPUFree) <= totalFreeCPU
		// Since NodeScore doesn't carry capacity, we compute used from the snapshot.
		var candidateCPUUsed, candidateMemUsed int64
		if n, ok := nodeMap[candidate.NodeName]; ok {
			candidateCPUUsed = n.CPURequested
			candidateMemUsed = n.MemoryRequested
		}
		canFit := candidateCPUUsed <= totalFreeCPU && candidateMemUsed <= totalFreeMem

		if !canFit {
			continue
		}

		hourlySavings := float64(0)
		// Estimate from node group
		if n, ok := nodeMap[candidate.NodeName]; ok {
			hourlySavings = n.HourlyCostUSD
		}

		nodesAffected := 1
		requiresAIGate := nodesAffected > c.config.AIGate.MaxEvictNodes

		recs = append(recs, optimizer.Recommendation{
			ID:             fmt.Sprintf("consolidate-%s-%d", candidate.NodeName, time.Now().Unix()),
			Type:           optimizer.RecommendationEviction,
			Priority:       optimizer.PriorityMedium,
			AutoExecutable: true,
			RequiresAIGate: requiresAIGate,
			TargetKind:     "Node",
			TargetName:     candidate.NodeName,
			Summary: fmt.Sprintf("Consolidate node %s (%.0f%% CPU, %.0f%% mem, %d pods) - pods can fit on other nodes",
				candidate.NodeName, candidate.CPUUtilPct, candidate.MemUtilPct, candidate.PodCount),
			ActionSteps: []string{
				fmt.Sprintf("Cordon node %s", candidate.NodeName),
				fmt.Sprintf("Drain %d pods from %s (PDB-checked)", candidate.PodCount, candidate.NodeName),
				"Wait for pods to reschedule",
				"Scale down node group by 1",
			},
			EstimatedSaving: optimizer.SavingEstimate{
				MonthlySavingsUSD: hourlySavings * cost.HoursPerMonth,
				AnnualSavingsUSD:  hourlySavings * cost.HoursPerMonth * 12,
				Currency:          "USD",
			},
			EstimatedImpact: optimizer.ImpactEstimate{
				MonthlyCostChangeUSD: -hourlySavings * cost.HoursPerMonth,
				NodesAffected:        nodesAffected,
				PodsAffected:         candidate.PodCount,
				RiskLevel:            "medium",
			},
			Details: map[string]string{
				"nodeName":  candidate.NodeName,
				"fragScore": fmt.Sprintf("%.2f", candidate.Score),
				"podCount":  fmt.Sprintf("%d", candidate.PodCount),
			},
			CreatedAt: time.Now(),
		})

		// Reduce available capacity after planning this consolidation.
		// The pods from this candidate will consume candidateCPUUsed/candidateMemUsed on other nodes.
		totalFreeCPU -= candidateCPUUsed
		totalFreeMem -= candidateMemUsed
	}

	return recs, nil
}

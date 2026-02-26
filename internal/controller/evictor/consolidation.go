package evictor

import (
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/internal/scheduler"
	"github.com/koptimizer/koptimizer/pkg/cost"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// Consolidator plans multi-node consolidation.
type Consolidator struct {
	config    *config.Config
	simulator *scheduler.Simulator
}

func NewConsolidator(cfg *config.Config) *Consolidator {
	return &Consolidator{config: cfg, simulator: scheduler.NewSimulator()}
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

	// Build node lookup map for O(1) access
	nodeMap := make(map[string]*optimizer.NodeInfo, len(snapshot.Nodes))
	for i := range snapshot.Nodes {
		nodeMap[snapshot.Nodes[i].Node.Name] = &snapshot.Nodes[i]
	}

	// Build non-candidate nodes list and pods-by-node map for simulation
	candidateSet := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		candidateSet[c.NodeName] = true
	}

	var nonCandidateNodes []*corev1.Node
	podsByNode := make(map[string][]*corev1.Pod)
	for _, n := range snapshot.Nodes {
		name := n.Node.Name
		if !candidateSet[name] {
			nonCandidateNodes = append(nonCandidateNodes, n.Node)
		}
		podsByNode[name] = n.Pods
	}

	// Check if candidate pods can fit on remaining nodes using per-pod simulation
	for _, candidate := range candidates {
		if len(recs) >= c.config.Evictor.MaxConcurrentEvictions {
			break
		}

		// Verify every non-DaemonSet pod on this candidate can be scheduled
		// on at least one non-candidate node.
		candidateNode, ok := nodeMap[candidate.NodeName]
		if !ok {
			continue
		}
		allFit := true
		// Track cumulative placements within this candidate's feasibility check.
		// Without this, two pods could both claim the same target node even if
		// it only has capacity for one.
		var tentativePlacements []struct {
			node string
			pod  *corev1.Pod
		}
		for _, pod := range candidateNode.Pods {
			if pod == nil {
				continue
			}
			isDaemonSet := false
			for _, ref := range pod.OwnerReferences {
				if ref.Kind == "DaemonSet" {
					isDaemonSet = true
					break
				}
			}
			if isDaemonSet {
				continue
			}
			fitting := c.simulator.FindFittingNodes(pod, nonCandidateNodes, podsByNode)
			if len(fitting) == 0 {
				allFit = false
				break
			}
			// Accumulate: add pod to target node so subsequent pods see reduced capacity
			podsByNode[fitting[0]] = append(podsByNode[fitting[0]], pod)
			tentativePlacements = append(tentativePlacements, struct {
				node string
				pod  *corev1.Pod
			}{fitting[0], pod})
		}

		if !allFit {
			// Roll back tentative placements since this candidate is infeasible
			for _, tp := range tentativePlacements {
				pods := podsByNode[tp.node]
				podsByNode[tp.node] = pods[:len(pods)-1]
			}
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

		// Pod placements were already accumulated during the feasibility check above,
		// so subsequent candidate iterations will see reduced capacity on target nodes.
	}

	return recs, nil
}

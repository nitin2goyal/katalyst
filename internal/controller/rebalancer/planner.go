package rebalancer

import (
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/koptimizer/koptimizer/internal/scheduler"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// Planner computes consolidation recommendations that pack pods onto fewer
// nodes at high utilization, freeing lightly-loaded nodes for removal.
type Planner struct {
	targetUtilPct float64 // pack target nodes to this % (default 95)
	maxEvacuate   int     // max nodes to evacuate per cycle
	simulator     *scheduler.Simulator
}

func NewPlanner() *Planner {
	return &Planner{targetUtilPct: 95.0, maxEvacuate: 2, simulator: scheduler.NewSimulator()}
}

// NewPlannerWithThreshold creates a Planner with a configurable target utilization.
func NewPlannerWithThreshold(thresholdPct float64) *Planner {
	if thresholdPct <= 0 {
		thresholdPct = 95.0
	}
	return &Planner{targetUtilPct: thresholdPct, maxEvacuate: 2, simulator: scheduler.NewSimulator()}
}

// NewPlannerWithConfig creates a Planner with full configuration.
func NewPlannerWithConfig(targetPct float64, maxEvacuate int) *Planner {
	if targetPct <= 0 {
		targetPct = 95.0
	}
	if maxEvacuate <= 0 {
		maxEvacuate = 2
	}
	return &Planner{targetUtilPct: targetPct, maxEvacuate: maxEvacuate, simulator: scheduler.NewSimulator()}
}

// consolidationNode tracks request-based utilization for bin-packing.
type consolidationNode struct {
	info         optimizer.NodeInfo
	cpuReqPct    float64
	memReqPct    float64
	cpuRequested int64
	memRequested int64
	cpuCapacity  int64
	memCapacity  int64
}

// ComputePlan identifies lightly-loaded nodes whose pods can be packed onto
// remaining nodes (targeting 95% request utilization), then recommends
// evacuating them so the node autoscaler can remove the empty nodes.
func (p *Planner) ComputePlan(snapshot *optimizer.ClusterSnapshot) ([]optimizer.Recommendation, error) {
	if len(snapshot.Nodes) < 2 {
		return nil, nil
	}

	// Build per-node request utilization, skipping GPU and cordoned nodes.
	var nodes []consolidationNode
	for _, n := range snapshot.Nodes {
		if n.CPUCapacity <= 0 || n.MemoryCapacity <= 0 {
			continue
		}
		if n.Node.Spec.Unschedulable {
			continue
		}
		if n.IsGPUNode {
			continue
		}
		cpuReqPct := float64(n.CPURequested) / float64(n.CPUCapacity) * 100
		memReqPct := float64(n.MemoryRequested) / float64(n.MemoryCapacity) * 100
		nodes = append(nodes, consolidationNode{
			info:         n,
			cpuReqPct:    cpuReqPct,
			memReqPct:    memReqPct,
			cpuRequested: n.CPURequested,
			memRequested: n.MemoryRequested,
			cpuCapacity:  n.CPUCapacity,
			memCapacity:  n.MemoryCapacity,
		})
	}

	if len(nodes) < 2 {
		return nil, nil
	}

	// Sort by memory requested ascending — lightest (easiest to evacuate) first.
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].memRequested < nodes[j].memRequested
	})

	// Source threshold: nodes below 50% on BOTH CPU and memory requests are
	// candidates for evacuation. They're lightly loaded and their pods can
	// likely fit on remaining nodes.
	const sourceThresholdPct = 50.0

	// Build initial pods-by-node map for the simulator. This tracks
	// accumulated pod placements as we simulate evacuations.
	podsByNode := make(map[string][]*corev1.Pod)
	for _, n := range snapshot.Nodes {
		podsByNode[n.Node.Name] = append([]*corev1.Pod{}, n.Pods...)
	}

	// Track accumulated requests on each node as we simulate placements.
	accCPU := make(map[string]int64)
	accMem := make(map[string]int64)
	for _, n := range nodes {
		accCPU[n.info.Node.Name] = n.cpuRequested
		accMem[n.info.Node.Name] = n.memRequested
	}

	// Track which nodes we've marked for evacuation (excluded from targets).
	evacuated := make(map[string]bool)

	var recs []optimizer.Recommendation

	for _, source := range nodes {
		if len(recs) >= p.maxEvacuate {
			break
		}

		// Only consider lightly-loaded nodes as evacuation sources.
		if source.cpuReqPct >= sourceThresholdPct || source.memReqPct >= sourceThresholdPct {
			continue
		}

		nodeName := source.info.Node.Name

		// Collect movable pods on this source node.
		var movablePods []*corev1.Pod
		for _, pod := range source.info.Pods {
			if pod == nil {
				continue
			}
			if !canMovePod(pod) {
				continue
			}
			movablePods = append(movablePods, pod)
		}

		if len(movablePods) == 0 {
			continue
		}

		// Sort pods largest-first (by memory request descending) for
		// best-fit-decreasing bin packing.
		sort.Slice(movablePods, func(i, j int) bool {
			_, memI := scheduler.EffectivePodResources(movablePods[i])
			_, memJ := scheduler.EffectivePodResources(movablePods[j])
			return memI > memJ
		})

		// Build target node list: all schedulable nodes except evacuated
		// ones and the source itself.
		var targetNodes []*corev1.Node
		targetCaps := make(map[string][2]int64) // [cpuCap, memCap]
		for _, n := range nodes {
			tName := n.info.Node.Name
			if tName == nodeName || evacuated[tName] {
				continue
			}
			targetNodes = append(targetNodes, n.info.Node)
			targetCaps[tName] = [2]int64{n.cpuCapacity, n.memCapacity}
		}

		// Try to place every movable pod on a target node.
		allPlaced := true
		// Track tentative placements so we can roll back on failure.
		type placement struct {
			targetNode string
			pod        *corev1.Pod
			cpuReq     int64
			memReq     int64
		}
		var placements []placement

		for _, pod := range movablePods {
			podCPU, podMem := scheduler.EffectivePodResources(pod)
			placed := false

			// Find fitting nodes using the scheduler (checks taints,
			// affinity, topology, etc.) then apply the 95% cap.
			fitting := p.simulator.FindFittingNodes(pod, targetNodes, podsByNode)
			// Sort fitting nodes by free memory ascending (best-fit: tightest first).
			sort.Slice(fitting, func(i, j int) bool {
				return accMem[fitting[i]] > accMem[fitting[j]]
			})

			for _, tName := range fitting {
				caps := targetCaps[tName]
				newCPU := accCPU[tName] + podCPU
				newMem := accMem[tName] + podMem
				cpuPct := float64(newCPU) / float64(caps[0]) * 100
				memPct := float64(newMem) / float64(caps[1]) * 100
				if cpuPct <= p.targetUtilPct && memPct <= p.targetUtilPct {
					// Place pod here.
					accCPU[tName] = newCPU
					accMem[tName] = newMem
					podsByNode[tName] = append(podsByNode[tName], pod)
					placements = append(placements, placement{tName, pod, podCPU, podMem})
					placed = true
					break
				}
			}

			if !placed {
				allPlaced = false
				break
			}
		}

		if !allPlaced {
			// Roll back tentative placements for this source.
			for _, pl := range placements {
				accCPU[pl.targetNode] -= pl.cpuReq
				accMem[pl.targetNode] -= pl.memReq
				pods := podsByNode[pl.targetNode]
				if len(pods) > 0 {
					podsByNode[pl.targetNode] = pods[:len(pods)-1]
				}
			}
			continue
		}

		// All pods from this source can be placed — recommend evacuation.
		evacuated[nodeName] = true

		recs = append(recs, optimizer.Recommendation{
			ID:             fmt.Sprintf("consolidate-%s-%d", nodeName, time.Now().Unix()),
			Type:           optimizer.RecommendationRebalance,
			Priority:       optimizer.PriorityMedium,
			AutoExecutable: true,
			TargetKind:     "Node",
			TargetName:     nodeName,
			Summary: fmt.Sprintf("Consolidate: evacuate node %s (CPU req: %.0f%%, Mem req: %.0f%%, %d pods) to pack remaining nodes toward %.0f%%",
				nodeName, source.cpuReqPct, source.memReqPct, len(movablePods), p.targetUtilPct),
			ActionSteps: []string{
				fmt.Sprintf("Cordon node %s", nodeName),
				fmt.Sprintf("Evict %d movable pods (PDB-aware)", len(movablePods)),
				"Scheduler places pods on remaining nodes (targeting high utilization)",
				"Node autoscaler removes empty cordoned node",
			},
			EstimatedImpact: optimizer.ImpactEstimate{
				NodesAffected: 1,
				PodsAffected:  len(movablePods),
				RiskLevel:     "low",
			},
			Details: map[string]string{
				"nodeName":    nodeName,
				"consolidate": "true",
				"cpuReqPct":   fmt.Sprintf("%.1f", source.cpuReqPct),
				"memReqPct":   fmt.Sprintf("%.1f", source.memReqPct),
				"podsToMove":  fmt.Sprintf("%d", len(movablePods)),
				"nodeGroup":   source.info.NodeGroup,
			},
			CreatedAt: time.Now(),
		})
	}

	return recs, nil
}

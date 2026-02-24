package rebalancer

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// Planner computes optimal workload distribution using bin-packing.
type Planner struct {
	imbalanceThresholdPct float64
}

func NewPlanner() *Planner {
	return &Planner{imbalanceThresholdPct: 40.0}
}

// NewPlannerWithThreshold creates a Planner with a configurable imbalance threshold.
func NewPlannerWithThreshold(thresholdPct float64) *Planner {
	if thresholdPct <= 0 {
		thresholdPct = 40.0
	}
	return &Planner{imbalanceThresholdPct: thresholdPct}
}

// nodeUtil pairs a node with its CPU utilization percentage.
type nodeUtil struct {
	index   int
	node    optimizer.NodeInfo
	cpuPct  float64
	freeCPU int64
}

// ComputePlan generates rebalancing recommendations with bin-packing.
func (p *Planner) ComputePlan(snapshot *optimizer.ClusterSnapshot) ([]optimizer.Recommendation, error) {
	if len(snapshot.Nodes) < 2 {
		return nil, nil
	}

	// Calculate per-node utilization
	var nodes []nodeUtil
	for i, n := range snapshot.Nodes {
		if n.CPUCapacity > 0 {
			pct := float64(n.CPURequested) / float64(n.CPUCapacity) * 100
			nodes = append(nodes, nodeUtil{
				index:   i,
				node:    n,
				cpuPct:  pct,
				freeCPU: n.CPUCapacity - n.CPURequested,
			})
		}
	}

	if len(nodes) == 0 {
		return nil, nil
	}

	// Sort by utilization ascending
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].cpuPct < nodes[j].cpuPct })

	spread := nodes[len(nodes)-1].cpuPct - nodes[0].cpuPct

	// Only suggest rebalancing if spread exceeds the configured threshold.
	if spread < p.imbalanceThresholdPct {
		return nil, nil
	}

	avgUtil := 0.0
	for _, n := range nodes {
		avgUtil += n.cpuPct
	}
	avgUtil /= float64(len(nodes))

	// Identify overloaded and underloaded nodes
	// Overloaded: above average + 10%, Underloaded: below average - 10%
	overloadThreshold := avgUtil + 10
	underloadThreshold := avgUtil - 10

	var overloaded, underloaded []nodeUtil
	for _, n := range nodes {
		if n.cpuPct >= overloadThreshold {
			overloaded = append(overloaded, n)
		} else if n.cpuPct <= underloadThreshold {
			underloaded = append(underloaded, n)
		}
	}

	// Bin-packing: identify specific pods on overloaded nodes that could fit on underloaded nodes
	var movablePods []string
	var targetNodes []string
	podsAffected := 0

	for _, over := range overloaded {
		if over.node.Node == nil {
			continue
		}
		nodeName := over.node.Node.Name
		for _, pod := range over.node.Pods {
			if pod == nil {
				continue
			}
			// Skip DaemonSet pods
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

			// Calculate pod's CPU request
			var podCPU int64
			for _, c := range pod.Spec.Containers {
				podCPU += c.Resources.Requests.Cpu().MilliValue()
			}
			if podCPU == 0 {
				continue
			}

			// Find an underloaded node with enough free capacity
			for j := range underloaded {
				if underloaded[j].freeCPU > 0 && underloaded[j].freeCPU >= podCPU {
					targetName := ""
					if underloaded[j].node.Node != nil {
						targetName = underloaded[j].node.Node.Name
					}

					movablePods = append(movablePods, fmt.Sprintf("%s/%s", pod.Namespace, pod.Name))
					if targetName != "" {
						targetNodes = append(targetNodes, targetName)
					}
					// Reduce free capacity on the target node
					underloaded[j].freeCPU -= podCPU
					podsAffected++
					break
				}
			}
		}

		_ = nodeName // nodeName is included via overloadedNames below
	}

	// Build the overloaded/underloaded node name lists
	var overloadedNames, underloadedNames []string
	for _, n := range overloaded {
		if n.node.Node != nil {
			overloadedNames = append(overloadedNames, n.node.Node.Name)
		}
	}
	for _, n := range underloaded {
		if n.node.Node != nil {
			underloadedNames = append(underloadedNames, n.node.Node.Name)
		}
	}

	details := map[string]string{
		"spread":           fmt.Sprintf("%.1f", spread),
		"minUtil":          fmt.Sprintf("%.1f", nodes[0].cpuPct),
		"maxUtil":          fmt.Sprintf("%.1f", nodes[len(nodes)-1].cpuPct),
		"avgUtil":          fmt.Sprintf("%.1f", avgUtil),
		"overloadedNodes":  strings.Join(overloadedNames, ","),
		"underloadedNodes": strings.Join(underloadedNames, ","),
	}

	if len(movablePods) > 0 {
		details["movablePods"] = strings.Join(movablePods, ",")
	}
	if len(targetNodes) > 0 {
		details["targetNodes"] = strings.Join(targetNodes, ",")
	}
	// Use the most overloaded node as the primary target for execution
	if len(overloadedNames) > 0 {
		details["nodeName"] = overloadedNames[0]
	}

	// Require AI Gate approval for operations affecting many nodes.
	requiresGate := len(overloaded)+len(underloaded) > 3 || podsAffected > 10

	return []optimizer.Recommendation{
		{
			ID:             fmt.Sprintf("rebalance-%d", time.Now().Unix()),
			Type:           optimizer.RecommendationRebalance,
			Priority:       optimizer.PriorityLow,
			AutoExecutable: true,
			RequiresAIGate: requiresGate,
			TargetKind:     "Cluster",
			TargetName:     "cluster",
			Summary:        fmt.Sprintf("Rebalance cluster: utilization spread is %.0f%% (min: %.0f%%, max: %.0f%%, avg: %.0f%%), %d pods to move", spread, nodes[0].cpuPct, nodes[len(nodes)-1].cpuPct, avgUtil, podsAffected),
			ActionSteps: []string{
				fmt.Sprintf("Cordon overloaded nodes: %s", strings.Join(overloadedNames, ", ")),
				fmt.Sprintf("Evict %d selected pods (PDB-aware)", podsAffected),
				fmt.Sprintf("Target underloaded nodes for scheduling: %s", strings.Join(underloadedNames, ", ")),
				"Let scheduler place pods on less loaded nodes",
				"Uncordon source nodes after rescheduling completes",
			},
			EstimatedImpact: optimizer.ImpactEstimate{
				NodesAffected: len(overloaded) + len(underloaded),
				PodsAffected:  podsAffected,
				RiskLevel:     "low",
			},
			Details:   details,
			CreatedAt: time.Now(),
		},
	}, nil
}

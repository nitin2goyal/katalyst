package evictor

import (
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// FragmentationScorer scores nodes by how fragmented their resource usage is.
type FragmentationScorer struct{}

func NewFragmentationScorer() *FragmentationScorer {
	return &FragmentationScorer{}
}

// NodeScore represents the fragmentation score for a single node.
type NodeScore struct {
	NodeName       string
	Score          float64 // 0 = fully packed, 1 = completely empty
	CPUFree        int64   // millicores
	MemFree        int64   // bytes
	CPUUtilPct     float64
	MemUtilPct     float64
	PodCount       int
	IsCandidate    bool    // true if node is a good consolidation candidate
}

// Score calculates fragmentation scores for all nodes.
func (f *FragmentationScorer) Score(snapshot *optimizer.ClusterSnapshot) []NodeScore {
	scores := make([]NodeScore, 0, len(snapshot.Nodes))

	for _, node := range snapshot.Nodes {
		cpuUtil := float64(0)
		if node.CPUCapacity > 0 {
			cpuUtil = float64(node.CPURequested) / float64(node.CPUCapacity) * 100
		}
		memUtil := float64(0)
		if node.MemoryCapacity > 0 {
			memUtil = float64(node.MemoryRequested) / float64(node.MemoryCapacity) * 100
		}

		// Score: average of free CPU% and free Memory%
		cpuFreeRatio := 1.0 - float64(node.CPURequested)/float64(max64(node.CPUCapacity, 1))
		memFreeRatio := 1.0 - float64(node.MemoryRequested)/float64(max64(node.MemoryCapacity, 1))
		score := (cpuFreeRatio + memFreeRatio) / 2.0

		// Filter out DaemonSet pods
		nonDSPods := 0
		for _, pod := range node.Pods {
			isDaemonSet := false
			for _, ref := range pod.OwnerReferences {
				if ref.Kind == "DaemonSet" {
					isDaemonSet = true
					break
				}
			}
			if !isDaemonSet {
				nonDSPods++
			}
		}

		scores = append(scores, NodeScore{
			NodeName:    node.Node.Name,
			Score:       score,
			CPUFree:     node.CPUCapacity - node.CPURequested,
			MemFree:     node.MemoryCapacity - node.MemoryRequested,
			CPUUtilPct:  cpuUtil,
			MemUtilPct:  memUtil,
			PodCount:    nonDSPods,
			IsCandidate: score > 0.6, // >60% free = consolidation candidate
		})
	}

	return scores
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

package nodeautoscaler

import (
	"fmt"
	"sort"

	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// BinPacker simulates bin-packing to determine the minimum number of nodes needed.
type BinPacker struct{}

func NewBinPacker() *BinPacker {
	return &BinPacker{}
}

// PackResult represents the result of a bin-packing simulation.
type PackResult struct {
	MinNodesNeeded int
	CurrentNodes   int
	CanConsolidate bool
	NodesSaved     int
	Assignments    map[string]string // pod key -> node name
}

// Pack runs a first-fit-decreasing bin-packing simulation.
func (b *BinPacker) Pack(snapshot *optimizer.ClusterSnapshot) *PackResult {
	if len(snapshot.Nodes) == 0 {
		return &PackResult{}
	}

	// Collect all non-daemonset pod requests
	type podReq struct {
		key string
		cpu int64
		mem int64
	}

	var pods []podReq
	for _, n := range snapshot.Nodes {
		for _, pod := range n.Pods {
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
			cpuReq := int64(0)
			memReq := int64(0)
			for _, c := range pod.Spec.Containers {
				if cpu, ok := c.Resources.Requests["cpu"]; ok {
					cpuReq += cpu.MilliValue()
				}
				if mem, ok := c.Resources.Requests["memory"]; ok {
					memReq += mem.Value()
				}
			}
			pods = append(pods, podReq{
				key: pod.Namespace + "/" + pod.Name,
				cpu: cpuReq,
				mem: memReq,
			})
		}
	}

	// Sort pods by CPU request descending (FFD)
	sort.Slice(pods, func(i, j int) bool {
		return pods[i].cpu > pods[j].cpu
	})

	// Use typical node capacity (from first node)
	nodeCPU := snapshot.Nodes[0].CPUCapacity
	nodeMem := snapshot.Nodes[0].MemoryCapacity

	// Reserve 10% for system overhead
	nodeCPU = nodeCPU * 90 / 100
	nodeMem = nodeMem * 90 / 100

	// First-fit decreasing bin packing
	type bin struct {
		cpuFree int64
		memFree int64
	}

	var bins []bin
	assignments := make(map[string]string)

	for _, pod := range pods {
		placed := false
		for i := range bins {
			if bins[i].cpuFree >= pod.cpu && bins[i].memFree >= pod.mem {
				bins[i].cpuFree -= pod.cpu
				bins[i].memFree -= pod.mem
				assignments[pod.key] = fmt.Sprintf("node-%d", i)
				placed = true
				break
			}
		}
		if !placed {
			bins = append(bins, bin{
				cpuFree: nodeCPU - pod.cpu,
				memFree: nodeMem - pod.mem,
			})
			assignments[pod.key] = fmt.Sprintf("node-%d", len(bins)-1)
		}
	}

	return &PackResult{
		MinNodesNeeded: len(bins),
		CurrentNodes:   len(snapshot.Nodes),
		CanConsolidate: len(bins) < len(snapshot.Nodes),
		NodesSaved:     len(snapshot.Nodes) - len(bins),
		Assignments:    assignments,
	}
}

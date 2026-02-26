package state

import (
	"sync"
	"time"

	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
)

// NodeGroupState tracks node groups and their associated nodes.
type NodeGroupState struct {
	mu     sync.RWMutex
	groups map[string]*NodeGroupInfo
}

// NodeGroupInfo is the enriched state of a node group.
type NodeGroupInfo struct {
	*cloudprovider.NodeGroup
	Nodes           []*NodeState
	TotalCPU        int64 // total capacity millicores
	TotalMemory     int64 // total capacity bytes
	UsedCPU         int64
	UsedMemory      int64
	RequestedCPU    int64
	RequestedMemory int64
	TotalPods       int
	MonthlyCostUSD  float64
	EmptySince      *int64 // unix timestamp, nil if not empty
}

// NewNodeGroupState creates a new NodeGroupState.
func NewNodeGroupState() *NodeGroupState {
	return &NodeGroupState{
		groups: make(map[string]*NodeGroupInfo),
	}
}

// Update refreshes the node group state with the latest data.
func (s *NodeGroupState) Update(groups []*cloudprovider.NodeGroup, nodes []*NodeState) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Build node-to-group mapping
	nodesByGroup := make(map[string][]*NodeState)
	for _, n := range nodes {
		if n.NodeGroupID != "" {
			nodesByGroup[n.NodeGroupID] = append(nodesByGroup[n.NodeGroupID], n)
		}
	}

	newGroups := make(map[string]*NodeGroupInfo, len(groups))
	for _, g := range groups {
		info := &NodeGroupInfo{
			NodeGroup: g,
			Nodes:     nodesByGroup[g.ID],
		}

		for _, n := range info.Nodes {
			info.TotalCPU += n.CPUCapacity
			info.TotalMemory += n.MemoryCapacity
			info.UsedCPU += n.CPUUsed
			info.UsedMemory += n.MemoryUsed
			info.RequestedCPU += n.CPURequested
			info.RequestedMemory += n.MemoryRequested
			info.MonthlyCostUSD += n.HourlyCostUSD * 730
			for _, pod := range n.Pods {
				if !isDaemonSetPod(pod) {
					info.TotalPods++
				}
			}
		}

		// Track empty state
		if prev, ok := s.groups[g.ID]; ok && prev.EmptySince != nil && info.TotalPods == 0 {
			info.EmptySince = prev.EmptySince
		} else if info.TotalPods == 0 {
			now := currentTimeUnix()
			info.EmptySince = &now
		}

		newGroups[g.ID] = info
	}

	s.groups = newGroups
}

// Get returns a node group by ID.
func (s *NodeGroupState) Get(id string) (*NodeGroupInfo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g, ok := s.groups[id]
	return g, ok
}

// GetAll returns all node groups.
func (s *NodeGroupState) GetAll() []*NodeGroupInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*NodeGroupInfo, 0, len(s.groups))
	for _, g := range s.groups {
		result = append(result, g)
	}
	return result
}

// CPUUtilization returns CPU utilization percentage for a node group.
func (i *NodeGroupInfo) CPUUtilization() float64 {
	if i.TotalCPU == 0 {
		return 0
	}
	return float64(i.UsedCPU) / float64(i.TotalCPU) * 100
}

// MemoryUtilization returns memory utilization percentage for a node group.
func (i *NodeGroupInfo) MemoryUtilization() float64 {
	if i.TotalMemory == 0 {
		return 0
	}
	return float64(i.UsedMemory) / float64(i.TotalMemory) * 100
}

// CPUAllocation returns CPU requests as a percentage of capacity for a node group.
func (i *NodeGroupInfo) CPUAllocation() float64 {
	if i.TotalCPU == 0 {
		return 0
	}
	return float64(i.RequestedCPU) / float64(i.TotalCPU) * 100
}

// MemoryAllocation returns memory requests as a percentage of capacity for a node group.
func (i *NodeGroupInfo) MemoryAllocation() float64 {
	if i.TotalMemory == 0 {
		return 0
	}
	return float64(i.RequestedMemory) / float64(i.TotalMemory) * 100
}

// IsEmpty returns true if the node group has no non-daemonset pods.
func (i *NodeGroupInfo) IsEmpty() bool {
	return i.TotalPods == 0
}

func currentTimeUnix() int64 {
	return time.Now().Unix()
}

package familylock

import (
	"context"
	"fmt"
	"sync"

	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
)

// NodeGroupAction represents an action on a node group.
type NodeGroupAction int

const (
	NodeGroupScale NodeGroupAction = iota
	NodeGroupCreate
	NodeGroupDelete
	NodeGroupModifyMin
	NodeGroupModifyMax
	NodeGroupChangeType
)

// FamilyLockGuard prevents any operation that would change instance families
// or create new node groups. This is the core safety mechanism.
type FamilyLockGuard struct {
	mu         sync.RWMutex
	nodeGroups map[string]*cloudprovider.NodeGroup
	provider   cloudprovider.CloudProvider
}

// NewFamilyLockGuard creates a new FamilyLockGuard.
func NewFamilyLockGuard(provider cloudprovider.CloudProvider) *FamilyLockGuard {
	return &FamilyLockGuard{
		nodeGroups: make(map[string]*cloudprovider.NodeGroup),
		provider:   provider,
	}
}

// Refresh discovers current node groups and caches them.
func (g *FamilyLockGuard) Refresh(ctx context.Context) error {
	groups, err := g.provider.DiscoverNodeGroups(ctx)
	if err != nil {
		return fmt.Errorf("discovering node groups: %w", err)
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.nodeGroups = make(map[string]*cloudprovider.NodeGroup, len(groups))
	for _, ng := range groups {
		g.nodeGroups[ng.ID] = ng
	}
	return nil
}

// ValidateScaleUp ensures we only add nodes of the SAME type as the node group already uses.
// If the node group is not in cache, it attempts a one-time auto-refresh from the cloud provider.
func (g *FamilyLockGuard) ValidateScaleUp(nodeGroupID string, proposedType string) error {
	return g.ValidateScaleUpCtx(context.Background(), nodeGroupID, proposedType)
}

// ValidateScaleUpCtx is like ValidateScaleUp but accepts a context for cancellation/timeout.
func (g *FamilyLockGuard) ValidateScaleUpCtx(ctx context.Context, nodeGroupID string, proposedType string) error {
	g.mu.RLock()
	ng, ok := g.nodeGroups[nodeGroupID]
	g.mu.RUnlock()

	if !ok {
		// Auto-refresh: the node group may have been created since last refresh
		if err := g.Refresh(ctx); err == nil {
			g.mu.RLock()
			ng, ok = g.nodeGroups[nodeGroupID]
			g.mu.RUnlock()
		}
		if !ok {
			return fmt.Errorf("unknown node group: %s", nodeGroupID)
		}
	}

	currentFamily, err := ExtractFamily(ng.InstanceType)
	if err != nil {
		return fmt.Errorf("extracting current family: %w", err)
	}

	proposedFamily, err := ExtractFamily(proposedType)
	if err != nil {
		return fmt.Errorf("extracting proposed family: %w", err)
	}

	if currentFamily != proposedFamily {
		return fmt.Errorf("BLOCKED: cannot change family from %s to %s in node group %s",
			currentFamily, proposedFamily, ng.Name)
	}
	return nil
}

// ValidateNodeGroupAction blocks creating new node groups and changing instance types.
func (g *FamilyLockGuard) ValidateNodeGroupAction(action NodeGroupAction) error {
	switch action {
	case NodeGroupCreate:
		return fmt.Errorf("BLOCKED: creating new node groups is not allowed")
	case NodeGroupChangeType:
		return fmt.Errorf("BLOCKED: changing node group instance type is not allowed")
	case NodeGroupDelete:
		return fmt.Errorf("BLOCKED: deleting node groups requires manual approval")
	case NodeGroupScale, NodeGroupModifyMin, NodeGroupModifyMax:
		return nil // These are allowed
	default:
		return fmt.Errorf("unknown node group action: %d", action)
	}
}

// GetAllowedSizes returns valid sizes within the same instance family.
func (g *FamilyLockGuard) GetAllowedSizes(ctx context.Context, currentType string) ([]*cloudprovider.InstanceType, error) {
	return g.provider.GetFamilySizes(ctx, currentType)
}

// GetNodeGroup returns a cached node group by ID.
func (g *FamilyLockGuard) GetNodeGroup(id string) (*cloudprovider.NodeGroup, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	ng, ok := g.nodeGroups[id]
	return ng, ok
}

// GetAllNodeGroups returns all cached node groups.
func (g *FamilyLockGuard) GetAllNodeGroups() []*cloudprovider.NodeGroup {
	g.mu.RLock()
	defer g.mu.RUnlock()
	groups := make([]*cloudprovider.NodeGroup, 0, len(g.nodeGroups))
	for _, ng := range g.nodeGroups {
		groups = append(groups, ng)
	}
	return groups
}

package commitments

import (
	"context"
	"strings"

	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
)

// UtilizationTracker tracks how well commitments are being utilized.
type UtilizationTracker struct {
	provider cloudprovider.CloudProvider
}

func NewUtilizationTracker(provider cloudprovider.CloudProvider) *UtilizationTracker {
	return &UtilizationTracker{provider: provider}
}

// UpdateUtilization calculates utilization for each commitment.
func (t *UtilizationTracker) UpdateUtilization(ctx context.Context, commitments []*cloudprovider.Commitment) error {
	// Discover current node groups to match against commitments
	nodeGroups, err := t.provider.DiscoverNodeGroups(ctx)
	if err != nil {
		return err
	}

	for _, c := range commitments {
		if c.Status != "active" {
			c.UtilizationPct = 0
			continue
		}

		// Count how many instances of this commitment type are running
		runningCount := 0
		for _, ng := range nodeGroups {
			if matchesCommitment(c, ng) {
				runningCount += ng.CurrentCount
			}
		}

		if c.Count > 0 {
			c.UtilizationPct = float64(runningCount) / float64(c.Count) * 100
			if c.UtilizationPct > 100 {
				c.UtilizationPct = 100
			}
		}
	}

	return nil
}

// matchesCommitment checks if a node group consumes a commitment.
// Matches on instance type or family, and validates region when both sides have it.
func matchesCommitment(c *cloudprovider.Commitment, ng *cloudprovider.NodeGroup) bool {
	// Region must match when both commitment and node group specify one.
	if c.Region != "" && ng.Region != "" && !strings.EqualFold(c.Region, ng.Region) {
		return false
	}

	// For instance-type-specific commitments
	if c.InstanceType != "" {
		return c.InstanceType == ng.InstanceType
	}
	// For family-flexible commitments (Savings Plans, CUDs)
	if c.InstanceFamily != "" {
		return strings.EqualFold(c.InstanceFamily, ng.InstanceFamily)
	}
	return false
}

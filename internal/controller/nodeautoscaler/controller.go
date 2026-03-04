package nodeautoscaler

import (
	"context"
	"fmt"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/internal/controller/evictor"
	"github.com/koptimizer/koptimizer/internal/state"
	"github.com/koptimizer/koptimizer/pkg/aigate"
	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
	"github.com/koptimizer/koptimizer/pkg/familylock"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// Controller handles node autoscaling within existing node groups.
type Controller struct {
	client      client.Client
	provider    cloudprovider.CloudProvider
	state       *state.ClusterState
	guard       *familylock.FamilyLockGuard
	gate        *aigate.AIGate
	config      *config.Config
	upscaler    *Upscaler
	downscaler  *Downscaler
	binPacker   *BinPacker
	sizeAdvisor *SizeAdvisor
	drainer     *evictor.Drainer
}

func NewController(mgr ctrl.Manager, provider cloudprovider.CloudProvider, st *state.ClusterState, guard *familylock.FamilyLockGuard, gate *aigate.AIGate, cfg *config.Config) *Controller {
	c := mgr.GetClient()
	return &Controller{
		client:      c,
		provider:    provider,
		state:       st,
		guard:       guard,
		gate:        gate,
		config:      cfg,
		upscaler:    NewUpscaler(provider, guard, cfg),
		downscaler:  NewDownscaler(provider, guard, gate, cfg),
		binPacker:   NewBinPacker(),
		sizeAdvisor: NewSizeAdvisor(provider, guard),
		drainer:     evictor.NewDrainer(c, cfg),
	}
}

func (c *Controller) SetupWithManager(mgr ctrl.Manager) error {
	return mgr.Add(c)
}

// Start implements manager.Runnable.
func (c *Controller) Start(ctx context.Context) error {
	c.run(ctx)
	return nil
}

func (c *Controller) Name() string { return "node-autoscaler" }

func (c *Controller) Analyze(ctx context.Context, snapshot *optimizer.ClusterSnapshot) ([]optimizer.Recommendation, error) {
	var recs []optimizer.Recommendation

	// Check for scale-down opportunities first (underutilized nodes) — these
	// take priority because removing underutilized nodes saves cost. Scale-up
	// recs for node groups that already have a scale-down pending would cause
	// conflicting operations (simultaneous add and remove on the same pool).
	downRecs, err := c.downscaler.Analyze(ctx, snapshot)
	if err != nil {
		return nil, err
	}
	recs = append(recs, downRecs...)

	// Build set of node groups with pending scale-down
	scaleDownGroups := make(map[string]bool, len(downRecs))
	for _, r := range downRecs {
		if gid, ok := r.Details["nodeGroupID"]; ok {
			scaleDownGroups[gid] = true
		}
	}

	// Check for scale-up needs (unschedulable pods), but skip node groups
	// that already have a scale-down recommendation to avoid conflicts.
	upRecs, err := c.upscaler.Analyze(ctx, snapshot)
	if err != nil {
		return nil, err
	}
	for _, r := range upRecs {
		if gid, ok := r.Details["nodeGroupID"]; ok && scaleDownGroups[gid] {
			continue // skip conflicting scale-up
		}
		recs = append(recs, r)
	}

	// Generate within-family size recommendations
	sizeRecs, err := c.sizeAdvisor.Analyze(ctx, snapshot)
	if err != nil {
		return nil, err
	}
	recs = append(recs, sizeRecs...)

	return recs, nil
}

func (c *Controller) Execute(ctx context.Context, rec optimizer.Recommendation) error {
	logger := log.FromContext(ctx).WithName("nodeautoscaler")
	if c.config.Mode != "active" || c.config.NodeAutoscaler.DryRun {
		if c.config.NodeAutoscaler.DryRun {
			logger.Info("Dry-run: would execute node scaling",
				"recommendation", rec.ID,
				"summary", rec.Summary,
			)
			c.state.AuditLog.Record("dry-run-scale", rec.TargetName, "nodeautoscaler", rec.Summary)
		}
		return nil
	}
	if !rec.AutoExecutable {
		return nil
	}

	// AI Gate validation — fail-closed: use RequiresValidation which checks
	// both the RequiresAIGate flag AND actual impact metrics (cost, nodes affected).
	if c.gate.RequiresValidation(rec) {
		valReq := aigate.ValidationRequest{
			Action:         rec.Summary,
			Recommendation: rec,
		}
		result, err := c.gate.Validate(ctx, valReq)
		if err != nil || !result.Approved {
			return nil // Falls back to recommendation mode
		}
	}

	switch rec.Type {
	case optimizer.RecommendationNodeScale:
		return c.executeScale(ctx, rec)
	}
	return nil
}

func (c *Controller) executeScale(ctx context.Context, rec optimizer.Recommendation) error {
	logger := log.FromContext(ctx).WithName("nodeautoscaler")
	nodeGroupID := rec.Details["nodeGroupID"]
	desiredStr := rec.Details["desiredCount"]
	direction := rec.Details["direction"]
	var desired int
	fmt.Sscanf(desiredStr, "%d", &desired)

	// Family lock validation
	if err := c.guard.ValidateNodeGroupAction(familylock.NodeGroupScale); err != nil {
		return err
	}

	c.state.AuditLog.Record("scale-nodegroup", nodeGroupID, "nodeautoscaler", rec.Summary)

	// For scale-down, drain underutilized nodes before reducing desired count
	// to ensure pods are gracefully evicted rather than force-killed by the
	// cloud provider.
	var drainedNodes []string
	var failedDrainNodes []string // nodes left cordoned after drain failure
	if direction == "down" {
		snapshot := c.state.Snapshot()
		drained := 0
		nodesToRemove := rec.EstimatedImpact.NodesAffected

		for _, n := range snapshot.Nodes {
			if drained >= nodesToRemove {
				break
			}
			if n.NodeGroup != nodeGroupID {
				continue
			}
			// Pick underutilized nodes
			cpuUtil := float64(0)
			if n.CPUCapacity > 0 {
				cpuUtil = float64(n.CPUUsed) / float64(n.CPUCapacity) * 100
			}
			if cpuUtil >= c.config.NodeAutoscaler.ScaleDownThreshold {
				continue
			}

			nodeName := n.Node.Name
			if err := c.state.NodeLock.TryLock(nodeName, "nodeautoscaler"); err != nil {
				logger.Info("Skipping node, locked by another controller", "node", nodeName, "error", err)
				continue
			}

			c.state.AuditLog.Record("drain-before-scaledown", nodeName, "nodeautoscaler",
				fmt.Sprintf("draining node before scaling %s to %d", nodeGroupID, desired))

			if err := c.drainer.DrainNode(ctx, nodeName); err != nil {
				logger.Error(err, "Failed to drain node before scale-down", "node", nodeName)
				c.state.NodeLock.Unlock(nodeName, "nodeautoscaler")
				c.state.AuditLog.Record("drain-failed", nodeName, "nodeautoscaler",
					fmt.Sprintf("drain failed: %v", err))
				failedDrainNodes = append(failedDrainNodes, nodeName)
				continue
			}
			c.state.NodeLock.Unlock(nodeName, "nodeautoscaler")
			drainedNodes = append(drainedNodes, nodeName)
			drained++
		}

		// Safety: do not scale down if no nodes were successfully drained.
		// Without this guard the cloud provider would terminate nodes whose
		// pods were never gracefully evicted, causing service disruption.
		if drained == 0 {
			logger.Info("No nodes drained successfully, aborting scale-down to prevent ungraceful termination",
				"nodeGroup", nodeGroupID, "desiredCount", desired)
			c.state.AuditLog.Record("scale-down-aborted", nodeGroupID, "nodeautoscaler",
				"no nodes drained successfully, scale-down aborted for safety")
			// Uncordon nodes left cordoned by failed drains — they won't be
			// removed since scale-down was aborted, so don't leave them in limbo.
			c.uncordonFailedDrainNodes(ctx, failedDrainNodes)
			return nil
		}

		// Adjust desired count if fewer nodes were drained than planned.
		// Without this, the cloud provider would terminate un-drained nodes
		// whose pods were never gracefully evicted.
		if drained < nodesToRemove {
			// Only remove the nodes we actually drained:
			// original count = desired + nodesToRemove, so new target = original - drained
			adjustedDesired := desired + (nodesToRemove - drained)
			logger.Info("Partial drain: adjusting desired count",
				"nodeGroup", nodeGroupID, "originalDesired", desired,
				"adjustedDesired", adjustedDesired, "drained", drained, "planned", nodesToRemove)
			c.state.AuditLog.Record("scale-down-adjusted", nodeGroupID, "nodeautoscaler",
				fmt.Sprintf("only %d/%d nodes drained, adjusting desired from %d to %d", drained, nodesToRemove, desired, adjustedDesired))
			desired = adjustedDesired
			// Uncordon nodes whose drains failed — they won't be removed by
			// the adjusted scale-down, so don't leave them stuck cordoned.
			c.uncordonFailedDrainNodes(ctx, failedDrainNodes)
		}
	}

	err := c.provider.ScaleNodeGroup(ctx, nodeGroupID, desired)
	if err != nil {
		c.state.AuditLog.Record("scale-nodegroup-failed", nodeGroupID, "nodeautoscaler", err.Error())

		// If scale-down failed after draining, uncordon the drained nodes so
		// they remain usable. Without this, nodes stay cordoned indefinitely
		// when the cloud API rejects the scale operation (e.g. 403 permission).
		if direction == "down" && len(drainedNodes) > 0 {
			logger.Info("Scale-down failed, uncordoning drained nodes to restore capacity",
				"nodeGroup", nodeGroupID, "drainedNodes", len(drainedNodes))
			for _, nodeName := range drainedNodes {
				if uncordErr := c.drainer.UncordonAndCleanup(ctx, nodeName); uncordErr != nil {
					logger.Error(uncordErr, "Failed to uncordon node after scale-down failure", "node", nodeName)
				} else {
					c.state.AuditLog.Record("uncordon-after-scale-failure", nodeName, "nodeautoscaler",
						fmt.Sprintf("uncordoned after scale-down API failure for %s", nodeGroupID))
				}
			}
		}
	} else {
		c.state.AuditLog.Record("scale-nodegroup-complete", nodeGroupID, "nodeautoscaler",
			fmt.Sprintf("scaled to %d nodes", desired))
	}
	return err
}

// uncordonFailedDrainNodes uncordons nodes that were left cordoned after drain
// failures. Without this, partially-drained nodes stay cordoned indefinitely
// when scale-down is aborted or adjusted — stuck in limbo.
func (c *Controller) uncordonFailedDrainNodes(ctx context.Context, nodes []string) {
	if len(nodes) == 0 {
		return
	}
	logger := log.FromContext(ctx).WithName("nodeautoscaler")
	logger.Info("Uncordoning nodes left cordoned by drain failures",
		"count", len(nodes), "nodes", nodes)
	for _, nodeName := range nodes {
		if err := c.drainer.UncordonAndCleanup(ctx, nodeName); err != nil {
			logger.Error(err, "Failed to uncordon node after drain failure", "node", nodeName)
		} else {
			c.state.AuditLog.Record("uncordon-after-drain-failure", nodeName, "nodeautoscaler",
				"uncordoned node that was left cordoned by failed drain during scale-down")
		}
	}
}

func (c *Controller) run(ctx context.Context) {
	logger := log.FromContext(ctx).WithName("nodeautoscaler")
	ticker := time.NewTicker(c.config.NodeAutoscaler.ScanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if c.state.Breaker.IsTripped(c.Name()) {
				logger.V(1).Info("Circuit breaker tripped, skipping execution cycle")
				continue
			}
			snapshot := c.state.Snapshot()
			recs, err := c.Analyze(ctx, snapshot)
			if err != nil {
				logger.Error(err, "Analysis failed")
				c.state.Breaker.RecordFailure(c.Name())
				continue
			}
			for _, rec := range recs {
				if err := c.Execute(ctx, rec); err != nil {
					logger.Error(err, "Execution failed", "recommendation", rec.ID)
					c.state.Breaker.RecordFailure(c.Name())
				} else {
					c.state.Breaker.RecordSuccess(c.Name())
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

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

	// Check for scale-up needs (unschedulable pods)
	upRecs, err := c.upscaler.Analyze(ctx, snapshot)
	if err != nil {
		return nil, err
	}
	recs = append(recs, upRecs...)

	// Check for scale-down opportunities (underutilized nodes)
	downRecs, err := c.downscaler.Analyze(ctx, snapshot)
	if err != nil {
		return nil, err
	}
	recs = append(recs, downRecs...)

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

	// AI Gate validation for risky changes — fail-closed: if the gate is nil
	// and the recommendation requires validation, block execution.
	if rec.RequiresAIGate {
		if c.gate == nil {
			return nil // AI Gate required but not configured; fall back to recommendation mode
		}
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
				logger.Error(err, "Failed to drain node before scale-down, keeping node cordoned for safety", "node", nodeName)
				c.state.NodeLock.Unlock(nodeName, "nodeautoscaler")
				c.state.AuditLog.Record("drain-failed-kept-cordoned", nodeName, "nodeautoscaler",
					fmt.Sprintf("drain failed, node left cordoned for manual review: %v", err))
				continue
			}
			c.state.NodeLock.Unlock(nodeName, "nodeautoscaler")
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
			return nil
		}
	}

	err := c.provider.ScaleNodeGroup(ctx, nodeGroupID, desired)
	if err != nil {
		c.state.AuditLog.Record("scale-nodegroup-failed", nodeGroupID, "nodeautoscaler", err.Error())
	} else {
		c.state.AuditLog.Record("scale-nodegroup-complete", nodeGroupID, "nodeautoscaler",
			fmt.Sprintf("scaled to %d nodes", desired))
	}
	return err
}

func (c *Controller) run(ctx context.Context) {
	logger := log.FromContext(ctx).WithName("nodeautoscaler")
	ticker := time.NewTicker(c.config.NodeAutoscaler.ScanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			snapshot := c.state.Snapshot()
			recs, err := c.Analyze(ctx, snapshot)
			if err != nil {
				logger.Error(err, "Analysis failed")
				continue
			}
			for _, rec := range recs {
				if err := c.Execute(ctx, rec); err != nil {
					logger.Error(err, "Execution failed", "recommendation", rec.ID)
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

package nodeautoscaler

import (
	"context"
	"fmt"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	corev1 "k8s.io/api/core/v1"

	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/internal/controller/evictor"
	"github.com/koptimizer/koptimizer/internal/scheduler"
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
	simulator   *scheduler.Simulator
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
		simulator:   scheduler.NewSimulator(),
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
		attempted := 0
		nodesToRemove := rec.EstimatedImpact.NodesAffected
		maxAttempts := c.config.NodeAutoscaler.MaxScaleDownNodes

		for _, n := range snapshot.Nodes {
			if drained >= nodesToRemove {
				break
			}
			// Stop after max attempts — don't keep cordoning nodes on failures.
			// Without this, partial drain failures (node stays cordoned, error returned)
			// let the loop iterate through every node in the group.
			if attempted >= maxAttempts {
				logger.Info("Max scale-down attempts reached, stopping",
					"attempted", attempted, "drained", drained, "maxAttempts", maxAttempts)
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

			attempted++

			// Pre-drain feasibility: check PDBs and pod placement before cordoning.
			// This avoids the cordon→fail→uncordon churn when a drain is doomed.
			if err := c.preDrainFeasibilityCheck(ctx, snapshot, nodeName); err != nil {
				logger.Info("Skipping node, pre-drain check failed",
					"node", nodeName, "reason", err.Error())
				c.state.NodeLock.Unlock(nodeName, "nodeautoscaler")
				c.state.AuditLog.Record("drain-skipped", nodeName, "nodeautoscaler",
					fmt.Sprintf("pre-drain check: %v", err))
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
				// Stop on first drain failure — don't cascade cordon more nodes
				break
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

	// Scale-down: drain is sufficient. Drained+cordoned nodes sit empty and
	// the cloud provider's own autoscaler handles actual node removal. This
	// avoids needing GCP IAM permissions for setSize and keeps the optimizer
	// non-intrusive (no direct node pool mutations).
	//
	// Scale-up: not auto-executed. Recommendations are generated for review
	// but adding nodes to the cloud provider is left to the native autoscaler
	// or manual intervention.
	if direction == "down" && len(drainedNodes) > 0 {
		c.state.AuditLog.Record("scale-nodegroup-complete", nodeGroupID, "nodeautoscaler",
			fmt.Sprintf("drained %d nodes (cordoned, awaiting cloud autoscaler removal)", len(drainedNodes)))
	} else if direction == "up" {
		logger.Info("Scale-up recommendation generated, skipping cloud API call (left to native autoscaler)",
			"nodeGroup", nodeGroupID, "desiredCount", desired)
		c.state.AuditLog.Record("scale-up-deferred", nodeGroupID, "nodeautoscaler",
			fmt.Sprintf("scale-up to %d deferred to native autoscaler", desired))
	}
	return nil
}

// preDrainFeasibilityCheck verifies that draining a node will likely succeed
// by checking two things:
// 1. PDB check: are there pods that can actually be evicted?
// 2. Capacity check: can the evictable pods fit on remaining nodes?
// This prevents the cordon→fail→uncordon churn that blocks all scale-downs.
func (c *Controller) preDrainFeasibilityCheck(ctx context.Context, snapshot *optimizer.ClusterSnapshot, nodeName string) error {
	logger := log.FromContext(ctx).WithName("nodeautoscaler")

	// 1. PDB pre-check (uses live API state)
	if err := c.drainer.PreDrainCheck(ctx, nodeName); err != nil {
		return err
	}

	// 2. Capacity pre-check: simulate placing this node's pods on remaining nodes.
	// Build node and pod maps from the snapshot, excluding the target node.
	var targetNodeInfo *optimizer.NodeInfo
	var otherNodes []*corev1.Node
	podsByNode := make(map[string][]*corev1.Pod)

	for i := range snapshot.Nodes {
		n := &snapshot.Nodes[i]
		if n.Node.Name == nodeName {
			targetNodeInfo = n
			continue
		}
		// Skip nodes that are already unschedulable — they can't accept pods.
		if n.Node.Spec.Unschedulable {
			continue
		}
		otherNodes = append(otherNodes, n.Node)
		podsByNode[n.Node.Name] = n.Pods
	}

	if targetNodeInfo == nil {
		return fmt.Errorf("node %s not found in snapshot", nodeName)
	}

	// Check each non-DaemonSet, non-system pod can be placed somewhere.
	unplaceable := 0
	for _, pod := range targetNodeInfo.Pods {
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
		// Skip system namespaces — these pods won't be evicted.
		if pod.Namespace == "kube-system" || pod.Namespace == "kube-public" || pod.Namespace == "kube-node-lease" {
			continue
		}

		fitting := c.simulator.FindFittingNodes(pod, otherNodes, podsByNode)
		if len(fitting) == 0 {
			unplaceable++
			logger.V(1).Info("Pod has no placement target",
				"pod", pod.Namespace+"/"+pod.Name, "node", nodeName)
		} else {
			// Accumulate: reserve capacity on the target node so subsequent
			// pods see reduced capacity (same approach as consolidation planner).
			podsByNode[fitting[0]] = append(podsByNode[fitting[0]], pod)
		}
	}

	if unplaceable > 0 {
		return fmt.Errorf("%d pods on %s cannot be placed on remaining nodes", unplaceable, nodeName)
	}

	return nil
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

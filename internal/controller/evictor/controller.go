package evictor

import (
	"context"
	"fmt"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/internal/state"
	"github.com/koptimizer/koptimizer/pkg/aigate"
	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
	"github.com/koptimizer/koptimizer/pkg/familylock"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// Controller consolidates workloads by evicting pods from underutilized nodes.
type Controller struct {
	client        client.Client
	provider      cloudprovider.CloudProvider
	state         *state.ClusterState
	guard         *familylock.FamilyLockGuard
	gate          *aigate.AIGate
	config        *config.Config
	fragScorer    *FragmentationScorer
	consolidator  *Consolidator
	drainer       *Drainer
}

func NewController(mgr ctrl.Manager, provider cloudprovider.CloudProvider, st *state.ClusterState, guard *familylock.FamilyLockGuard, gate *aigate.AIGate, cfg *config.Config) *Controller {
	c := mgr.GetClient()
	drainer := NewDrainer(c, cfg)
	// Wire up node lock refresh so long drains don't expire the lock
	drainer.SetLockRefresh(st.NodeLock.Refresh)
	return &Controller{
		client:       c,
		provider:     provider,
		state:        st,
		guard:        guard,
		gate:         gate,
		config:       cfg,
		fragScorer:   NewFragmentationScorer(),
		consolidator: NewConsolidator(cfg),
		drainer:      drainer,
	}
}

func (c *Controller) SetupWithManager(mgr ctrl.Manager) error {
	return mgr.Add(c)
}

// Start implements manager.Runnable so the manager provides a context
// that is cancelled on shutdown, preventing goroutine leaks.
func (c *Controller) Start(ctx context.Context) error {
	c.run(ctx)
	return nil
}

func (c *Controller) Name() string { return "evictor" }

func (c *Controller) Analyze(ctx context.Context, snapshot *optimizer.ClusterSnapshot) ([]optimizer.Recommendation, error) {
	// Score node fragmentation
	scores := c.fragScorer.Score(snapshot)

	// Build consolidation plan
	plan, err := c.consolidator.Plan(snapshot, scores)
	if err != nil {
		return nil, err
	}

	return plan, nil
}

func (c *Controller) Execute(ctx context.Context, rec optimizer.Recommendation) error {
	logger := log.FromContext(ctx).WithName("evictor")
	if c.config.Mode != "active" || c.config.Evictor.DryRun {
		if c.config.Evictor.DryRun {
			logger.Info("Dry-run: would execute eviction",
				"node", rec.Details["nodeName"],
				"summary", rec.Summary,
				"estimatedSavings", rec.EstimatedSaving.MonthlySavingsUSD,
			)
			c.state.AuditLog.Record("dry-run-eviction", rec.Details["nodeName"], "evictor", rec.Summary)
		}
		return nil
	}
	if !rec.AutoExecutable {
		return nil
	}

	// AI Gate for large evictions — fail-closed: if the gate is nil and the
	// threshold is exceeded, block execution rather than proceeding unsafely.
	nodesAffected := rec.EstimatedImpact.NodesAffected
	if nodesAffected > c.config.AIGate.MaxEvictNodes {
		if c.gate == nil {
			logger.Info("AI Gate required but not configured, blocking eviction for safety",
				"nodesAffected", nodesAffected, "threshold", c.config.AIGate.MaxEvictNodes)
			return nil
		}
		valReq := aigate.ValidationRequest{
			Action:         rec.Summary,
			Recommendation: rec,
			RiskFactors:    []string{fmt.Sprintf("Evicting from %d nodes simultaneously", nodesAffected)},
		}
		result, err := c.gate.Validate(ctx, valReq)
		if err != nil || !result.Approved {
			return nil
		}
	}

	// Family lock validation before draining
	if err := c.guard.ValidateNodeGroupAction(familylock.NodeGroupScale); err != nil {
		return err
	}

	nodeName := rec.Details["nodeName"]

	// Acquire node-level lock to prevent concurrent operations
	if err := c.state.NodeLock.TryLock(nodeName, "evictor"); err != nil {
		return fmt.Errorf("cannot drain node: %w", err)
	}
	defer c.state.NodeLock.Unlock(nodeName, "evictor")

	c.state.AuditLog.Record("drain-node", nodeName, "evictor", rec.Summary)
	err := c.drainer.DrainNode(ctx, nodeName)
	if err != nil {
		c.state.AuditLog.Record("drain-node-failed", nodeName, "evictor",
			fmt.Sprintf("drain failed: %v", err))
	} else {
		c.state.AuditLog.Record("drain-node-complete", nodeName, "evictor",
			fmt.Sprintf("successfully drained node, recommendation: %s", rec.ID))
	}
	return err
}

func (c *Controller) run(ctx context.Context) {
	logger := log.FromContext(ctx).WithName("evictor")
	ticker := time.NewTicker(c.config.Evictor.ConsolidationInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// Reconcile partially-drained nodes first — auto-uncordon if TTL expired.
			c.reconcilePartialDrains(ctx)

			snapshot := c.state.Snapshot()
			recs, err := c.Analyze(ctx, snapshot)
			if err != nil {
				logger.Error(err, "Analysis failed")
				continue
			}
			// Enforce MaxConcurrentEvictions: only execute up to the
			// configured limit per tick to prevent cascading failures.
			maxExec := c.config.Evictor.MaxConcurrentEvictions
			executed := 0
			for _, rec := range recs {
				if maxExec > 0 && executed >= maxExec {
					logger.V(1).Info("MaxConcurrentEvictions reached, deferring remaining",
						"executed", executed, "remaining", len(recs)-executed)
					break
				}
				if err := c.Execute(ctx, rec); err != nil {
					logger.Error(err, "Execution failed", "recommendation", rec.ID)
				} else {
					executed++
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

// reconcilePartialDrains finds nodes annotated with koptimizer.io/partial-drain-at
// and auto-uncordons them if the TTL has expired. This prevents partially drained
// nodes from staying cordoned indefinitely.
func (c *Controller) reconcilePartialDrains(ctx context.Context) {
	logger := log.FromContext(ctx).WithName("evictor")
	ttl := c.config.Evictor.PartialDrainTTL
	if ttl <= 0 {
		return
	}

	nodes := c.state.GetAllNodes()
	for _, ns := range nodes {
		if ns.Node.Annotations == nil {
			continue
		}
		tsStr, ok := ns.Node.Annotations["koptimizer.io/partial-drain-at"]
		if !ok {
			continue
		}
		ts, err := time.Parse(time.RFC3339, tsStr)
		if err != nil {
			logger.Error(err, "Invalid partial-drain-at timestamp", "node", ns.Node.Name, "value", tsStr)
			continue
		}
		if time.Since(ts) < ttl {
			continue
		}

		// TTL expired — uncordon the node and clear the annotation.
		logger.Info("Auto-uncordoning partially drained node (TTL expired)",
			"node", ns.Node.Name,
			"partialDrainAt", tsStr,
			"ttl", ttl,
		)

		// Fetch fresh copy of the node to avoid stale resource version.
		node := ns.Node.DeepCopy()
		node.Spec.Unschedulable = false
		delete(node.Annotations, "koptimizer.io/partial-drain-at")
		delete(node.Annotations, "koptimizer.io/partial-drain-reason")

		if err := c.client.Update(ctx, node); err != nil {
			logger.Error(err, "Failed to auto-uncordon partially drained node", "node", ns.Node.Name)
		} else {
			c.state.AuditLog.Record("auto-uncordon", ns.Node.Name, "evictor",
				fmt.Sprintf("auto-uncordoned after partial drain TTL (%s)", ttl))
		}
	}
}

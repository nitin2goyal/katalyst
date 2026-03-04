package rebalancer

import (
	"context"
	"fmt"
	"sync"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"github.com/robfig/cron/v3"

	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/internal/state"
	"github.com/koptimizer/koptimizer/pkg/aigate"
	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
	"github.com/koptimizer/koptimizer/pkg/familylock"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// Controller periodically consolidates workloads for optimal packing.
type Controller struct {
	client      client.Client
	provider    cloudprovider.CloudProvider
	state       *state.ClusterState
	guard       *familylock.FamilyLockGuard
	gate        *aigate.AIGate
	config      *config.Config
	planner     *Planner
	executor    *Executor
	scheduler   *Scheduler
	busyRedist  *BusyRedistributor
	reconcileMu sync.Mutex // Prevents concurrent reconciliation
}

func NewController(mgr ctrl.Manager, provider cloudprovider.CloudProvider, st *state.ClusterState, guard *familylock.FamilyLockGuard, gate *aigate.AIGate, cfg *config.Config) *Controller {
	c := mgr.GetClient()
	targetPct := cfg.Rebalancer.TargetUtilizationPct
	if targetPct <= 0 {
		targetPct = 95.0
	}
	maxEvac := cfg.Rebalancer.MaxEvacuatePerCycle
	if maxEvac <= 0 {
		maxEvac = 2
	}
	return &Controller{
		client:     c,
		provider:   provider,
		state:      st,
		guard:      guard,
		gate:       gate,
		config:     cfg,
		planner:    NewPlannerWithConfig(targetPct, maxEvac),
		executor:   NewExecutor(c, cfg),
		scheduler:  NewScheduler(cfg),
		busyRedist: NewBusyRedistributor(c, cfg),
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

func (c *Controller) Name() string { return "rebalancer" }

func (c *Controller) Analyze(ctx context.Context, snapshot *optimizer.ClusterSnapshot) ([]optimizer.Recommendation, error) {
	var recs []optimizer.Recommendation

	// Consolidation: pack pods onto fewer nodes at high utilization.
	planRecs, err := c.planner.ComputePlan(snapshot)
	if err != nil {
		return nil, err
	}
	recs = append(recs, planRecs...)

	// Check for busy redistribution needs
	if c.config.Rebalancer.BusyRedistribution.Enabled {
		busyRecs, err := c.busyRedist.Analyze(ctx, snapshot)
		if err != nil {
			return nil, err
		}
		recs = append(recs, busyRecs...)
	}

	return recs, nil
}

func (c *Controller) Execute(ctx context.Context, rec optimizer.Recommendation) error {
	c.reconcileMu.Lock()
	defer c.reconcileMu.Unlock()

	logger := log.FromContext(ctx).WithName("rebalancer")
	if c.config.Mode != "active" || c.config.Rebalancer.DryRun {
		if c.config.Rebalancer.DryRun {
			logger.Info("Dry-run: would execute rebalance",
				"node", rec.Details["nodeName"],
				"summary", rec.Summary,
			)
			c.state.AuditLog.Record("dry-run-rebalance", rec.Details["nodeName"], "rebalancer", rec.Summary)
		}
		return nil
	}
	if !rec.AutoExecutable {
		return nil
	}

	// AI Gate validation for risky changes — fail-closed: use RequiresValidation
	// which checks both the flag AND actual impact metrics, so high-impact
	// recommendations cannot bypass the gate by omitting the flag.
	if c.gate.RequiresValidation(rec) {
		valReq := aigate.ValidationRequest{
			Action:         rec.Summary,
			Recommendation: rec,
		}
		result, err := c.gate.Validate(ctx, valReq)
		if err != nil || !result.Approved {
			return nil
		}
	}

	nodeName := rec.Details["nodeName"]
	if nodeName != "" {
		if err := c.state.NodeLock.TryLock(nodeName, "rebalancer"); err != nil {
			return fmt.Errorf("cannot rebalance node: %w", err)
		}
		defer c.state.NodeLock.Unlock(nodeName, "rebalancer")
	}

	c.state.AuditLog.Record("rebalance", nodeName, "rebalancer", rec.Summary)
	err := c.executor.Execute(ctx, rec)
	if err != nil {
		c.state.AuditLog.Record("rebalance-failed", nodeName, "rebalancer", err.Error())
	} else {
		c.state.AuditLog.Record("rebalance-complete", nodeName, "rebalancer", rec.ID)
	}
	return err
}

func (c *Controller) run(ctx context.Context) {
	logger := log.FromContext(ctx).WithName("rebalancer")

	// Scheduled rebalancing (legacy cron mode, still supported)
	var cronScheduler *cron.Cron
	if c.config.Rebalancer.Schedule != "" {
		cronScheduler = cron.New()
		cronScheduler.AddFunc(c.config.Rebalancer.Schedule, func() {
			logger.Info("Running scheduled rebalance")
			c.runConsolidationCycle(ctx)
		})
		cronScheduler.Start()
	}
	defer func() {
		if cronScheduler != nil {
			stopCtx := cronScheduler.Stop()
			<-stopCtx.Done()
		}
	}()

	// Continuous consolidation loop: runs every ConsolidationInterval to
	// pack pods tightly and free nodes for removal.
	interval := c.config.Rebalancer.ConsolidationInterval
	if interval <= 0 {
		interval = 60 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if c.state.Breaker.IsTripped(c.Name()) {
				logger.V(1).Info("Circuit breaker tripped, skipping consolidation cycle")
				continue
			}
			c.runConsolidationCycle(ctx)

			// Also run busy redistribution if enabled
			if c.config.Rebalancer.BusyRedistribution.Enabled {
				snapshot := c.state.Snapshot()
				recs, err := c.busyRedist.Analyze(ctx, snapshot)
				if err != nil {
					logger.Error(err, "Busy redistribution analysis failed")
					c.state.Breaker.RecordFailure(c.Name())
					continue
				}
				for _, rec := range recs {
					if err := c.Execute(ctx, rec); err != nil {
						logger.Error(err, "Busy redistribution failed", "recommendation", rec.ID)
						c.state.Breaker.RecordFailure(c.Name())
					} else {
						c.state.Breaker.RecordSuccess(c.Name())
					}
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

func (c *Controller) runConsolidationCycle(ctx context.Context) {
	logger := log.FromContext(ctx).WithName("rebalancer")
	snapshot := c.state.Snapshot()
	recs, err := c.planner.ComputePlan(snapshot)
	if err != nil {
		logger.Error(err, "Consolidation analysis failed")
		c.state.Breaker.RecordFailure(c.Name())
		return
	}
	for _, rec := range recs {
		if err := c.Execute(ctx, rec); err != nil {
			logger.Error(err, "Consolidation failed", "recommendation", rec.ID)
			c.state.Breaker.RecordFailure(c.Name())
		} else {
			c.state.Breaker.RecordSuccess(c.Name())
		}
	}
}

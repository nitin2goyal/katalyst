package costmonitor

import (
	"context"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/koptimizer/koptimizer/internal/config"
	intmetrics "github.com/koptimizer/koptimizer/internal/metrics"
	"github.com/koptimizer/koptimizer/internal/state"
	"github.com/koptimizer/koptimizer/internal/store"
	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
	"github.com/koptimizer/koptimizer/pkg/cost"
)

// Controller monitors cluster costs and generates CostReport CRDs.
type Controller struct {
	client    client.Client
	provider  cloudprovider.CloudProvider
	state     *state.ClusterState
	config    *config.Config
	allocator *Allocator
	reporter  *Reporter
	costStore *store.CostStore
}

func NewController(mgr ctrl.Manager, provider cloudprovider.CloudProvider, state *state.ClusterState, cfg *config.Config, costStore *store.CostStore) *Controller {
	return &Controller{
		client:    mgr.GetClient(),
		provider:  provider,
		state:     state,
		config:    cfg,
		allocator: NewAllocator(provider),
		reporter:  NewReporter(mgr.GetClient(), cfg.ClusterName),
		costStore: costStore,
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

func (c *Controller) run(ctx context.Context) {
	logger := log.FromContext(ctx).WithName("costmonitor")
	ticker := time.NewTicker(c.config.CostMonitor.UpdateInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := c.reconcile(ctx); err != nil {
				logger.Error(err, "Cost monitoring reconciliation failed")
			}
		case <-ctx.Done():
			return
		}
	}
}

func (c *Controller) reconcile(ctx context.Context) error {
	logger := log.FromContext(ctx).WithName("costmonitor")
	logger.V(1).Info("Starting cost reconciliation")

	// Get cluster snapshot
	snapshot := c.state.Snapshot()

	// Calculate costs
	costByNamespace, err := c.allocator.AllocateByNamespace(ctx, snapshot)
	if err != nil {
		return err
	}

	costByNodeGroup, err := c.allocator.AllocateByNodeGroup(ctx, snapshot)
	if err != nil {
		return err
	}

	topWorkloads, err := c.allocator.TopWorkloads(ctx, snapshot, 20)
	if err != nil {
		return err
	}

	totalMonthlyCost := 0.0
	for _, n := range snapshot.Nodes {
		totalMonthlyCost += n.HourlyCostUSD * cost.HoursPerMonth
	}

	// Update Prometheus metrics
	intmetrics.ClusterNodeCount.Set(float64(len(snapshot.Nodes)))
	intmetrics.ClusterMonthlyCostUSD.Set(totalMonthlyCost)

	for _, ng := range c.state.GetNodeGroups().GetAll() {
		intmetrics.NodeGroupDesiredCount.WithLabelValues(ng.Name, ng.InstanceType, ng.InstanceFamily).
			Set(float64(ng.DesiredCount))
		intmetrics.NodeGroupCPUUtilization.WithLabelValues(ng.Name).
			Set(ng.CPUUtilization())
		intmetrics.NodeGroupMemoryUtilization.WithLabelValues(ng.Name).
			Set(ng.MemoryUtilization())
	}

	// Persist daily cost snapshot to SQLite (nil-safe inside CostStore)
	c.costStore.RecordDailySnapshot(totalMonthlyCost, costByNamespace, costByNodeGroup)

	// Update CostReport CRD
	return c.reporter.UpdateCostReport(ctx, totalMonthlyCost, costByNamespace, costByNodeGroup, topWorkloads)
}

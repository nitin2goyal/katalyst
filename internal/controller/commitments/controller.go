package commitments

import (
	"context"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
)

// Controller tracks RI/SP/CUD commitments and their utilization.
type Controller struct {
	client   client.Client
	provider cloudprovider.CloudProvider
	config   *config.Config
	importer *Importer
	tracker  *UtilizationTracker
	reporter *Reporter
}

func NewController(mgr ctrl.Manager, provider cloudprovider.CloudProvider, cfg *config.Config) *Controller {
	c := mgr.GetClient()
	return &Controller{
		client:   c,
		provider: provider,
		config:   cfg,
		importer: NewImporter(provider),
		tracker:  NewUtilizationTracker(provider),
		reporter: NewReporter(c, cfg.ClusterName),
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
	logger := log.FromContext(ctx).WithName("commitments")
	ticker := time.NewTicker(c.config.Commitments.UpdateInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := c.reconcile(ctx); err != nil {
				logger.Error(err, "Commitment reconciliation failed")
			}
		case <-ctx.Done():
			return
		}
	}
}

func (c *Controller) reconcile(ctx context.Context) error {
	logger := log.FromContext(ctx).WithName("commitments")
	logger.V(1).Info("Starting commitment reconciliation")

	// Import all commitments
	commitments, err := c.importer.ImportAll(ctx)
	if err != nil {
		return err
	}

	// Track utilization
	if err := c.tracker.UpdateUtilization(ctx, commitments); err != nil {
		logger.Error(err, "Failed to update commitment utilization")
	}

	// Update report
	return c.reporter.UpdateCommitmentReport(ctx, commitments, c.config.Commitments.ExpiryWarningDays)
}

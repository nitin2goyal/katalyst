package workloadscaler

import (
	"context"
	"time"

	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/internal/state"
	"github.com/koptimizer/koptimizer/pkg/aigate"
	"github.com/koptimizer/koptimizer/pkg/familylock"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// Controller provides unified HPA+VPA autoscaling that resolves conflicts.
type Controller struct {
	client      client.Client
	state       *state.ClusterState
	guard       *familylock.FamilyLockGuard
	gate        *aigate.AIGate
	config      *config.Config
	horizontal  *HorizontalScaler
	vertical    *VerticalScaler
	coordinator *Coordinator
	surge       *SurgeDetector
}

func NewController(mgr ctrl.Manager, st *state.ClusterState, guard *familylock.FamilyLockGuard, gate *aigate.AIGate, cfg *config.Config) *Controller {
	c := mgr.GetClient()
	return &Controller{
		client:      c,
		state:       st,
		guard:       guard,
		gate:        gate,
		config:      cfg,
		horizontal:  NewHorizontalScaler(c, cfg),
		vertical:    NewVerticalScaler(c, cfg),
		coordinator: NewCoordinator(cfg),
		surge:       NewSurgeDetector(cfg),
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

func (c *Controller) Name() string { return "workload-scaler" }

func (c *Controller) Analyze(ctx context.Context, snapshot *optimizer.ClusterSnapshot) ([]optimizer.Recommendation, error) {
	var recs []optimizer.Recommendation

	// Check for surges first (needs immediate action)
	if c.config.WorkloadScaler.SurgeDetection {
		surgeRecs, err := c.surge.Detect(ctx, snapshot)
		if err != nil {
			return nil, err
		}
		recs = append(recs, surgeRecs...)
	}

	// Vertical scaling recommendations
	if c.config.WorkloadScaler.VerticalEnabled {
		vRecs, err := c.vertical.Analyze(ctx, snapshot)
		if err != nil {
			return nil, err
		}
		recs = append(recs, vRecs...)
	}

	// Horizontal scaling recommendations
	if c.config.WorkloadScaler.HorizontalEnabled {
		hRecs, err := c.horizontal.Analyze(ctx, snapshot)
		if err != nil {
			return nil, err
		}
		recs = append(recs, hRecs...)
	}

	// Coordinate to prevent conflicts
	recs = c.coordinator.Resolve(recs)

	return recs, nil
}

func (c *Controller) Execute(ctx context.Context, rec optimizer.Recommendation) error {
	logger := log.FromContext(ctx).WithName("workloadscaler")
	if c.config.Mode != "active" {
		return nil
	}
	if !rec.AutoExecutable {
		return nil
	}

	// AI Gate validation — fail-closed for workload scaling changes
	if c.gate.RequiresValidation(rec) {
		valReq := aigate.ValidationRequest{
			Action:         rec.Summary,
			Recommendation: rec,
			RiskFactors:    []string{fmt.Sprintf("Workload scaling: %s", rec.Details["scalingType"])},
		}
		result, err := c.gate.Validate(ctx, valReq)
		if err != nil || !result.Approved {
			logger.Info("AI Gate rejected workload scaling", "recommendation", rec.ID)
			return nil
		}
	}

	switch rec.Details["scalingType"] {
	case "horizontal":
		return c.horizontal.Execute(ctx, rec)
	case "vertical":
		return c.vertical.Execute(ctx, rec)
	}
	return nil
}

func (c *Controller) run(ctx context.Context) {
	logger := log.FromContext(ctx).WithName("workloadscaler")
	ticker := time.NewTicker(30 * time.Second)
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

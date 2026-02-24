package gpu

import (
	"context"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/internal/state"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// Controller handles GPU optimization: detect idle GPUs, manage CPU fallback, and CPU scavenging.
type Controller struct {
	client    client.Client
	state     *state.ClusterState
	config    *config.Config
	detector  *Detector
	fallback  *FallbackManager
	scavenger *Scavenger
}

func NewController(mgr ctrl.Manager, st *state.ClusterState, cfg *config.Config) *Controller {
	c := mgr.GetClient()
	return &Controller{
		client:    c,
		state:     st,
		config:    cfg,
		detector:  NewDetector(cfg),
		fallback:  NewFallbackManager(c, cfg),
		scavenger: NewScavenger(c, cfg),
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

func (c *Controller) Name() string { return "gpu-optimizer" }

func (c *Controller) Analyze(ctx context.Context, snapshot *optimizer.ClusterSnapshot) ([]optimizer.Recommendation, error) {
	var recs []optimizer.Recommendation

	// Detect idle GPU nodes
	idleRecs, err := c.detector.DetectIdle(ctx, snapshot)
	if err != nil {
		return nil, err
	}
	recs = append(recs, idleRecs...)

	// CPU fallback recommendations
	if c.config.GPU.CPUFallbackEnabled {
		fallbackRecs, err := c.fallback.Analyze(ctx, snapshot)
		if err != nil {
			return nil, err
		}
		recs = append(recs, fallbackRecs...)
	}

	// CPU scavenging recommendations (spare CPU on active GPU nodes)
	if c.config.GPU.CPUScavengingEnabled {
		scavRecs, err := c.scavenger.DetectScavengeable(ctx, snapshot)
		if err != nil {
			return nil, err
		}
		recs = append(recs, scavRecs...)
	}

	return recs, nil
}

func (c *Controller) Execute(ctx context.Context, rec optimizer.Recommendation) error {
	if c.config.Mode != "active" {
		return nil
	}
	if !rec.AutoExecutable {
		return nil
	}

	action := rec.Details["action"]
	switch action {
	case "enable-cpu-scavenging", "disable-cpu-scavenging", "update-cpu-scavenging":
		return c.scavenger.Execute(ctx, rec)
	default:
		return c.fallback.Execute(ctx, rec)
	}
}

func (c *Controller) run(ctx context.Context) {
	logger := log.FromContext(ctx).WithName("gpu")
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			snapshot := c.state.Snapshot()
			recs, err := c.Analyze(ctx, snapshot)
			if err != nil {
				logger.Error(err, "GPU analysis failed")
				continue
			}
			for _, rec := range recs {
				if err := c.Execute(ctx, rec); err != nil {
					logger.Error(err, "GPU execution failed", "recommendation", rec.ID)
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

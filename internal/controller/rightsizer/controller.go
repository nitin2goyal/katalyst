package rightsizer

import (
	"context"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/internal/metrics"
	"github.com/koptimizer/koptimizer/internal/state"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// Controller handles pod rightsizing (VPA-like behavior).
type Controller struct {
	client      client.Client
	state       *state.ClusterState
	config      *config.Config
	metricsStore *metrics.Store
	analyzer    *Analyzer
	recommender *Recommender
	actuator    *Actuator
	oomTracker  *OOMTracker
}

func NewController(mgr ctrl.Manager, st *state.ClusterState, cfg *config.Config, metricsStore *metrics.Store) *Controller {
	c := mgr.GetClient()
	return &Controller{
		client:       c,
		state:        st,
		config:       cfg,
		metricsStore: metricsStore,
		analyzer:     NewAnalyzer(cfg, metricsStore),
		recommender:  NewRecommender(cfg),
		actuator:     NewActuator(c, cfg),
		oomTracker:   NewOOMTracker(c, cfg),
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

func (c *Controller) Name() string { return "rightsizer" }

func (c *Controller) Analyze(ctx context.Context, snapshot *optimizer.ClusterSnapshot) ([]optimizer.Recommendation, error) {
	var recs []optimizer.Recommendation

	// Check for OOM events using snapshot pods (avoids redundant API call)
	oomRecs, err := c.oomTracker.Analyze(ctx, snapshot.Pods)
	if err != nil {
		return nil, err
	}
	recs = append(recs, oomRecs...)

	// Analyze pod resource usage patterns
	for _, pod := range snapshot.Pods {
		if c.isExcluded(pod.Pod.Namespace) {
			continue
		}

		analysis := c.analyzer.AnalyzePod(ctx, pod)
		if analysis == nil {
			continue
		}

		podRecs := c.recommender.Recommend(analysis)
		recs = append(recs, podRecs...)
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
	return c.actuator.Apply(ctx, rec)
}

func (c *Controller) isExcluded(namespace string) bool {
	for _, ns := range c.config.Rightsizer.ExcludeNamespaces {
		if ns == namespace {
			return true
		}
	}
	return false
}

func (c *Controller) run(ctx context.Context) {
	logger := log.FromContext(ctx).WithName("rightsizer")
	ticker := time.NewTicker(60 * time.Second)
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

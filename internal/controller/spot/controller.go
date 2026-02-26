package spot

import (
	"context"
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

// Controller manages spot instance optimization: spot/OD mix, interruption
// handling, diversity, and automatic fallback.
type Controller struct {
	client       client.Client
	provider     cloudprovider.CloudProvider
	state        *state.ClusterState
	guard        *familylock.FamilyLockGuard
	gate         *aigate.AIGate
	config       *config.Config
	mixer        *Mixer
	interruption *InterruptionHandler
	diversity    *DiversityManager
}

func NewController(mgr ctrl.Manager, provider cloudprovider.CloudProvider, st *state.ClusterState, guard *familylock.FamilyLockGuard, gate *aigate.AIGate, cfg *config.Config) *Controller {
	c := mgr.GetClient()
	return &Controller{
		client:       c,
		provider:     provider,
		state:        st,
		guard:        guard,
		gate:         gate,
		config:       cfg,
		mixer:        NewMixer(provider, cfg),
		interruption: NewInterruptionHandler(c, provider, cfg),
		diversity:    NewDiversityManager(provider, cfg),
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

func (c *Controller) Name() string { return "spot-optimizer" }

func (c *Controller) Analyze(ctx context.Context, snapshot *optimizer.ClusterSnapshot) ([]optimizer.Recommendation, error) {
	var recs []optimizer.Recommendation

	// Analyze spot/on-demand mix optimization
	mixRecs, err := c.mixer.Analyze(ctx, snapshot)
	if err != nil {
		return nil, err
	}
	recs = append(recs, mixRecs...)

	// Check spot diversity (are we spread across enough instance types?)
	divRecs, err := c.diversity.Analyze(ctx, snapshot)
	if err != nil {
		return nil, err
	}
	recs = append(recs, divRecs...)

	// Check for pending interruptions
	intRecs, err := c.interruption.Analyze(ctx, snapshot)
	if err != nil {
		return nil, err
	}
	recs = append(recs, intRecs...)

	return recs, nil
}

func (c *Controller) Execute(ctx context.Context, rec optimizer.Recommendation) error {
	if c.config.Mode != "active" {
		return nil
	}
	if !rec.AutoExecutable {
		return nil
	}

	// AI Gate validation — fail-closed
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

	action := rec.Details["action"]
	switch action {
	case "convert-to-spot", "convert-to-ondemand", "adjust-spot-mix":
		return c.mixer.Execute(ctx, rec)
	case "drain-spot-interruption":
		return c.interruption.Execute(ctx, rec)
	case "diversify-spot-types":
		return c.diversity.Execute(ctx, rec)
	default:
		return nil
	}
}

func (c *Controller) run(ctx context.Context) {
	logger := log.FromContext(ctx).WithName("spot")
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
				logger.Error(err, "Spot analysis failed")
				c.state.Breaker.RecordFailure(c.Name())
				continue
			}
			for _, rec := range recs {
				if err := c.Execute(ctx, rec); err != nil {
					logger.Error(err, "Spot execution failed", "recommendation", rec.ID)
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

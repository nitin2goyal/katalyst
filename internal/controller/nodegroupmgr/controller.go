package nodegroupmgr

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

// Controller manages node group lifecycle (min/max adjustment, empty detection, deletion).
type Controller struct {
	client       client.Client
	provider     cloudprovider.CloudProvider
	state        *state.ClusterState
	guard        *familylock.FamilyLockGuard
	gate         *aigate.AIGate
	config       *config.Config
	minAdjuster  *MinAdjuster
	emptyChecker *EmptyChecker
	lifecycle    *Lifecycle
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
		minAdjuster:  NewMinAdjuster(provider, guard, gate, cfg),
		emptyChecker: NewEmptyChecker(cfg),
		lifecycle:    NewLifecycle(c, cfg),
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

func (c *Controller) Name() string { return "node-group-manager" }

func (c *Controller) Analyze(ctx context.Context, snapshot *optimizer.ClusterSnapshot) ([]optimizer.Recommendation, error) {
	var recs []optimizer.Recommendation

	nodeGroupState := c.state.GetNodeGroups()

	// Check for min count adjustments
	if c.config.NodeGroupMgr.MinAdjustment.Enabled {
		minRecs, err := c.minAdjuster.Analyze(ctx, nodeGroupState)
		if err != nil {
			return nil, err
		}
		recs = append(recs, minRecs...)
	}

	// Check for empty node groups
	if c.config.NodeGroupMgr.EmptyGroupDetection.Enabled {
		emptyRecs, err := c.emptyChecker.Analyze(ctx, nodeGroupState)
		if err != nil {
			return nil, err
		}
		recs = append(recs, emptyRecs...)
	}

	return recs, nil
}

// Execute runs the recommendation if possible. It returns the recommendation
// back (possibly modified) and a boolean indicating whether it was executed.
// Recommendations rejected by AI Gate or that are non-auto-executable are
// returned so callers can persist them as CRDs.
func (c *Controller) Execute(ctx context.Context, rec optimizer.Recommendation) (optimizer.Recommendation, bool, error) {
	if c.config.Mode != "active" {
		return rec, false, nil
	}
	if !rec.AutoExecutable {
		return rec, false, nil
	}

	// AI Gate validation
	if rec.RequiresAIGate && c.gate != nil {
		valReq := aigate.ValidationRequest{
			Action:         rec.Summary,
			Recommendation: rec,
		}
		result, err := c.gate.Validate(ctx, valReq)
		if err != nil {
			rec.AIGateResult = &optimizer.AIGateResult{
				Approved:  false,
				Reasoning: fmt.Sprintf("AI Gate error: %v", err),
			}
			return rec, false, nil
		}
		if !result.Approved {
			rec.AIGateResult = &optimizer.AIGateResult{
				Approved:   false,
				Confidence: result.Confidence,
				Reasoning:  result.Reasoning,
				Warnings:   result.Warnings,
			}
			return rec, false, nil
		}
	}

	err := c.minAdjuster.Execute(ctx, rec)
	return rec, err == nil, err
}

func (c *Controller) run(ctx context.Context) {
	logger := log.FromContext(ctx).WithName("nodegroupmgr")
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
				updatedRec, executed, err := c.Execute(ctx, rec)
				if err != nil {
					logger.Error(err, "Execution failed", "recommendation", rec.ID)
				}

				// Persist non-executed recommendations as CRDs so they are visible
				// to users. This covers:
				// - non-auto-executable recs (e.g. manual approval needed)
				// - recs rejected by AI Gate
				// - recs that failed execution
				if !executed {
					if crdErr := c.lifecycle.CreateRecommendationCRD(ctx, updatedRec); crdErr != nil {
						logger.Error(crdErr, "Failed to persist recommendation CRD", "recommendation", updatedRec.ID)
					}
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

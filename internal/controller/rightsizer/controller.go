package rightsizer

import (
	"context"
	"fmt"
	"time"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/internal/metrics"
	"github.com/koptimizer/koptimizer/internal/state"
	"github.com/koptimizer/koptimizer/pkg/aigate"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// Controller handles pod rightsizing (VPA-like behavior).
type Controller struct {
	client       client.Client
	state        *state.ClusterState
	gate         *aigate.AIGate
	config       *config.Config
	metricsStore *metrics.Store
	analyzer     *Analyzer
	recommender  *Recommender
	actuator     *Actuator
	oomTracker   *OOMTracker
}

func NewController(mgr ctrl.Manager, st *state.ClusterState, gate *aigate.AIGate, cfg *config.Config, metricsStore *metrics.Store) *Controller {
	c := mgr.GetClient()
	return &Controller{
		client:       c,
		state:        st,
		gate:         gate,
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

	// Build HPA targets set to skip HPA-managed workloads
	hpaTargets := c.buildHPATargets(ctx)

	// Analyze pod resource usage patterns
	for _, pod := range snapshot.Pods {
		if c.isExcluded(pod.Pod.Namespace) {
			continue
		}

		// Skip pods whose owner is managed by an HPA — rightsizing
		// recommendations conflict with HPA's own scaling decisions.
		if c.isHPAManaged(pod, hpaTargets) {
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

// buildHPATargets queries all HPAs and returns a set of "namespace/kind/name" keys
// for workloads managed by HPAs.
func (c *Controller) buildHPATargets(ctx context.Context) map[string]bool {
	targets := make(map[string]bool)
	hpaList := &autoscalingv2.HorizontalPodAutoscalerList{}
	if err := c.client.List(ctx, hpaList); err != nil {
		// If we can't list HPAs, return empty set — we'll still generate recs.
		return targets
	}
	for _, hpa := range hpaList.Items {
		ref := hpa.Spec.ScaleTargetRef
		key := fmt.Sprintf("%s/%s/%s", hpa.Namespace, ref.Kind, ref.Name)
		targets[key] = true
	}
	return targets
}

// isHPAManaged checks if a pod's owner is managed by an HPA.
// HPAs target Deployments/StatefulSets, but pods are owned by ReplicaSets.
// We resolve ReplicaSet → Deployment by checking the RS's OwnerReferences.
func (c *Controller) isHPAManaged(pod optimizer.PodInfo, hpaTargets map[string]bool) bool {
	if pod.OwnerKind == "" || pod.OwnerName == "" {
		return false
	}
	ns := pod.Pod.Namespace
	// Direct match (e.g., StatefulSet pods, or if owner is already the HPA target)
	key := fmt.Sprintf("%s/%s/%s", ns, pod.OwnerKind, pod.OwnerName)
	if hpaTargets[key] {
		return true
	}
	// Resolve ReplicaSet → Deployment: check the pod's owner ReplicaSet for a
	// parent Deployment in its OwnerReferences.
	if pod.OwnerKind == "ReplicaSet" && pod.Pod != nil {
		for _, ref := range pod.Pod.OwnerReferences {
			if ref.Kind == "ReplicaSet" {
				// The ReplicaSet itself may have OwnerReferences pointing to a Deployment,
				// but we don't have that info from the pod alone. Use naming convention:
				// Deployment "myapp" creates ReplicaSet "myapp-<hash>".
				// Find the last dash-separated segment (the hash).
				rsName := ref.Name
				for i := len(rsName) - 1; i >= 0; i-- {
					if rsName[i] == '-' {
						deployName := rsName[:i]
						deployKey := fmt.Sprintf("%s/Deployment/%s", ns, deployName)
						if hpaTargets[deployKey] {
							return true
						}
						break
					}
				}
			}
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

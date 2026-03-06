package rightsizer

import (
	"context"
	"fmt"
	"sync"
	"time"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	corev1 "k8s.io/api/core/v1"

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
	notifier     *Notifier

	mu        sync.Mutex
	downsized map[string]time.Time // tracks workloads already downsized with TTL
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
		notifier:     NewNotifier(cfg, st.AuditLog),
		downsized:    make(map[string]time.Time),
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

	// Build node capacity lookup for ratio-based rightsizing
	nodeCapacity := make(map[string]*optimizer.NodeInfo, len(snapshot.Nodes))
	for i := range snapshot.Nodes {
		nodeCapacity[snapshot.Nodes[i].Node.Name] = &snapshot.Nodes[i]
	}

	// Analyze pod resource usage patterns
	for _, pod := range snapshot.Pods {
		if c.isExcluded(pod.Pod.Namespace) {
			continue
		}

		// Skip non-Running pods — Pending pods report 0 utilization and
		// would generate incorrect downsize recommendations.
		if pod.Pod.Status.Phase != corev1.PodRunning {
			continue
		}

		// Skip pods with unready containers — they may be starting up or
		// crashlooping and don't reflect steady-state usage.
		if !allContainersReady(pod.Pod) {
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

		// Attach node capacity for ratio-based rightsizing
		if node, ok := nodeCapacity[pod.Pod.Spec.NodeName]; ok {
			analysis.NodeCPUCapMilli = node.CPUCapacity
			analysis.NodeMemCapBytes = node.MemoryCapacity
		}

		podRecs := c.recommender.Recommend(analysis)
		recs = append(recs, podRecs...)
	}

	return recs, nil
}

func (c *Controller) Execute(ctx context.Context, rec optimizer.Recommendation) error {
	if c.config.GetMode() != "active" {
		return nil
	}
	if !rec.AutoExecutable {
		return nil
	}

	// Safety actions (OOM memory bumps) always execute immediately.
	if !isDownsizeRec(rec) {
		return c.executeWithGate(ctx, rec)
	}

	// Downsize recommendations require auto-approve to be enabled.
	if !c.config.Rightsizer.AutoApprove {
		return nil
	}

	// Cooldown: skip if this workload was downsized within the last 24h.
	workloadKey := rec.TargetNamespace + "/" + rec.TargetKind + "/" + rec.TargetName
	c.mu.Lock()
	downsizedAt, already := c.downsized[workloadKey]
	c.mu.Unlock()
	if already && time.Since(downsizedAt) < 24*time.Hour {
		return nil
	}

	if err := c.executeWithGate(ctx, rec); err != nil {
		return err
	}

	// Record successful downsize with timestamp for TTL-based expiry.
	c.mu.Lock()
	c.downsized[workloadKey] = time.Now()
	c.mu.Unlock()
	return nil
}

// executeWithGate runs AI Gate validation then applies the recommendation.
func (c *Controller) executeWithGate(ctx context.Context, rec optimizer.Recommendation) error {
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

// isDownsizeRec returns true for proportional CPU+memory downsize recommendations.
// OOM memory bumps are safety actions and return false.
func isDownsizeRec(rec optimizer.Recommendation) bool {
	return rec.Details["resource"] == "cpu+memory"
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

// allContainersReady returns true if every container in the pod has a Ready
// condition set to true. Returns false for pods with no container statuses.
func allContainersReady(pod *corev1.Pod) bool {
	if len(pod.Status.ContainerStatuses) == 0 {
		return false
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if !cs.Ready {
			return false
		}
	}
	return true
}

func (c *Controller) run(ctx context.Context) {
	logger := log.FromContext(ctx).WithName("rightsizer")
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	cleanupTicker := time.NewTicker(1 * time.Hour)
	defer cleanupTicker.Stop()

	for {
		select {
		case <-cleanupTicker.C:
			c.cleanupDownsized()
			c.oomTracker.Cleanup()
			c.notifier.Cleanup()
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
				// Push to notification channels (with cooldown dedup)
				c.notifier.Notify(ctx, rec)

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

// cleanupDownsized removes entries older than 24h to prevent memory leaks.
func (c *Controller) cleanupDownsized() {
	c.mu.Lock()
	defer c.mu.Unlock()
	cutoff := time.Now().Add(-24 * time.Hour)
	for k, t := range c.downsized {
		if t.Before(cutoff) {
			delete(c.downsized, k)
		}
	}
}

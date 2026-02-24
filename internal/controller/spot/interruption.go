package spot

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/koptimizer/koptimizer/internal/config"
	intmetrics "github.com/koptimizer/koptimizer/internal/metrics"
	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// InterruptionHandler monitors spot nodes for interruption signals and
// proactively drains workloads before termination. Supports:
// - AWS: TerminationNotice condition (AWS Node Termination Handler)
// - GCP: PreemptionNotice condition, impending-node-termination taint
// - Azure: scheduled-event annotation, scalesetpriority taint
// - Universal: koptimizer.io/spot-interruption annotation
type InterruptionHandler struct {
	client   client.Client
	provider cloudprovider.CloudProvider
	config   *config.Config
}

func NewInterruptionHandler(c client.Client, provider cloudprovider.CloudProvider, cfg *config.Config) *InterruptionHandler {
	return &InterruptionHandler{client: c, provider: provider, config: cfg}
}

func (h *InterruptionHandler) Analyze(ctx context.Context, snapshot *optimizer.ClusterSnapshot) ([]optimizer.Recommendation, error) {
	if !h.config.Spot.InterruptionHandling {
		return nil, nil
	}

	var recs []optimizer.Recommendation

	for _, node := range snapshot.Nodes {
		if !cloudprovider.IsSpotNode(node.Node) {
			continue
		}

		// Check for interruption signal via multiple cloud-specific methods.
		interrupted := false

		// AWS: TerminationNotice node condition (set by AWS Node Termination Handler).
		for _, cond := range node.Node.Status.Conditions {
			if cond.Type == "TerminationNotice" && cond.Status == corev1.ConditionTrue {
				interrupted = true
				break
			}
		}

		// GCP: Preemption notice via node condition set by GKE metadata agent.
		if !interrupted {
			for _, cond := range node.Node.Status.Conditions {
				if (cond.Type == "PreemptionNotice" || cond.Type == "MaintenanceEvent") && cond.Status == corev1.ConditionTrue {
					interrupted = true
					break
				}
			}
		}

		// Azure: Scheduled events via node condition or taint.
		if !interrupted && node.Node.Annotations != nil {
			if _, ok := node.Node.Annotations["kubernetes.azure.com/scheduled-event"]; ok {
				interrupted = true
			}
		}

		// Universal: koptimizer annotation (set by external pollers or webhooks).
		if !interrupted && node.Node.Annotations != nil {
			if _, ok := node.Node.Annotations["koptimizer.io/spot-interruption"]; ok {
				interrupted = true
			}
		}

		// Check for preemption taints applied by cloud providers.
		if !interrupted {
			for _, taint := range node.Node.Spec.Taints {
				if taint.Key == "cloud.google.com/impending-node-termination" ||
					taint.Key == "kubernetes.azure.com/scalesetpriority" && taint.Effect == corev1.TaintEffectNoSchedule {
					interrupted = true
					break
				}
			}
		}

		if interrupted {
			podCount := len(node.Pods)
			recs = append(recs, optimizer.Recommendation{
				ID:             fmt.Sprintf("spot-drain-%s", node.Node.Name),
				Type:           optimizer.RecommendationSpotOptimize,
				Priority:       optimizer.PriorityCritical,
				AutoExecutable: true,
				TargetKind:     "Node",
				TargetName:     node.Node.Name,
				Summary:        fmt.Sprintf("Spot interruption on %s — drain %d pods", node.Node.Name, podCount),
				ActionSteps: []string{
					fmt.Sprintf("Cordon node %s to prevent new scheduling", node.Node.Name),
					fmt.Sprintf("Evict %d pods to healthy nodes", podCount),
					"Scheduler will reschedule pods on available capacity",
				},
				EstimatedImpact: optimizer.ImpactEstimate{
					NodesAffected: 1,
					PodsAffected:  podCount,
					RiskLevel:     "high",
				},
				Details: map[string]string{
					"action":   "drain-spot-interruption",
					"nodeName": node.Node.Name,
				},
			})
		}
	}

	return recs, nil
}

func (h *InterruptionHandler) Execute(ctx context.Context, rec optimizer.Recommendation) error {
	logger := log.FromContext(ctx).WithName("spot-interruption")
	nodeName := rec.Details["nodeName"]

	intmetrics.SpotInterruptions.Inc()

	// Cordon the node
	node := &corev1.Node{}
	if err := h.client.Get(ctx, client.ObjectKey{Name: nodeName}, node); err != nil {
		return fmt.Errorf("getting node %s: %w", nodeName, err)
	}

	if !node.Spec.Unschedulable {
		node.Spec.Unschedulable = true
		if err := h.client.Update(ctx, node); err != nil {
			return fmt.Errorf("cordoning node %s: %w", nodeName, err)
		}
		logger.Info("Cordoned spot node for interruption drain", "node", nodeName)
	}

	// Evict all non-DaemonSet, non-mirror pods
	podList := &corev1.PodList{}
	if err := h.client.List(ctx, podList, client.MatchingFields{"spec.nodeName": nodeName}); err != nil {
		return fmt.Errorf("listing pods on node %s: %w", nodeName, err)
	}

	// Pre-fetch PDBs per namespace to respect PodDisruptionBudgets during
	// emergency spot drains. We log violations but still proceed because the
	// node is being terminated — better to evict gracefully than let the cloud
	// provider hard-kill the pod.
	pdbByNS := make(map[string]*policyv1.PodDisruptionBudgetList)

	gracePeriod := int64(30)
	if h.config.Spot.DrainGracePeriodSeconds > 0 {
		gracePeriod = int64(h.config.Spot.DrainGracePeriodSeconds)
	}

	evicted := 0
	pdbViolations := 0
	for i := range podList.Items {
		pod := &podList.Items[i]

		// Skip DaemonSet pods, mirror pods, and already-terminating pods
		if isDaemonSetPod(pod) || isMirrorPod(pod) || pod.DeletionTimestamp != nil {
			continue
		}
		// Skip completed pods
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}

		// Check PDBs — warn on violation but proceed (emergency drain).
		if wouldViolatePDB(pod, pdbByNS, h.client, ctx) {
			logger.Info("Spot drain: PDB would be violated, proceeding due to imminent termination",
				"pod", pod.Name, "namespace", pod.Namespace)
			pdbViolations++
		}

		eviction := &policyv1.Eviction{
			ObjectMeta: metav1.ObjectMeta{
				Name:      pod.Name,
				Namespace: pod.Namespace,
			},
			DeleteOptions: &metav1.DeleteOptions{
				GracePeriodSeconds: &gracePeriod,
			},
		}
		if err := h.client.SubResource("eviction").Create(ctx, pod, eviction); err != nil {
			logger.Error(err, "Failed to evict pod", "pod", pod.Name, "namespace", pod.Namespace)
			continue
		}
		evicted++
	}

	logger.Info("Spot interruption drain complete",
		"node", nodeName,
		"podsEvicted", evicted,
		"pdbViolations", pdbViolations,
	)

	return nil
}

// wouldViolatePDB checks if evicting this pod would violate any PDB in its namespace.
func wouldViolatePDB(pod *corev1.Pod, cache map[string]*policyv1.PodDisruptionBudgetList, c client.Client, ctx context.Context) bool {
	ns := pod.Namespace
	if _, ok := cache[ns]; !ok {
		pdbList := &policyv1.PodDisruptionBudgetList{}
		if err := c.List(ctx, pdbList, client.InNamespace(ns)); err != nil {
			return false // if we can't check, assume safe
		}
		cache[ns] = pdbList
	}
	for _, pdb := range cache[ns].Items {
		if pdb.Status.DisruptionsAllowed <= 0 {
			// Check if the PDB selector matches this pod's labels
			selector, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
			if err != nil {
				continue
			}
			if selector.Matches(labelSetFromMap(pod.Labels)) {
				return true
			}
		}
	}
	return false
}

// labelSetFromMap converts a map to a labels.Set for selector matching.
func labelSetFromMap(m map[string]string) labelSet {
	return labelSet(m)
}

type labelSet map[string]string

func (ls labelSet) Has(key string) bool {
	_, ok := ls[key]
	return ok
}

func (ls labelSet) Get(key string) string {
	return ls[key]
}

func isDaemonSetPod(pod *corev1.Pod) bool {
	for _, ref := range pod.OwnerReferences {
		if ref.Kind == "DaemonSet" {
			return true
		}
	}
	return false
}

func isMirrorPod(pod *corev1.Pod) bool {
	_, ok := pod.Annotations["kubernetes.io/config.mirror"]
	return ok
}

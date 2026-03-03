package gpu

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/internal/state"
	intmetrics "github.com/koptimizer/koptimizer/internal/metrics"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// Reclaimer evicts non-GPU pods from GPU nodes that have lost all GPU workloads.
// When GPU pods scale down, non-GPU pods (placed via scavenger auto-overflow) remain
// stranded on expensive GPU hardware. The reclaimer detects this and evicts them back
// to non-GPU nodes or to other GPU-active nodes.
type Reclaimer struct {
	client    client.Client
	config    *config.Config
	nodeLock  *state.NodeLock
	auditLog  *state.AuditLog
	idleSince map[string]time.Time // nodeName → when it became GPU-idle with non-GPU pods
}

// NewReclaimer creates a new Reclaimer.
func NewReclaimer(c client.Client, cfg *config.Config, nodeLock *state.NodeLock, auditLog *state.AuditLog) *Reclaimer {
	return &Reclaimer{
		client:    c,
		config:    cfg,
		nodeLock:  nodeLock,
		auditLog:  auditLog,
		idleSince: make(map[string]time.Time),
	}
}

// Analyze detects GPU nodes that have zero GPU pods but still have non-GPU pods running.
// After a grace period, it generates recommendations to evict those non-GPU pods.
func (r *Reclaimer) Analyze(ctx context.Context, snapshot *optimizer.ClusterSnapshot) ([]optimizer.Recommendation, error) {
	var recs []optimizer.Recommendation
	gracePeriod := r.config.GPU.ReclaimGracePeriod
	if gracePeriod <= 0 {
		gracePeriod = 5 * time.Minute
	}

	// Track which GPU nodes are still in scope this cycle
	activeGPUNodes := make(map[string]bool)

	for i := range snapshot.Nodes {
		node := &snapshot.Nodes[i]
		if !node.IsGPUNode {
			continue
		}
		activeGPUNodes[node.Node.Name] = true

		// Check if node has any GPU pods
		hasGPUPods := nodeHasGPUPods(node)

		if hasGPUPods {
			// GPU demand present — reset timer
			delete(r.idleSince, node.Node.Name)
			continue
		}

		// Node is GPU-idle. Count non-GPU pods (skip system/DaemonSet/completed).
		nonGPUPodCount := 0
		var nonGPUPodCPUMillis int64
		for _, pod := range node.Pods {
			if shouldSkipEviction(pod) {
				continue
			}
			if isGPUPod(pod) {
				continue
			}
			nonGPUPodCount++
			for _, c := range pod.Spec.Containers {
				if cpu, ok := c.Resources.Requests[corev1.ResourceCPU]; ok {
					nonGPUPodCPUMillis += cpu.MilliValue()
				}
			}
		}

		if nonGPUPodCount == 0 {
			// No non-GPU pods to evict — nothing to do
			delete(r.idleSince, node.Node.Name)
			continue
		}

		// Track when this node first became GPU-idle with non-GPU pods
		if _, tracked := r.idleSince[node.Node.Name]; !tracked {
			r.idleSince[node.Node.Name] = time.Now()
			continue
		}

		// Check if grace period has elapsed
		idleDuration := time.Since(r.idleSince[node.Node.Name])
		if idleDuration < gracePeriod {
			continue
		}

		// Grace period elapsed — build recommendation
		rec := optimizer.Recommendation{
			ID:             fmt.Sprintf("gpu-reclaim-%s", node.Node.Name),
			Type:           optimizer.RecommendationGPUOptimize,
			Priority:       optimizer.PriorityHigh,
			AutoExecutable: true,
			TargetKind:     "Node",
			TargetName:     node.Node.Name,
			Summary: fmt.Sprintf("Reclaim GPU node %s: %d non-GPU pods stranded (GPU-idle for %s)",
				node.Node.Name, nonGPUPodCount, idleDuration.Round(time.Second)),
			ActionSteps: []string{
				fmt.Sprintf("Evict %d non-GPU pods from GPU node %s", nonGPUPodCount, node.Node.Name),
				fmt.Sprintf("Restore %s:NoSchedule taint", GPUFallbackTaint),
				fmt.Sprintf("Remove scavenger labels and annotations"),
			},
			Details: map[string]string{
				"nodeName":        node.Node.Name,
				"action":          "reclaim-gpu-node",
				"nonGPUPodCount":  fmt.Sprintf("%d", nonGPUPodCount),
				"idleDurationSec": fmt.Sprintf("%d", int(idleDuration.Seconds())),
			},
		}

		// Check cluster capacity to absorb displaced pods
		availableCPU := int64(0)
		for j := range snapshot.Nodes {
			other := &snapshot.Nodes[j]
			if other.Node.Name == node.Node.Name {
				continue
			}
			// Count capacity on non-GPU nodes and active-GPU-scavenger nodes
			if !other.IsGPUNode {
				availableCPU += other.CPUCapacity - other.CPURequested
			} else if nodeHasGPUPods(other) {
				// Active GPU node with scavenging — its spare CPU is available
				if other.Node.Labels != nil {
					if _, isScavenger := other.Node.Labels[GPUScavengerLabel]; isScavenger {
						availableCPU += other.CPUCapacity - other.CPURequested
					}
				}
			}
		}

		if availableCPU < nonGPUPodCPUMillis {
			rec.AutoExecutable = false
			rec.Summary = fmt.Sprintf("Reclaim GPU node %s: %d non-GPU pods stranded but insufficient capacity (%dm needed, %dm available)",
				node.Node.Name, nonGPUPodCount, nonGPUPodCPUMillis, availableCPU)
		}

		recs = append(recs, rec)
	}

	// Clean up idleSince entries for nodes that no longer exist in snapshot
	for nodeName := range r.idleSince {
		if !activeGPUNodes[nodeName] {
			delete(r.idleSince, nodeName)
		}
	}

	return recs, nil
}

// Execute evicts non-GPU pods from a GPU-idle node and restores the GPU taint.
func (r *Reclaimer) Execute(ctx context.Context, rec optimizer.Recommendation) error {
	logger := log.FromContext(ctx).WithName("gpu-reclaimer")
	nodeName := rec.Details["nodeName"]

	// Acquire node lock
	if err := r.nodeLock.TryLock(nodeName, "gpu-reclaimer"); err != nil {
		return fmt.Errorf("cannot reclaim node: %w", err)
	}
	defer r.nodeLock.Unlock(nodeName, "gpu-reclaimer")

	r.auditLog.Record("reclaim-gpu-node", nodeName, "gpu-reclaimer", rec.Summary)

	// List pods on the node
	podList := &corev1.PodList{}
	if err := r.client.List(ctx, podList, client.MatchingFields{"spec.nodeName": nodeName}); err != nil {
		return fmt.Errorf("listing pods on node %s: %w", nodeName, err)
	}

	// Pre-fetch PDBs per namespace
	pdbByNamespace := make(map[string]*policyv1.PodDisruptionBudgetList)
	for i := range podList.Items {
		ns := podList.Items[i].Namespace
		if _, ok := pdbByNamespace[ns]; !ok {
			pdbList := &policyv1.PodDisruptionBudgetList{}
			if err := r.client.List(ctx, pdbList, client.InNamespace(ns)); err != nil {
				logger.Error(err, "Failed to list PDBs, treating all pods in namespace as protected", "namespace", ns)
				pdbByNamespace[ns] = nil
			} else {
				pdbByNamespace[ns] = pdbList
			}
		}
	}

	// Evict non-GPU pods
	var evicted, skipped int
	for i := range podList.Items {
		pod := &podList.Items[i]

		if isGPUPod(pod) {
			continue
		}
		if shouldSkipEviction(pod) {
			continue
		}

		if !checkPDBSafe(pod, pdbByNamespace[pod.Namespace]) {
			logger.Info("Skipping pod protected by PDB", "pod", pod.Name, "namespace", pod.Namespace)
			skipped++
			continue
		}

		if err := evictPod(ctx, r.client, pod); err != nil {
			logger.Error(err, "Failed to evict pod", "pod", pod.Name, "namespace", pod.Namespace)
			skipped++
		} else {
			logger.V(1).Info("Evicted non-GPU pod from GPU node", "pod", pod.Name, "namespace", pod.Namespace, "node", nodeName)
			intmetrics.EvictionsTotal.Inc()
			evicted++
		}
	}

	// Restore taint and clean up annotations/labels
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		fresh := &corev1.Node{}
		if err := r.client.Get(ctx, types.NamespacedName{Name: nodeName}, fresh); err != nil {
			return err
		}

		// Restore NoSchedule taint
		hasTaint := false
		for _, t := range fresh.Spec.Taints {
			if t.Key == GPUFallbackTaint && t.Effect == corev1.TaintEffectNoSchedule {
				hasTaint = true
				break
			}
		}
		if !hasTaint {
			fresh.Spec.Taints = append(fresh.Spec.Taints, corev1.Taint{
				Key: GPUFallbackTaint, Value: "present", Effect: corev1.TaintEffectNoSchedule,
			})
		}

		// Remove scavenger/fallback annotations and labels
		delete(fresh.Annotations, GPUFallbackAnnotation)
		delete(fresh.Annotations, GPUScavengerAnnotation)
		delete(fresh.Annotations, GPUScavengerHeadroom)
		delete(fresh.Labels, GPUScavengerLabel)

		// Mark as reclaimed
		if fresh.Annotations == nil {
			fresh.Annotations = make(map[string]string)
		}
		fresh.Annotations[GPUReclaimAnnotation] = time.Now().UTC().Format(time.RFC3339)

		return r.client.Update(ctx, fresh)
	})
	if err != nil {
		return fmt.Errorf("restoring taint on node %s: %w", nodeName, err)
	}

	// Reset idle tracking
	delete(r.idleSince, nodeName)

	logger.Info("Reclaimed GPU node",
		"node", nodeName,
		"evicted", evicted,
		"skipped", skipped,
	)
	r.auditLog.Record("reclaim-gpu-node-complete", nodeName, "gpu-reclaimer",
		fmt.Sprintf("evicted %d pods, skipped %d", evicted, skipped))

	return nil
}

// nodeHasGPUPods returns true if any pod on the node requests nvidia.com/gpu > 0.
func nodeHasGPUPods(node *optimizer.NodeInfo) bool {
	if node.GPUsUsed > 0 {
		return true
	}
	for _, pod := range node.Pods {
		if isGPUPod(pod) {
			return true
		}
	}
	return false
}

// isGPUPod returns true if any container in the pod requests nvidia.com/gpu > 0.
func isGPUPod(pod *corev1.Pod) bool {
	for _, c := range pod.Spec.Containers {
		if gpuQty, ok := c.Resources.Requests[GPUResourceName]; ok && !gpuQty.IsZero() {
			return true
		}
	}
	for _, c := range pod.Spec.InitContainers {
		if gpuQty, ok := c.Resources.Requests[GPUResourceName]; ok && !gpuQty.IsZero() {
			return true
		}
	}
	return false
}

// shouldSkipEviction mirrors the evictor's shouldSkipPod logic.
// Pods that should not be evicted: DaemonSets, system-critical, mirror pods, completed, excluded.
func shouldSkipEviction(pod *corev1.Pod) bool {
	// Skip system-critical namespaces
	if pod.Namespace == "kube-system" || pod.Namespace == "kube-public" || pod.Namespace == "kube-node-lease" {
		return true
	}
	// Skip system-critical priority classes
	if pod.Spec.PriorityClassName == "system-cluster-critical" || pod.Spec.PriorityClassName == "system-node-critical" {
		return true
	}
	// Skip DaemonSet pods
	for _, ref := range pod.OwnerReferences {
		if ref.Kind == "DaemonSet" {
			return true
		}
	}
	// Skip mirror pods (static pods)
	if _, ok := pod.Annotations["kubernetes.io/config.mirror"]; ok {
		return true
	}
	// Skip completed pods
	if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
		return true
	}
	// Skip excluded pods
	if v, ok := pod.Annotations["koptimizer.io/exclude"]; ok && v == "true" {
		return true
	}
	return false
}

// checkPDBSafe returns true if evicting the pod won't violate any PDB.
func checkPDBSafe(pod *corev1.Pod, pdbList *policyv1.PodDisruptionBudgetList) bool {
	if pdbList == nil {
		return false // Fail-safe: if PDBs unavailable, block eviction
	}
	for _, pdb := range pdbList.Items {
		selector, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
		if err != nil {
			continue
		}
		if selector.Matches(labels.Set(pod.Labels)) {
			if pdb.Status.DisruptionsAllowed <= 0 {
				return false
			}
		}
	}
	return true
}

// evictPod evicts a pod using the Kubernetes eviction API.
func evictPod(ctx context.Context, c client.Client, pod *corev1.Pod) error {
	gracePeriod := pod.Spec.TerminationGracePeriodSeconds
	if gracePeriod == nil {
		defaultGrace := int64(30)
		gracePeriod = &defaultGrace
	}
	eviction := &policyv1.Eviction{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pod.Name,
			Namespace: pod.Namespace,
		},
		DeleteOptions: &metav1.DeleteOptions{
			GracePeriodSeconds: gracePeriod,
		},
	}
	return c.SubResource("eviction").Create(ctx, pod, eviction)
}

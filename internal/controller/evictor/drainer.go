package evictor

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/koptimizer/koptimizer/internal/config"
	intmetrics "github.com/koptimizer/koptimizer/internal/metrics"
)

// Drainer safely drains pods from a node, respecting PDBs.
type Drainer struct {
	client     client.Client
	config     *config.Config
	lockRefresh func(nodeName, controller string) // optional callback to refresh node lock
}

func NewDrainer(c client.Client, cfg *config.Config) *Drainer {
	return &Drainer{client: c, config: cfg}
}

// SetLockRefresh sets a callback that will be called periodically during drain
// to refresh the node lock and prevent stale lock expiry.
func (d *Drainer) SetLockRefresh(fn func(nodeName, controller string)) {
	d.lockRefresh = fn
}

// DrainNode cordons and drains a node.
// It uncordons the node if pod listing fails or if all evictions fail.
// A partial drain (some evictions failed) keeps the node cordoned but returns an error.
func (d *Drainer) DrainNode(ctx context.Context, nodeName string) error {
	logger := log.FromContext(ctx).WithName("drainer")

	// Cordon the node
	if err := d.cordonNode(ctx, nodeName); err != nil {
		return fmt.Errorf("cordoning node %s: %w", nodeName, err)
	}
	logger.Info("Cordoned node", "node", nodeName)

	drainTimeout := d.config.Evictor.DrainTimeout
	if drainTimeout <= 0 {
		drainTimeout = 5 * time.Minute
	}
	drainCtx, drainCancel := context.WithTimeout(ctx, drainTimeout)
	defer drainCancel()

	// List pods on the node — uncordon on failure to avoid leaving a node unusable.
	podList := &corev1.PodList{}
	if err := d.client.List(drainCtx, podList, client.MatchingFields{"spec.nodeName": nodeName}); err != nil {
		if uncordErr := d.uncordonNode(ctx, nodeName); uncordErr != nil {
			logger.Error(uncordErr, "Failed to uncordon node after pod listing failure", "node", nodeName)
		}
		return fmt.Errorf("listing pods on node %s: %w", nodeName, err)
	}

	// Pre-fetch PDBs per namespace to avoid N+1 API calls (one per pod).
	pdbByNamespace := make(map[string]*policyv1.PodDisruptionBudgetList)
	for i := range podList.Items {
		ns := podList.Items[i].Namespace
		if _, ok := pdbByNamespace[ns]; !ok {
			pdbList := &policyv1.PodDisruptionBudgetList{}
			if err := d.client.List(drainCtx, pdbList, client.InNamespace(ns)); err != nil {
				logger.Error(err, "Failed to list PDBs, treating all pods in namespace as protected", "namespace", ns)
				// Set nil so checkPDBSafeWithCache returns false (fail-safe)
				pdbByNamespace[ns] = nil
			} else {
				pdbByNamespace[ns] = pdbList
			}
		}
	}

	// Evict pods (skip DaemonSet pods and mirror pods)
	var evicted, failed int
	var evictErrs []error
	for i := range podList.Items {
		pod := &podList.Items[i]

		if shouldSkipPod(pod) {
			continue
		}

		if !d.checkPDBSafeWithCache(pod, pdbByNamespace[pod.Namespace]) {
			logger.Info("Skipping pod protected by PDB", "pod", pod.Name, "namespace", pod.Namespace)
			failed++
			evictErrs = append(evictErrs, fmt.Errorf("pod %s/%s protected by PDB (0 disruptions allowed)", pod.Namespace, pod.Name))
			continue
		}

		if err := d.evictPod(drainCtx, pod); err != nil {
			logger.Error(err, "Failed to evict pod", "pod", pod.Name, "namespace", pod.Namespace)
			failed++
			evictErrs = append(evictErrs, fmt.Errorf("pod %s/%s: %w", pod.Namespace, pod.Name, err))
		} else {
			logger.V(1).Info("Evicted pod", "pod", pod.Name, "namespace", pod.Namespace)
			intmetrics.EvictionsTotal.Inc()
			evicted++
		}

		// Refresh node lock to prevent stale expiry during long drain operations
		if d.lockRefresh != nil {
			d.lockRefresh(nodeName, "evictor")
		}
	}

	// No pods needed eviction — drain is trivially successful.
	if evicted == 0 && failed == 0 {
		intmetrics.NodesConsolidated.Inc()
		logger.Info("Drained node (no evictable pods)", "node", nodeName)
		return nil
	}

	// All evictions failed — uncordon the node so it remains usable.
	if evicted == 0 && failed > 0 {
		if uncordErr := d.uncordonNode(ctx, nodeName); uncordErr != nil {
			logger.Error(uncordErr, "Failed to uncordon node after total eviction failure", "node", nodeName)
		}
		return fmt.Errorf("draining node %s: all %d evictions failed: %w", nodeName, failed, errors.Join(evictErrs...))
	}

	// Partial failure — keep node cordoned (some pods moved) but annotate with
	// a timestamp so the node can be auto-uncordoned if the issue isn't resolved.
	if failed > 0 {
		node := &corev1.Node{}
		if err := d.client.Get(ctx, types.NamespacedName{Name: nodeName}, node); err == nil {
			if node.Annotations == nil {
				node.Annotations = make(map[string]string)
			}
			node.Annotations["koptimizer.io/partial-drain-at"] = time.Now().UTC().Format(time.RFC3339)
			node.Annotations["koptimizer.io/partial-drain-reason"] = fmt.Sprintf("%d/%d evictions failed", failed, evicted+failed)
			if updateErr := d.client.Update(ctx, node); updateErr != nil {
				logger.Error(updateErr, "Failed to annotate partially drained node", "node", nodeName)
			}
		}
		return fmt.Errorf("draining node %s: %d/%d evictions failed: %w", nodeName, failed, evicted+failed, errors.Join(evictErrs...))
	}

	intmetrics.NodesConsolidated.Inc()
	logger.Info("Drained node", "node", nodeName, "evicted", evicted)
	return nil
}

func (d *Drainer) cordonNode(ctx context.Context, nodeName string) error {
	node := &corev1.Node{}
	if err := d.client.Get(ctx, types.NamespacedName{Name: nodeName}, node); err != nil {
		return err
	}

	node.Spec.Unschedulable = true
	return d.client.Update(ctx, node)
}

func (d *Drainer) uncordonNode(ctx context.Context, nodeName string) error {
	node := &corev1.Node{}
	if err := d.client.Get(ctx, types.NamespacedName{Name: nodeName}, node); err != nil {
		return err
	}

	node.Spec.Unschedulable = false
	return d.client.Update(ctx, node)
}

func (d *Drainer) evictPod(ctx context.Context, pod *corev1.Pod) error {
	// Use the pod's termination grace period, defaulting to 30s if unset.
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

	// Use SubResource to create eviction
	return d.client.SubResource("eviction").Create(ctx, pod, eviction)
}

// checkPDBSafe returns true if evicting the pod won't violate any PDB.
func (d *Drainer) checkPDBSafe(ctx context.Context, pod *corev1.Pod) bool {
	pdbList := &policyv1.PodDisruptionBudgetList{}
	if err := d.client.List(ctx, pdbList, client.InNamespace(pod.Namespace)); err != nil {
		return false // Fail-safe: if we can't verify PDB, block eviction
	}
	return d.checkPDBSafeWithCache(pod, pdbList)
}

// checkPDBSafeWithCache checks PDB safety using a pre-fetched PDB list.
func (d *Drainer) checkPDBSafeWithCache(pod *corev1.Pod, pdbList *policyv1.PodDisruptionBudgetList) bool {
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

func shouldSkipPod(pod *corev1.Pod) bool {
	// Skip system-critical namespaces
	if pod.Namespace == "kube-system" || pod.Namespace == "kube-public" || pod.Namespace == "kube-node-lease" {
		return true
	}
	// Skip pods with system-critical priority classes
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
	// Skip pods with koptimizer.io/exclude annotation
	if v, ok := pod.Annotations["koptimizer.io/exclude"]; ok && v == "true" {
		return true
	}
	// Skip pods with local storage (EmptyDir without safe-to-evict, HostPath)
	safeToEvict := pod.Annotations["koptimizer.io/safe-to-evict"] == "true" ||
		pod.Annotations["cluster-autoscaler.kubernetes.io/safe-to-evict"] == "true"
	for _, vol := range pod.Spec.Volumes {
		if vol.EmptyDir != nil && !safeToEvict {
			return true
		}
		if vol.HostPath != nil {
			return true
		}
	}
	return false
}

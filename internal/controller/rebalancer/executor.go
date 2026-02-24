package rebalancer

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
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// Executor performs the actual pod migrations for rebalancing.
type Executor struct {
	client client.Client
	config *config.Config
}

func NewExecutor(c client.Client, cfg *config.Config) *Executor {
	return &Executor{client: c, config: cfg}
}

// Execute carries out a rebalancing recommendation.
func (e *Executor) Execute(ctx context.Context, rec optimizer.Recommendation) error {
	logger := log.FromContext(ctx).WithName("rebalancer-executor")

	nodeName := rec.Details["nodeName"]
	if nodeName == "" {
		logger.V(1).Info("No specific node targeted for rebalance")
		return nil
	}

	// Cordon the node
	node := &corev1.Node{}
	if err := e.client.Get(ctx, types.NamespacedName{Name: nodeName}, node); err != nil {
		return fmt.Errorf("getting node %s: %w", nodeName, err)
	}

	node.Spec.Unschedulable = true
	if err := e.client.Update(ctx, node); err != nil {
		return fmt.Errorf("cordoning node %s: %w", nodeName, err)
	}

	// Evict movable pods and track which pods were evicted
	podList := &corev1.PodList{}
	if err := e.client.List(ctx, podList, client.MatchingFields{"spec.nodeName": nodeName}); err != nil {
		// Uncordon the node since we cordoned it above but cannot proceed.
		logger.Error(err, "Failed to list pods, uncordoning node", "node", nodeName)
		node = &corev1.Node{}
		if getErr := e.client.Get(ctx, types.NamespacedName{Name: nodeName}, node); getErr == nil {
			node.Spec.Unschedulable = false
			if uncordErr := e.client.Update(ctx, node); uncordErr != nil {
				logger.Error(uncordErr, "Failed to uncordon node after pod listing failure", "node", nodeName)
			}
		}
		return fmt.Errorf("listing pods on %s: %w", nodeName, err)
	}

	// Pre-fetch PDBs per namespace to avoid N+1 API calls.
	pdbByNamespace := make(map[string]*policyv1.PodDisruptionBudgetList)
	for i := range podList.Items {
		ns := podList.Items[i].Namespace
		if _, ok := pdbByNamespace[ns]; !ok {
			pdbList := &policyv1.PodDisruptionBudgetList{}
			if err := e.client.List(ctx, pdbList, client.InNamespace(ns)); err != nil {
				logger.Error(err, "Failed to list PDBs, treating all pods in namespace as protected", "namespace", ns)
			}
			pdbByNamespace[ns] = pdbList
		}
	}

	type evictedPod struct {
		name      string
		namespace string
	}
	var evictedPods []evictedPod
	var evicted, failed int
	var evictErrs []error

	for i := range podList.Items {
		pod := &podList.Items[i]
		if canMovePod(pod) {
			// Check PDB safety before evicting
			if !e.checkPDBSafeWithCache(pod, pdbByNamespace[pod.Namespace]) {
				logger.Info("Skipping pod protected by PDB", "pod", pod.Name, "namespace", pod.Namespace)
				failed++
				evictErrs = append(evictErrs, fmt.Errorf("pod %s/%s protected by PDB", pod.Namespace, pod.Name))
				continue
			}
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
			if err := e.client.SubResource("eviction").Create(ctx, pod, eviction); err != nil {
				logger.Error(err, "Failed to evict pod", "pod", pod.Name)
				failed++
				evictErrs = append(evictErrs, fmt.Errorf("evicting %s/%s: %w", pod.Namespace, pod.Name, err))
			} else {
				evicted++
				evictedPods = append(evictedPods, evictedPod{name: pod.Name, namespace: pod.Namespace})
			}
		}
	}

	// If all evictions failed, uncordon immediately and return error
	if failed > 0 && evicted == 0 {
		logger.Info("All evictions failed, uncordoning node immediately", "node", nodeName, "failed", failed)
		node = &corev1.Node{}
		if err := e.client.Get(ctx, types.NamespacedName{Name: nodeName}, node); err != nil {
			return fmt.Errorf("re-fetching node %s before uncordon after failed evictions: %w", nodeName, err)
		}
		node.Spec.Unschedulable = false
		if err := e.client.Update(ctx, node); err != nil {
			return fmt.Errorf("uncordoning node %s after failed evictions: %w", nodeName, err)
		}
		return fmt.Errorf("all pod evictions failed on node %s: %w", nodeName, errors.Join(evictErrs...))
	}
	if failed > 0 {
		logger.Info("Some evictions failed", "node", nodeName, "evicted", evicted, "failed", failed)
	}

	// Wait for evicted pods to be rescheduled before uncordoning.
	// Poll every 5 seconds for up to the configured timeout to verify pods have moved off.
	if len(evictedPods) > 0 {
		logger.Info("Waiting for evicted pods to be rescheduled", "count", len(evictedPods))
		waitTimeout := e.config.Rebalancer.RescheduleTimeout
		if waitTimeout <= 0 {
			waitTimeout = 60 * time.Second
		}
		waitCtx, cancel := context.WithTimeout(ctx, waitTimeout)
		defer cancel()

		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-waitCtx.Done():
				logger.Info("Wait timeout reached, proceeding to uncordon", "node", nodeName)
				goto uncordon
			case <-ticker.C:
				allRescheduled := true
				for _, ep := range evictedPods {
					pod := &corev1.Pod{}
					err := e.client.Get(ctx, types.NamespacedName{Namespace: ep.namespace, Name: ep.name}, pod)
					if err != nil {
						// Pod not found means it was deleted (will be recreated by controller)
						continue
					}
					// If the pod is still on the cordoned node and not terminated, it hasn't moved
					if pod.Spec.NodeName == nodeName && pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
						allRescheduled = false
						break
					}
				}
				if allRescheduled {
					logger.Info("All evicted pods rescheduled", "node", nodeName)
					goto uncordon
				}
			}
		}
	}

uncordon:
	// Re-fetch the node to avoid conflicts from stale resourceVersion
	node = &corev1.Node{}
	if err := e.client.Get(ctx, types.NamespacedName{Name: nodeName}, node); err != nil {
		return fmt.Errorf("re-fetching node %s before uncordon: %w", nodeName, err)
	}
	node.Spec.Unschedulable = false
	if err := e.client.Update(ctx, node); err != nil {
		return fmt.Errorf("uncordoning node %s: %w", nodeName, err)
	}

	return nil
}

// checkPDBSafeWithCache checks PDB safety using a pre-fetched PDB list.
func (e *Executor) checkPDBSafeWithCache(pod *corev1.Pod, pdbList *policyv1.PodDisruptionBudgetList) bool {
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

func canMovePod(pod *corev1.Pod) bool {
	// Skip DaemonSet pods
	for _, ref := range pod.OwnerReferences {
		if ref.Kind == "DaemonSet" {
			return false
		}
	}
	// Skip static pods
	if _, ok := pod.Annotations["kubernetes.io/config.mirror"]; ok {
		return false
	}
	// Skip pods with koptimizer.io/exclude annotation
	if v, ok := pod.Annotations["koptimizer.io/exclude"]; ok && v == "true" {
		return false
	}
	// Skip pods with local storage
	for _, vol := range pod.Spec.Volumes {
		if vol.EmptyDir != nil && pod.Annotations["koptimizer.io/safe-to-evict"] != "true" {
			return false
		}
		if vol.HostPath != nil {
			return false
		}
	}
	return true
}

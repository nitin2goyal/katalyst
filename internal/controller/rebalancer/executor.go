package rebalancer

import (
	"context"
	"errors"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
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

type ownerRef struct {
	namespace string
	kind      string // "ReplicaSet", "Deployment", or "StatefulSet"
	name      string
}

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
				// Set nil so checkPDBSafeWithCache returns false (fail-safe)
				pdbByNamespace[ns] = nil
			} else {
				pdbByNamespace[ns] = pdbList
			}
		}
	}

	ownerSet := make(map[ownerRef]bool)
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
				// Track owners (Deployment/ReplicaSet) for rescheduling verification
				for _, ref := range pod.OwnerReferences {
					if ref.Kind == "ReplicaSet" || ref.Kind == "Deployment" || ref.Kind == "StatefulSet" {
						ownerSet[ownerRef{namespace: pod.Namespace, kind: ref.Kind, name: ref.Name}] = true
					}
				}
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

	// Wait for evicted pods' owners to reach desired ready replicas before uncordoning.
	// This verifies replacement pods are actually running, not just that old pods were deleted.
	if len(ownerSet) > 0 {
		logger.Info("Waiting for owner controllers to reach ready state", "owners", len(ownerSet))
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
				allReady := true
				for owner := range ownerSet {
					ready, err := e.isOwnerReady(ctx, owner)
					if err != nil {
						// Owner not found (e.g. orphan pod) — skip
						continue
					}
					if !ready {
						allReady = false
						break
					}
				}
				if allReady {
					logger.Info("All evicted pod owners report ready replicas", "node", nodeName)
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

// isOwnerReady checks if the owner controller has its desired replicas ready.
func (e *Executor) isOwnerReady(ctx context.Context, owner ownerRef) (bool, error) {
	switch owner.kind {
	case "ReplicaSet":
		rs := &appsv1.ReplicaSet{}
		if err := e.client.Get(ctx, types.NamespacedName{Namespace: owner.namespace, Name: owner.name}, rs); err != nil {
			return false, err
		}
		desired := int32(1)
		if rs.Spec.Replicas != nil {
			desired = *rs.Spec.Replicas
		}
		return rs.Status.ReadyReplicas >= desired, nil
	case "Deployment":
		dep := &appsv1.Deployment{}
		if err := e.client.Get(ctx, types.NamespacedName{Namespace: owner.namespace, Name: owner.name}, dep); err != nil {
			return false, err
		}
		desired := int32(1)
		if dep.Spec.Replicas != nil {
			desired = *dep.Spec.Replicas
		}
		return dep.Status.ReadyReplicas >= desired, nil
	case "StatefulSet":
		ss := &appsv1.StatefulSet{}
		if err := e.client.Get(ctx, types.NamespacedName{Namespace: owner.namespace, Name: owner.name}, ss); err != nil {
			return false, err
		}
		desired := int32(1)
		if ss.Spec.Replicas != nil {
			desired = *ss.Spec.Replicas
		}
		return ss.Status.ReadyReplicas >= desired, nil
	default:
		return true, nil
	}
}

func canMovePod(pod *corev1.Pod) bool {
	// Skip system-critical namespaces
	if pod.Namespace == "kube-system" || pod.Namespace == "kube-public" || pod.Namespace == "kube-node-lease" {
		return false
	}
	// Skip system-critical priority classes
	if pod.Spec.PriorityClassName == "system-cluster-critical" || pod.Spec.PriorityClassName == "system-node-critical" {
		return false
	}
	// Skip completed pods
	if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
		return false
	}
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
	// Skip pods with local storage (unless annotated safe-to-evict by either koptimizer or cluster-autoscaler)
	safeToEvict := pod.Annotations["koptimizer.io/safe-to-evict"] == "true" ||
		pod.Annotations["cluster-autoscaler.kubernetes.io/safe-to-evict"] == "true"
	for _, vol := range pod.Spec.Volumes {
		if vol.EmptyDir != nil && !safeToEvict {
			return false
		}
		if vol.HostPath != nil {
			return false
		}
	}
	return true
}

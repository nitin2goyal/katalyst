package gpu

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/koptimizer/koptimizer/internal/config"
	intmetrics "github.com/koptimizer/koptimizer/internal/metrics"
	"github.com/koptimizer/koptimizer/internal/state"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// Redistributor actively moves CPU pods to/from GPU nodes:
//   - Inbound: evict CPU pods from CPU nodes so they land on scavenging GPU nodes with active GPU workloads
//   - Outbound: evict CPU pods OFF GPU-idle nodes (no GPU pods) before the reclaimer's grace period
type Redistributor struct {
	client   client.Client
	config   *config.Config
	nodeLock *state.NodeLock
	auditLog *state.AuditLog
}

func NewRedistributor(c client.Client, cfg *config.Config, nodeLock *state.NodeLock, auditLog *state.AuditLog) *Redistributor {
	return &Redistributor{
		client:   c,
		config:   cfg,
		nodeLock: nodeLock,
		auditLog: auditLog,
	}
}

const (
	maxRedistributePerCycle = 20
	intuitionNamespace      = "intuition"
)

// Analyze generates recommendations to redistribute CPU pods.
// Phase 1: Evacuate — move CPU pods off GPU-idle nodes.
// Phase 2: Attract — move CPU pods from CPU nodes to scavenging GPU nodes with active GPU workloads.
func (r *Redistributor) Analyze(ctx context.Context, snapshot *optimizer.ClusterSnapshot) ([]optimizer.Recommendation, error) {
	var recs []optimizer.Recommendation

	// Phase 1 — Evacuate: GPU nodes with zero GPU pods but stranded CPU pods
	evacuateRecs := r.analyzeEvacuate(snapshot)
	recs = append(recs, evacuateRecs...)

	// Phase 2 — Attract: move CPU pods from CPU nodes to scavenging GPU nodes
	attractRecs := r.analyzeAttract(snapshot)
	recs = append(recs, attractRecs...)

	return recs, nil
}

// analyzeEvacuate finds GPU nodes that have no GPU pods but have non-system CPU pods.
func (r *Redistributor) analyzeEvacuate(snapshot *optimizer.ClusterSnapshot) []optimizer.Recommendation {
	logger := log.Log.WithName("gpu-redistributor").WithName("evacuate")
	var recs []optimizer.Recommendation
	var gpuIdleNodes int

	for i := range snapshot.Nodes {
		node := &snapshot.Nodes[i]
		if !node.IsGPUNode {
			continue
		}

		// Only target GPU-idle nodes
		if nodeHasGPUPods(node) {
			continue
		}
		gpuIdleNodes++

		// Find non-GPU, non-system pods on this GPU-idle node
		for _, pod := range node.Pods {
			if shouldSkipEviction(pod) {
				continue
			}
			if isGPUPod(pod) {
				continue
			}

			cpuMillis := podCPUMillis(pod)
			rec := optimizer.Recommendation{
				ID:             fmt.Sprintf("gpu-redistribute-evacuate-%s-%s", node.Node.Name, pod.Name),
				Type:           optimizer.RecommendationGPUOptimize,
				Priority:       optimizer.PriorityHigh,
				AutoExecutable: true,
				TargetKind:     "Pod",
				TargetName:     pod.Name,
				TargetNamespace: pod.Namespace,
				Summary: fmt.Sprintf("Evacuate CPU pod %s/%s from GPU-idle node %s (%dm CPU)",
					pod.Namespace, pod.Name, node.Node.Name, cpuMillis),
				Details: map[string]string{
					"action":    "evacuate-from-gpu",
					"podName":   pod.Name,
					"namespace": pod.Namespace,
					"nodeName":  node.Node.Name,
					"cpuMillis": fmt.Sprintf("%d", cpuMillis),
				},
			}
			recs = append(recs, rec)
		}
	}

	logger.Info("Evacuate analysis", "gpuIdleNodes", gpuIdleNodes, "podsToEvacuate", len(recs))
	return recs
}

// gpuNodeHeadroom tracks available CPU and memory on a scavenging GPU node.
type gpuNodeHeadroom struct {
	nodeName string
	cpuFree  int64 // millicores
	memFree  int64 // bytes
}

// analyzeAttract finds CPU pods on CPU nodes that could be moved to scavenging GPU nodes with active GPU workloads.
func (r *Redistributor) analyzeAttract(snapshot *optimizer.ClusterSnapshot) []optimizer.Recommendation {
	logger := log.Log.WithName("gpu-redistributor").WithName("attract")

	// Build per-node headroom for scavenging-labeled GPU nodes
	var gpuNodes []gpuNodeHeadroom
	var totalCPUHeadroom, totalMemHeadroom int64

	for i := range snapshot.Nodes {
		node := &snapshot.Nodes[i]
		if !node.IsGPUNode {
			continue
		}
		if node.Node.Labels == nil {
			continue
		}
		if _, isScavenger := node.Node.Labels[GPUScavengerLabel]; !isScavenger {
			continue
		}
		cpuFree := node.CPUCapacity - node.CPURequested
		memFree := node.MemoryCapacity - node.MemoryRequested
		if cpuFree > 0 && memFree > 0 {
			gpuNodes = append(gpuNodes, gpuNodeHeadroom{
				nodeName: node.Node.Name,
				cpuFree:  cpuFree,
				memFree:  memFree,
			})
			totalCPUHeadroom += cpuFree
			totalMemHeadroom += memFree
		}
	}

	logger.Info("GPU scavenging headroom",
		"scavengingNodes", len(gpuNodes),
		"totalCPUHeadroomMillis", totalCPUHeadroom,
		"totalMemHeadroomMB", totalMemHeadroom/(1024*1024))

	if len(gpuNodes) == 0 {
		logger.Info("No GPU nodes with available headroom, skipping attract")
		return nil
	}

	// Collect candidate CPU pods from non-GPU nodes in the intuition namespace
	type candidate struct {
		pod      *corev1.Pod
		nodeName string
		cpuReq   int64
		memReq   int64
	}
	var candidates []candidate
	var skippedNonIntuition, skippedSystem, skippedGPU, skippedNoCPU int

	for i := range snapshot.Nodes {
		node := &snapshot.Nodes[i]
		if node.IsGPUNode {
			continue
		}

		for _, pod := range node.Pods {
			if pod.Namespace != intuitionNamespace {
				skippedNonIntuition++
				continue
			}
			if shouldSkipEviction(pod) {
				skippedSystem++
				continue
			}
			if isGPUPod(pod) {
				skippedGPU++
				continue
			}
			cpuReq := podCPUMillis(pod)
			if cpuReq <= 0 {
				skippedNoCPU++
				continue
			}
			candidates = append(candidates, candidate{
				pod:      pod,
				nodeName: node.Node.Name,
				cpuReq:   cpuReq,
				memReq:   podMemoryBytes(pod),
			})
		}
	}

	logger.Info("Attract candidates from CPU nodes",
		"eligible", len(candidates),
		"skippedNonIntuition", skippedNonIntuition,
		"skippedSystem", skippedSystem,
		"skippedGPU", skippedGPU,
		"skippedNoCPU", skippedNoCPU)

	if len(candidates) == 0 {
		return nil
	}

	// Sort by CPU request descending — move big pods first for maximum savings
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].cpuReq > candidates[j].cpuReq
	})

	var recs []optimizer.Recommendation
	var skippedNoFit int

	for _, c := range candidates {
		if len(recs) >= maxRedistributePerCycle {
			break
		}

		// Find a GPU node that can fit this pod by both CPU and memory.
		// Pick the node with most CPU free (simulates LeastRequested scoring).
		bestIdx := -1
		for j, gn := range gpuNodes {
			if c.cpuReq <= gn.cpuFree && c.memReq <= gn.memFree {
				if bestIdx == -1 || gn.cpuFree > gpuNodes[bestIdx].cpuFree {
					bestIdx = j
				}
			}
		}
		if bestIdx == -1 {
			skippedNoFit++
			continue
		}

		rec := optimizer.Recommendation{
			ID:             fmt.Sprintf("gpu-redistribute-to-gpu-%s-%s", c.nodeName, c.pod.Name),
			Type:           optimizer.RecommendationGPUOptimize,
			Priority:       optimizer.PriorityMedium,
			AutoExecutable: true,
			TargetKind:     "Pod",
			TargetName:     c.pod.Name,
			TargetNamespace: c.pod.Namespace,
			Summary: fmt.Sprintf("Redistribute CPU pod %s/%s from %s to GPU node %s (%dm CPU, %dMB mem)",
				c.pod.Namespace, c.pod.Name, c.nodeName, gpuNodes[bestIdx].nodeName,
				c.cpuReq, c.memReq/(1024*1024)),
			Details: map[string]string{
				"action":     "redistribute-to-gpu",
				"podName":    c.pod.Name,
				"namespace":  c.pod.Namespace,
				"sourceNode": c.nodeName,
				"targetNode": gpuNodes[bestIdx].nodeName,
				"cpuMillis":  fmt.Sprintf("%d", c.cpuReq),
				"memBytes":   fmt.Sprintf("%d", c.memReq),
			},
		}
		recs = append(recs, rec)

		// Deduct from the selected GPU node
		gpuNodes[bestIdx].cpuFree -= c.cpuReq
		gpuNodes[bestIdx].memFree -= c.memReq
	}

	if skippedNoFit > 0 {
		logger.Info("Some candidates skipped — no GPU node with enough CPU+memory headroom",
			"skippedNoFit", skippedNoFit)
	}
	logger.Info("Attract recommendations generated", "count", len(recs))

	return recs
}

// Execute moves a pod to/from GPU nodes.
//   - Attract (redistribute-to-gpu): direct eviction from CPU node — Deployment controller
//     recreates the pod, scheduler places it on GPU node (untainted, high spare capacity).
//   - Evacuate (evacuate-from-gpu): surge-then-evict for zero downtime on GPU nodes.
func (r *Redistributor) Execute(ctx context.Context, rec optimizer.Recommendation) error {
	logger := log.FromContext(ctx).WithName("gpu-redistributor")
	action := rec.Details["action"]
	podName := rec.Details["podName"]
	namespace := rec.Details["namespace"]
	nodeName := rec.Details["nodeName"]
	if nodeName == "" {
		nodeName = rec.Details["sourceNode"]
	}

	if r.config.GetMode() != "active" {
		return nil
	}

	// Acquire node lock on the source node
	if err := r.nodeLock.TryLock(nodeName, "gpu-redistributor"); err != nil {
		r.auditLog.Record("gpu-redistribute-skipped", nodeName, "gpu-redistributor",
			fmt.Sprintf("node lock unavailable for %s/%s: %v", namespace, podName, err))
		return nil
	}
	defer r.nodeLock.Unlock(nodeName, "gpu-redistributor")

	// Get the pod
	pod := &corev1.Pod{}
	if err := r.client.Get(ctx, client.ObjectKey{Name: podName, Namespace: namespace}, pod); err != nil {
		r.auditLog.Record("gpu-redistribute-skipped", nodeName, "gpu-redistributor",
			fmt.Sprintf("pod %s/%s not found: %v", namespace, podName, err))
		return nil
	}

	// Verify pod is still on the expected node
	if pod.Spec.NodeName != nodeName {
		r.auditLog.Record("gpu-redistribute-skipped", nodeName, "gpu-redistributor",
			fmt.Sprintf("pod %s/%s moved to %s", namespace, podName, pod.Spec.NodeName))
		return nil
	}

	// Check PDB safety
	pdbList := &policyv1.PodDisruptionBudgetList{}
	if err := r.client.List(ctx, pdbList, client.InNamespace(namespace)); err != nil {
		r.auditLog.Record("gpu-redistribute-skipped", nodeName, "gpu-redistributor",
			fmt.Sprintf("PDB check failed for %s/%s: %v", namespace, podName, err))
		return nil
	}
	if !checkPDBSafe(pod, pdbList) {
		r.auditLog.Record("gpu-redistribute-skipped", nodeName, "gpu-redistributor",
			fmt.Sprintf("pod %s/%s protected by PDB", namespace, podName))
		return nil
	}

	// Attract: direct eviction from CPU node (fast, Deployment controller recreates).
	// The scheduler will place the new pod on a GPU node because untainted GPU nodes
	// have high spare capacity (LeastRequestedPriority scoring favors them).
	if action == "redistribute-to-gpu" {
		return r.executeDirectEviction(ctx, logger, pod, nodeName, action)
	}

	// Evacuate: surge-then-evict for zero-downtime migration off GPU nodes.
	return r.executeSurgeThenEvict(ctx, logger, pod, nodeName, action)
}

// executeDirectEviction evicts a pod directly. Used for attract (CPU→GPU) and Job pods.
func (r *Redistributor) executeDirectEviction(ctx context.Context, logger logr.Logger, pod *corev1.Pod, nodeName, action string) error {
	podName := pod.Name
	namespace := pod.Namespace

	if err := evictPod(ctx, r.client, pod); err != nil {
		r.auditLog.Record("gpu-redistribute-skipped", nodeName, "gpu-redistributor",
			fmt.Sprintf("failed to evict %s/%s: %v", namespace, podName, err))
		return nil
	}
	intmetrics.EvictionsTotal.Inc()

	auditAction := "gpu-redistribute-to-gpu"
	if action == "evacuate-from-gpu" {
		auditAction = "gpu-redistribute-evacuate"
	}
	r.auditLog.Record(auditAction, nodeName, "gpu-redistributor",
		fmt.Sprintf("evicted %s/%s from %s", namespace, podName, nodeName))
	logger.Info("Redistributed pod via direct eviction",
		"pod", podName, "namespace", namespace, "node", nodeName, "action", action)
	return nil
}

// executeSurgeThenEvict uses maxUnavailable=0 strategy for safe migration.
func (r *Redistributor) executeSurgeThenEvict(ctx context.Context, logger logr.Logger, pod *corev1.Pod, nodeName, action string) error {
	podName := pod.Name
	namespace := pod.Namespace

	// Find owning Deployment
	deploy, originalReplicas, err := r.findOwningDeployment(ctx, pod)
	if err != nil || deploy == nil {
		// No Deployment owner — check if it's a Job/CronJob pod (safe to evict directly)
		for _, ref := range pod.OwnerReferences {
			if ref.Kind == "Job" {
				return r.executeDirectEviction(ctx, logger, pod, nodeName, action)
			}
		}
		r.auditLog.Record("gpu-redistribute-skipped", nodeName, "gpu-redistributor",
			fmt.Sprintf("pod %s/%s has no Deployment/Job owner, skipping", namespace, podName))
		return nil
	}

	// Surge the Deployment by 1 replica
	surgedReplicas := originalReplicas + 1
	if err := r.scaleDeployment(ctx, deploy, surgedReplicas); err != nil {
		r.auditLog.Record("gpu-redistribute-skipped", nodeName, "gpu-redistributor",
			fmt.Sprintf("failed to surge %s: %v", deploy.Name, err))
		return nil
	}
	logger.Info("Surged Deployment for safe migration",
		"deployment", deploy.Name, "namespace", namespace,
		"from", originalReplicas, "to", surgedReplicas)

	ready, err := r.waitForReady(ctx, deploy, surgedReplicas, 5*time.Minute)
	if !ready || err != nil {
		logger.Error(err, "New replica not ready in time, rolling back surge",
			"deployment", deploy.Name)
		_ = r.scaleDeployment(ctx, deploy, originalReplicas)
		r.auditLog.Record("gpu-redistribute-skipped", nodeName, "gpu-redistributor",
			fmt.Sprintf("surge timeout for %s/%s, rolled back", namespace, deploy.Name))
		return nil
	}

	if err := evictPod(ctx, r.client, pod); err != nil {
		logger.Error(err, "Failed to evict pod after surge, rolling back",
			"pod", podName, "namespace", namespace)
		_ = r.scaleDeployment(ctx, deploy, originalReplicas)
		return err
	}

	if err := r.scaleDeployment(ctx, deploy, originalReplicas); err != nil {
		logger.Error(err, "Failed to scale back after eviction (will self-correct)",
			"deployment", deploy.Name, "target", originalReplicas)
	}

	intmetrics.EvictionsTotal.Inc()

	auditAction := "gpu-redistribute-evacuate"
	r.auditLog.Record(auditAction, nodeName, "gpu-redistributor",
		fmt.Sprintf("safely migrated %s/%s from %s (surged %s %d→%d→%d)",
			namespace, podName, nodeName, deploy.Name, originalReplicas, surgedReplicas, originalReplicas))

	logger.Info("Safely redistributed CPU pod (maxUnavailable=0)",
		"pod", podName, "namespace", namespace, "node", nodeName,
		"action", action, "deployment", deploy.Name)

	return nil
}

// findOwningDeployment traverses pod → ReplicaSet → Deployment ownership chain.
func (r *Redistributor) findOwningDeployment(ctx context.Context, pod *corev1.Pod) (*appsv1.Deployment, int32, error) {
	// Find owning ReplicaSet
	var rsName string
	for _, ref := range pod.OwnerReferences {
		if ref.Kind == "ReplicaSet" {
			rsName = ref.Name
			break
		}
	}
	if rsName == "" {
		return nil, 0, nil
	}

	rs := &appsv1.ReplicaSet{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: rsName, Namespace: pod.Namespace}, rs); err != nil {
		return nil, 0, fmt.Errorf("getting ReplicaSet %s: %w", rsName, err)
	}

	// Find owning Deployment
	var deployName string
	for _, ref := range rs.OwnerReferences {
		if ref.Kind == "Deployment" {
			deployName = ref.Name
			break
		}
	}
	if deployName == "" {
		return nil, 0, nil
	}

	deploy := &appsv1.Deployment{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: deployName, Namespace: pod.Namespace}, deploy); err != nil {
		return nil, 0, fmt.Errorf("getting Deployment %s: %w", deployName, err)
	}

	replicas := int32(1)
	if deploy.Spec.Replicas != nil {
		replicas = *deploy.Spec.Replicas
	}

	return deploy, replicas, nil
}

// scaleDeployment sets the replica count on a Deployment with retry on conflict.
func (r *Redistributor) scaleDeployment(ctx context.Context, deploy *appsv1.Deployment, replicas int32) error {
	for attempt := 0; attempt < 3; attempt++ {
		fresh := &appsv1.Deployment{}
		if err := r.client.Get(ctx, types.NamespacedName{Name: deploy.Name, Namespace: deploy.Namespace}, fresh); err != nil {
			return err
		}
		fresh.Spec.Replicas = &replicas
		err := r.client.Update(ctx, fresh)
		if err == nil {
			return nil
		}
		// Retry on conflict ("the object has been modified"), fail on other errors
		if !isConflictError(err) {
			return err
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("conflict after 3 retries scaling %s to %d replicas", deploy.Name, replicas)
}

// isConflictError checks if the error is an optimistic locking conflict.
func isConflictError(err error) bool {
	if err == nil {
		return false
	}
	if apierrors.IsConflict(err) {
		return true
	}
	return strings.Contains(err.Error(), "the object has been modified")
}

// waitForReady polls until the Deployment has the desired number of ready replicas.
func (r *Redistributor) waitForReady(ctx context.Context, deploy *appsv1.Deployment, desired int32, timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				return false, fmt.Errorf("timeout waiting for %s to reach %d ready replicas", deploy.Name, desired)
			}
			fresh := &appsv1.Deployment{}
			if err := r.client.Get(ctx, types.NamespacedName{Name: deploy.Name, Namespace: deploy.Namespace}, fresh); err != nil {
				continue
			}
			if fresh.Status.ReadyReplicas >= desired {
				return true, nil
			}
		}
	}
}

// podCPUMillis returns total CPU request in millicores for a pod.
func podCPUMillis(pod *corev1.Pod) int64 {
	var total int64
	for _, c := range pod.Spec.Containers {
		if cpu, ok := c.Resources.Requests[corev1.ResourceCPU]; ok {
			total += cpu.MilliValue()
		}
	}
	return total
}

// podMemoryBytes returns total memory request in bytes for a pod.
func podMemoryBytes(pod *corev1.Pod) int64 {
	var total int64
	for _, c := range pod.Spec.Containers {
		if mem, ok := c.Resources.Requests[corev1.ResourceMemory]; ok {
			total += mem.Value()
		}
	}
	return total
}

package gpu

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
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
	maxRedistributePerCycle = 5
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
	var recs []optimizer.Recommendation

	for i := range snapshot.Nodes {
		node := &snapshot.Nodes[i]
		if !node.IsGPUNode {
			continue
		}

		// Only target GPU-idle nodes
		if nodeHasGPUPods(node) {
			continue
		}

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

	return recs
}

// analyzeAttract finds CPU pods on CPU nodes that could be moved to scavenging GPU nodes with active GPU workloads.
func (r *Redistributor) analyzeAttract(snapshot *optimizer.ClusterSnapshot) []optimizer.Recommendation {
	// Compute total available headroom on scavenging GPU nodes that have active GPU pods
	var totalHeadroomMillis int64
	for i := range snapshot.Nodes {
		node := &snapshot.Nodes[i]
		if !node.IsGPUNode || !nodeHasGPUPods(node) {
			continue
		}
		if node.Node.Labels == nil {
			continue
		}
		if _, isScavenger := node.Node.Labels[GPUScavengerLabel]; !isScavenger {
			continue
		}
		headroom := node.CPUCapacity - node.CPURequested
		if headroom > 0 {
			totalHeadroomMillis += headroom
		}
	}

	if totalHeadroomMillis <= 0 {
		return nil
	}

	// Collect candidate CPU pods from non-GPU nodes in the intuition namespace
	type candidate struct {
		pod      *corev1.Pod
		nodeName string
		cpuReq   int64
	}
	var candidates []candidate

	for i := range snapshot.Nodes {
		node := &snapshot.Nodes[i]
		if node.IsGPUNode {
			continue
		}

		for _, pod := range node.Pods {
			if pod.Namespace != intuitionNamespace {
				continue
			}
			if shouldSkipEviction(pod) {
				continue
			}
			if isGPUPod(pod) {
				continue
			}
			cpuReq := podCPUMillis(pod)
			if cpuReq <= 0 {
				continue
			}
			candidates = append(candidates, candidate{
				pod:      pod,
				nodeName: node.Node.Name,
				cpuReq:   cpuReq,
			})
		}
	}

	// Sort by CPU request descending — move big pods first for maximum savings
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].cpuReq > candidates[j].cpuReq
	})

	var recs []optimizer.Recommendation
	remainingHeadroom := totalHeadroomMillis

	for _, c := range candidates {
		if len(recs) >= maxRedistributePerCycle {
			break
		}
		if c.cpuReq > remainingHeadroom {
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
			Summary: fmt.Sprintf("Redistribute CPU pod %s/%s from %s to GPU node (%dm CPU, %dm headroom left)",
				c.pod.Namespace, c.pod.Name, c.nodeName, c.cpuReq, remainingHeadroom),
			Details: map[string]string{
				"action":           "redistribute-to-gpu",
				"podName":          c.pod.Name,
				"namespace":        c.pod.Namespace,
				"sourceNode":       c.nodeName,
				"cpuMillis":        fmt.Sprintf("%d", c.cpuReq),
				"headroomRemaining": fmt.Sprintf("%d", remainingHeadroom-c.cpuReq),
			},
		}
		recs = append(recs, rec)
		remainingHeadroom -= c.cpuReq
	}

	return recs
}

// Execute evicts a pod for redistribution (either evacuate or attract).
func (r *Redistributor) Execute(ctx context.Context, rec optimizer.Recommendation) error {
	logger := log.FromContext(ctx).WithName("gpu-redistributor")
	action := rec.Details["action"]
	podName := rec.Details["podName"]
	namespace := rec.Details["namespace"]
	nodeName := rec.Details["nodeName"]
	if nodeName == "" {
		nodeName = rec.Details["sourceNode"]
	}

	if r.config.Mode != "active" {
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

	// Evict the pod
	if err := evictPod(ctx, r.client, pod); err != nil {
		logger.Error(err, "Failed to evict pod for redistribution",
			"pod", podName, "namespace", namespace, "node", nodeName, "action", action)
		return err
	}

	intmetrics.EvictionsTotal.Inc()

	auditAction := "gpu-redistribute-to-gpu"
	if action == "evacuate-from-gpu" {
		auditAction = "gpu-redistribute-evacuate"
	}
	r.auditLog.Record(auditAction, nodeName, "gpu-redistributor",
		fmt.Sprintf("evicted %s/%s from %s (%s)", namespace, podName, nodeName, action))

	logger.Info("Redistributed CPU pod",
		"pod", podName, "namespace", namespace, "node", nodeName, "action", action)

	return nil
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

package gpu

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/koptimizer/koptimizer/internal/config"
	intmetrics "github.com/koptimizer/koptimizer/internal/metrics"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

const (
	// GPUFallbackAnnotation marks a GPU node as serving CPU workloads.
	GPUFallbackAnnotation = "koptimizer.io/gpu-fallback"
	// GPUFallbackTaint is the taint key for GPU nodes.
	GPUFallbackTaint = "nvidia.com/gpu"
	// GPUFallbackPriority is the PriorityClass name for CPU pods scheduled on GPU nodes.
	GPUFallbackPriority = "koptimizer-gpu-fallback"
	// GPUResourceName is the extended resource name for NVIDIA GPUs.
	GPUResourceName corev1.ResourceName = "nvidia.com/gpu"
	// CPUHeadroomReservePct is the percentage of node CPU reserved for GPU pod data-loading phases.
	// CPU pods get conservative requests so they don't starve GPU workloads during data loading.
	CPUHeadroomReservePct = 30

	// Scavenging constants (GPU active, spare CPU available for low-priority pods)

	// GPUScavengerLabel is the node label for nodeAffinity matching by scavenger pods.
	GPUScavengerLabel = "koptimizer.io/cpu-scavengeable"
	// GPUScavengerAnnotation marks a node as having active CPU scavenging.
	GPUScavengerAnnotation = "koptimizer.io/cpu-scavenger"
	// GPUScavengerHeadroom is the annotation key for available CPU in millicores.
	GPUScavengerHeadroom = "koptimizer.io/scavenger-cpu-millis"
	// GPUScavengerPriority is the PriorityClass name for scavenger pods.
	GPUScavengerPriority = "koptimizer-scavenger"
)

// FallbackManager manages GPU-to-CPU fallback scheduling.
// When GPU nodes are idle, it removes the GPU taint so CPU workloads can use them.
// When GPU demand returns, it re-taints and evicts low-priority CPU pods.
type FallbackManager struct {
	client client.Client
	config *config.Config
}

func NewFallbackManager(c client.Client, cfg *config.Config) *FallbackManager {
	return &FallbackManager{client: c, config: cfg}
}

// Analyze checks GPU nodes and generates recommendations for fallback management.
func (f *FallbackManager) Analyze(ctx context.Context, snapshot *optimizer.ClusterSnapshot) ([]optimizer.Recommendation, error) {
	var recs []optimizer.Recommendation
	cpuFallbackCount := 0

	for _, node := range snapshot.Nodes {
		if !node.IsGPUNode {
			continue
		}

		isFallback := false
		if node.Node.Annotations != nil {
			_, isFallback = node.Node.Annotations[GPUFallbackAnnotation]
		}

		if isFallback {
			cpuFallbackCount++

			// Validate CPU pods on this node don't request nvidia.com/gpu (even 0).
			// Some schedulers misbehave when a pod has requests: nvidia.com/gpu: 0.
			for _, pod := range node.Pods {
				if violatesGPURequestPolicy(pod) {
					recs = append(recs, optimizer.Recommendation{
						ID:             fmt.Sprintf("gpu-request-violation-%s-%s", node.Node.Name, pod.Name),
						Type:           optimizer.RecommendationGPUOptimize,
						Priority:       optimizer.PriorityHigh,
						AutoExecutable: false,
						TargetKind:     "Pod",
						TargetName:     pod.Name,
						TargetNamespace: pod.Namespace,
						Summary:        fmt.Sprintf("CPU pod %s/%s on GPU fallback node requests nvidia.com/gpu — remove this field", pod.Namespace, pod.Name),
						ActionSteps: []string{
							fmt.Sprintf("Remove nvidia.com/gpu from resources.requests and resources.limits in pod %s/%s", pod.Namespace, pod.Name),
							"Even nvidia.com/gpu: 0 can cause scheduler issues on some clusters",
						},
						Details: map[string]string{
							"nodeName": node.Node.Name,
							"podName":  pod.Name,
							"action":   "fix-gpu-request",
						},
					})
				}
			}

			// Check if GPU demand has returned
			hasGPUPods := false
			for _, pod := range node.Pods {
				for _, c := range pod.Spec.Containers {
					if gpuQty, ok := c.Resources.Requests[GPUResourceName]; ok && !gpuQty.IsZero() {
						hasGPUPods = true
						break
					}
				}
				if hasGPUPods {
					break
				}
			}

			if node.GPUsUsed > 0 || hasGPUPods {
				// GPU demand returned - need to reclaim node
				rec := optimizer.Recommendation{
					ID:             fmt.Sprintf("gpu-reclaim-%s", node.Node.Name),
					Type:           optimizer.RecommendationGPUOptimize,
					Priority:       optimizer.PriorityHigh,
					AutoExecutable: true,
					TargetKind:     "Node",
					TargetName:     node.Node.Name,
					Summary:        fmt.Sprintf("GPU demand returned on %s, reclaim from CPU fallback", node.Node.Name),
					ActionSteps: []string{
						fmt.Sprintf("Re-taint %s with nvidia.com/gpu:NoSchedule", node.Node.Name),
						"Remove gpu-fallback annotation",
						"Low-priority CPU pods will be evicted by scheduler",
					},
					Details: map[string]string{
						"nodeName": node.Node.Name,
						"action":   "disable-cpu-fallback",
					},
				}

				// Check if cluster has capacity to absorb displaced CPU pods
				cpuPodsOnNode := int64(0)
				for _, pod := range node.Pods {
					// Count non-GPU pods
					isGPUPod := false
					for _, c := range pod.Spec.Containers {
						if _, ok := c.Resources.Requests["nvidia.com/gpu"]; ok {
							isGPUPod = true
							break
						}
					}
					if !isGPUPod {
						for _, c := range pod.Spec.Containers {
							if cpu, ok := c.Resources.Requests[corev1.ResourceCPU]; ok {
								cpuPodsOnNode += cpu.MilliValue()
							}
						}
					}
				}

				// Calculate available CPU across non-GPU nodes
				availableCPU := int64(0)
				for _, other := range snapshot.Nodes {
					if other.IsGPUNode || other.Node.Name == node.Node.Name {
						continue
					}
					availableCPU += other.CPUCapacity - other.CPURequested
				}

				if availableCPU < cpuPodsOnNode {
					// Not enough capacity - mark as non-auto-executable
					rec.AutoExecutable = false
					rec.Summary = fmt.Sprintf("GPU demand returned on %s but insufficient CPU capacity to absorb displaced pods (%dm needed, %dm available)", node.Node.Name, cpuPodsOnNode, availableCPU)
				}

				recs = append(recs, rec)
			}
		}
	}

	intmetrics.GPUNodesCPUFallback.Set(float64(cpuFallbackCount))

	return recs, nil
}

// violatesGPURequestPolicy returns true if any container in the pod requests
// nvidia.com/gpu (even with quantity 0). CPU pods on GPU fallback nodes must
// never include nvidia.com/gpu in their resource specs.
func violatesGPURequestPolicy(pod *corev1.Pod) bool {
	for _, c := range pod.Spec.Containers {
		if _, ok := c.Resources.Requests[GPUResourceName]; ok {
			return true
		}
		if _, ok := c.Resources.Limits[GPUResourceName]; ok {
			return true
		}
	}
	for _, c := range pod.Spec.InitContainers {
		if _, ok := c.Resources.Requests[GPUResourceName]; ok {
			return true
		}
		if _, ok := c.Resources.Limits[GPUResourceName]; ok {
			return true
		}
	}
	return false
}

// ComputeCPUHeadroom calculates how much CPU is available for scavenger (CPU fallback)
// pods on a GPU node, after reserving capacity for GPU pod data-loading phases.
//
// Formula: headroom = nodeAllocatableCPU - gpuPodCPUUsage - (nodeAllocatableCPU * reservePct/100)
// The reserve ensures GPU pods have breathing room during CPU-intensive data loading/preprocessing.
func ComputeCPUHeadroom(node *optimizer.NodeInfo) resource.Quantity {
	allocatable := node.Node.Status.Allocatable[corev1.ResourceCPU]
	allocMillis := allocatable.MilliValue()

	// Sum CPU used by GPU pods (pods that actually request nvidia.com/gpu > 0)
	var gpuPodCPUMillis int64
	for _, pod := range node.Pods {
		isGPUPod := false
		for _, c := range pod.Spec.Containers {
			if gpuQty, ok := c.Resources.Requests[GPUResourceName]; ok && !gpuQty.IsZero() {
				isGPUPod = true
				break
			}
		}
		if isGPUPod {
			for _, c := range pod.Spec.Containers {
				gpuPodCPUMillis += c.Resources.Requests.Cpu().MilliValue()
			}
		}
	}

	// Reserve extra headroom for GPU pod data-loading CPU bursts
	reserveMillis := allocMillis * CPUHeadroomReservePct / 100

	headroomMillis := allocMillis - gpuPodCPUMillis - reserveMillis
	if headroomMillis < 0 {
		headroomMillis = 0
	}
	return *resource.NewMilliQuantity(headroomMillis, resource.DecimalSI)
}

// BuildResourceQuotaRecommendation generates a recommendation to create a ResourceQuota
// in the scavenger/fallback namespace to cap how much CPU/RAM fallback pods can claim.
// This prevents CPU pods from starving GPU workloads on shared nodes.
func BuildResourceQuotaRecommendation(namespace string, cpuLimit, memoryLimit resource.Quantity) optimizer.Recommendation {
	return optimizer.Recommendation{
		ID:             fmt.Sprintf("gpu-fallback-quota-%s", namespace),
		Type:           optimizer.RecommendationGPUOptimize,
		Priority:       optimizer.PriorityMedium,
		AutoExecutable: false,
		TargetKind:     "Namespace",
		TargetName:     namespace,
		Summary:        fmt.Sprintf("Create ResourceQuota in %s to cap CPU fallback pod resources (CPU: %s, Memory: %s)", namespace, cpuLimit.String(), memoryLimit.String()),
		ActionSteps: []string{
			fmt.Sprintf("Create ResourceQuota in namespace %s", namespace),
			fmt.Sprintf("Set requests.cpu limit to %s", cpuLimit.String()),
			fmt.Sprintf("Set requests.memory limit to %s", memoryLimit.String()),
			"This prevents scavenger CPU pods from claiming too many resources on GPU nodes",
		},
		Details: map[string]string{
			"action":      "create-resource-quota",
			"namespace":   namespace,
			"cpuLimit":    cpuLimit.String(),
			"memoryLimit": memoryLimit.String(),
		},
	}
}

// Execute applies GPU fallback taint changes.
func (f *FallbackManager) Execute(ctx context.Context, rec optimizer.Recommendation) error {
	logger := log.FromContext(ctx).WithName("gpu-fallback")
	nodeName := rec.Details["nodeName"]
	action := rec.Details["action"]

	node := &corev1.Node{}
	if err := f.client.Get(ctx, types.NamespacedName{Name: nodeName}, node); err != nil {
		return fmt.Errorf("getting node %s: %w", nodeName, err)
	}

	switch action {
	case "enable-cpu-fallback":
		return f.enableCPUFallback(ctx, node, logger)
	case "disable-cpu-fallback":
		return f.disableCPUFallback(ctx, node, logger)
	default:
		return fmt.Errorf("unknown GPU fallback action: %s", action)
	}
}

func (f *FallbackManager) enableCPUFallback(ctx context.Context, node *corev1.Node, logger interface{ Info(string, ...interface{}) }) error {
	// Remove GPU NoSchedule taint to allow CPU workloads
	var newTaints []corev1.Taint
	for _, t := range node.Spec.Taints {
		if t.Key == GPUFallbackTaint && t.Effect == corev1.TaintEffectNoSchedule {
			continue // Remove this taint
		}
		newTaints = append(newTaints, t)
	}
	node.Spec.Taints = newTaints

	// Add fallback annotation
	if node.Annotations == nil {
		node.Annotations = make(map[string]string)
	}
	node.Annotations[GPUFallbackAnnotation] = "true"

	// Add annotation with CPU headroom guidance for admission webhooks or schedulers.
	// CPU pods on GPU nodes should set conservative resource requests to avoid
	// competing with GPU workloads during data-loading CPU bursts.
	allocCPU := node.Status.Allocatable[corev1.ResourceCPU]
	headroomMillis := allocCPU.MilliValue() * (100 - CPUHeadroomReservePct) / 100
	node.Annotations["koptimizer.io/cpu-headroom-millis"] = fmt.Sprintf("%d", headroomMillis)

	// Safety notes for CPU pods on GPU nodes:
	// - Set resource requests conservatively; GPU pods need CPU during data loading phases
	// - Monitor node_cpu_seconds_total minus GPU pod usage in Prometheus for actual headroom
	// - Use ResourceQuotas per namespace to cap scavenger pod resource claims
	// - NEVER let CPU pods request nvidia.com/gpu (even 0 causes scheduler issues)
	// - Java pods using -XX:MaxRAMPercentage may over-claim memory on high-memory GPU nodes
	// - GPU nodes are always amd64 — no arm64 pods will land here

	if err := f.client.Update(ctx, node); err != nil {
		return fmt.Errorf("enabling CPU fallback on %s: %w", node.Name, err)
	}

	logger.Info("Enabled CPU fallback on GPU node",
		"node", node.Name,
		"cpuHeadroomMillis", headroomMillis,
		"reservePct", CPUHeadroomReservePct,
	)

	return nil
}

func (f *FallbackManager) disableCPUFallback(ctx context.Context, node *corev1.Node, logger interface{ Info(string, ...interface{}) }) error {
	// Re-add GPU NoSchedule taint
	hasTaint := false
	for _, t := range node.Spec.Taints {
		if t.Key == GPUFallbackTaint && t.Effect == corev1.TaintEffectNoSchedule {
			hasTaint = true
			break
		}
	}
	if !hasTaint {
		node.Spec.Taints = append(node.Spec.Taints, corev1.Taint{
			Key:    GPUFallbackTaint,
			Value:  "present",
			Effect: corev1.TaintEffectNoSchedule,
		})
	}

	// Remove fallback annotation
	delete(node.Annotations, GPUFallbackAnnotation)

	if err := f.client.Update(ctx, node); err != nil {
		return fmt.Errorf("disabling CPU fallback on %s: %w", node.Name, err)
	}

	return nil
}

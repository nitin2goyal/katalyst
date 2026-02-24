package rightsizer

import (
	"context"
	"encoding/json"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// Actuator applies resource patches to workloads. It supports two modes:
// 1. In-place pod resize (K8s 1.27+ with InPlacePodVerticalScaling feature gate)
//    — patches running pods directly without restart
// 2. Deployment/StatefulSet patch — triggers a rolling restart
type Actuator struct {
	client              client.Client
	config              *config.Config
	inPlaceResizeSupported bool
}

func NewActuator(c client.Client, cfg *config.Config) *Actuator {
	return &Actuator{client: c, config: cfg}
}

// DetectInPlaceResizeSupport checks if the cluster supports in-place pod resize.
// This is detected by checking if pods have the ResizePolicy field or if the
// feature gate is enabled.
func (a *Actuator) DetectInPlaceResizeSupport(ctx context.Context) {
	// Check by listing a pod and seeing if resize status is available
	podList := &corev1.PodList{}
	if err := a.client.List(ctx, podList, client.Limit(1)); err != nil {
		return
	}
	if len(podList.Items) > 0 {
		// If the pod has Resize field in status, in-place resize is supported
		// The field exists in the API since 1.27 when the feature gate is on
		pod := &podList.Items[0]
		if len(pod.Status.Resize) > 0 || a.hasResizePolicy(pod) {
			a.inPlaceResizeSupported = true
		}
	}
}

func (a *Actuator) hasResizePolicy(pod *corev1.Pod) bool {
	for _, c := range pod.Spec.Containers {
		if len(c.ResizePolicy) > 0 {
			return true
		}
	}
	return false
}

// Apply patches the target workload's resource requests. If in-place resize
// is supported, it patches pods directly. Otherwise, it patches the owning
// Deployment/StatefulSet which triggers a rolling restart.
func (a *Actuator) Apply(ctx context.Context, rec optimizer.Recommendation) error {
	logger := log.FromContext(ctx).WithName("rightsizer-actuator")

	resourceType := rec.Details["resource"]
	suggestedStr := rec.Details["suggestedRequest"]

	// Validate inputs before applying any changes
	if suggestedStr == "" {
		return fmt.Errorf("missing suggestedRequest in recommendation details")
	}
	qty, err := resource.ParseQuantity(suggestedStr)
	if err != nil {
		return fmt.Errorf("invalid resource quantity %q: %w", suggestedStr, err)
	}
	if qty.IsZero() {
		return fmt.Errorf("suggested resource quantity must be positive, got %s", suggestedStr)
	}
	switch resourceType {
	case "cpu", "memory":
	default:
		return fmt.Errorf("unsupported resource type %q: must be cpu or memory", resourceType)
	}

	// Try in-place pod resize first if supported
	if a.inPlaceResizeSupported && rec.Details["podName"] != "" {
		err := a.resizePodInPlace(ctx, rec.TargetNamespace, rec.Details["podName"], resourceType, suggestedStr)
		if err == nil {
			logger.Info("Applied in-place pod resize",
				"pod", rec.Details["podName"],
				"resource", resourceType,
				"value", suggestedStr,
			)
			return nil
		}
		logger.V(1).Info("In-place resize failed, falling back to deployment patch", "error", err)
	}

	switch rec.TargetKind {
	case "Deployment":
		return a.patchDeployment(ctx, rec.TargetNamespace, rec.TargetName, resourceType, suggestedStr)
	case "StatefulSet":
		return a.patchStatefulSet(ctx, rec.TargetNamespace, rec.TargetName, resourceType, suggestedStr)
	case "ReplicaSet":
		logger.V(1).Info("Skipping ReplicaSet patch (should patch owning Deployment instead)")
		return nil
	default:
		logger.V(1).Info("Unsupported target kind for patching", "kind", rec.TargetKind)
		return nil
	}
}

// resizePodInPlace directly patches a running pod's resource requests using
// the Kubernetes in-place pod resize feature (K8s 1.27+).
func (a *Actuator) resizePodInPlace(ctx context.Context, namespace, podName, resourceType, value string) error {
	pod := &corev1.Pod{}
	if err := a.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: podName}, pod); err != nil {
		return fmt.Errorf("getting pod %s/%s: %w", namespace, podName, err)
	}

	qty, err := resource.ParseQuantity(value)
	if err != nil {
		return fmt.Errorf("parsing quantity %q: %w", value, err)
	}

	var resourceName corev1.ResourceName
	switch resourceType {
	case "cpu":
		resourceName = corev1.ResourceCPU
	case "memory":
		resourceName = corev1.ResourceMemory
	default:
		return fmt.Errorf("unsupported resource type: %s", resourceType)
	}

	// Build patch for pod resources
	containerPatches := make([]map[string]interface{}, len(pod.Spec.Containers))
	for i, c := range pod.Spec.Containers {
		containerPatches[i] = map[string]interface{}{
			"name": c.Name,
			"resources": map[string]interface{}{
				"requests": map[string]string{
					string(resourceName): qty.String(),
				},
			},
		}
	}

	patchData := map[string]interface{}{
		"spec": map[string]interface{}{
			"containers": containerPatches,
		},
	}

	patch, err := json.Marshal(patchData)
	if err != nil {
		return err
	}

	return a.client.Patch(ctx, pod, client.RawPatch(types.StrategicMergePatchType, patch))
}

func (a *Actuator) patchDeployment(ctx context.Context, namespace, name, resourceType, value string) error {
	deploy := &appsv1.Deployment{}
	if err := a.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, deploy); err != nil {
		return fmt.Errorf("getting deployment %s/%s: %w", namespace, name, err)
	}

	patchData := buildResourcePatch(deploy.Spec.Template.Spec.Containers, resourceType, value)
	if patchData == nil {
		return nil
	}

	patch, err := json.Marshal(patchData)
	if err != nil {
		return err
	}

	return a.client.Patch(ctx, deploy, client.RawPatch(types.StrategicMergePatchType, patch))
}

func (a *Actuator) patchStatefulSet(ctx context.Context, namespace, name, resourceType, value string) error {
	sts := &appsv1.StatefulSet{}
	if err := a.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, sts); err != nil {
		return fmt.Errorf("getting statefulset %s/%s: %w", namespace, name, err)
	}

	patchData := buildResourcePatch(sts.Spec.Template.Spec.Containers, resourceType, value)
	if patchData == nil {
		return nil
	}

	patch, err := json.Marshal(patchData)
	if err != nil {
		return err
	}

	return a.client.Patch(ctx, sts, client.RawPatch(types.StrategicMergePatchType, patch))
}

func buildResourcePatch(containers []corev1.Container, resourceType, value string) map[string]interface{} {
	if len(containers) == 0 {
		return nil
	}

	qty, err := resource.ParseQuantity(value)
	if err != nil {
		return nil
	}

	var resourceName corev1.ResourceName
	switch resourceType {
	case "cpu":
		resourceName = corev1.ResourceCPU
	case "memory":
		resourceName = corev1.ResourceMemory
	default:
		return nil
	}

	containerPatches := make([]map[string]interface{}, len(containers))
	for i, c := range containers {
		containerPatches[i] = map[string]interface{}{
			"name": c.Name,
			"resources": map[string]interface{}{
				"requests": map[string]string{
					string(resourceName): qty.String(),
				},
			},
		}
	}

	return map[string]interface{}{
		"spec": map[string]interface{}{
			"template": map[string]interface{}{
				"spec": map[string]interface{}{
					"containers": containerPatches,
				},
			},
		},
	}
}

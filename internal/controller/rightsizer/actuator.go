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

	// Safety net: never patch DaemonSet workloads.
	if rec.TargetKind == "DaemonSet" {
		logger.Info("Skipping DaemonSet workload", "name", rec.TargetName, "namespace", rec.TargetNamespace)
		return nil
	}

	resourceType := rec.Details["resource"]

	// Dispatch combined CPU+memory patches
	if resourceType == "cpu+memory" {
		return a.applyCombinedResources(ctx, rec)
	}

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
		return fmt.Errorf("unsupported resource type %q: must be cpu, memory, or cpu+memory", resourceType)
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
		// Resolve ReplicaSet → owning Deployment and patch that instead.
		deployName, err := a.resolveReplicaSetOwner(ctx, rec.TargetNamespace, rec.TargetName)
		if err != nil {
			return fmt.Errorf("resolving ReplicaSet %s/%s owner: %w", rec.TargetNamespace, rec.TargetName, err)
		}
		logger.Info("Resolved ReplicaSet to Deployment for patching",
			"replicaSet", rec.TargetName, "deployment", deployName)
		return a.patchDeployment(ctx, rec.TargetNamespace, deployName, resourceType, suggestedStr)
	default:
		logger.V(1).Info("Unsupported target kind for patching", "kind", rec.TargetKind)
		return nil
	}
}

// applyCombinedResources patches both CPU and memory in a single API call.
func (a *Actuator) applyCombinedResources(ctx context.Context, rec optimizer.Recommendation) error {
	logger := log.FromContext(ctx).WithName("rightsizer-actuator")

	cpuStr := rec.Details["suggestedCPURequest"]
	memStr := rec.Details["suggestedMemRequest"]
	if cpuStr == "" || memStr == "" {
		return fmt.Errorf("missing suggestedCPURequest or suggestedMemRequest in recommendation details")
	}

	// Validate both quantities
	cpuQty, err := resource.ParseQuantity(cpuStr)
	if err != nil {
		return fmt.Errorf("invalid CPU quantity %q: %w", cpuStr, err)
	}
	memQty, err := resource.ParseQuantity(memStr)
	if err != nil {
		return fmt.Errorf("invalid memory quantity %q: %w", memStr, err)
	}
	if cpuQty.IsZero() || memQty.IsZero() {
		return fmt.Errorf("suggested quantities must be positive: cpu=%s, memory=%s", cpuStr, memStr)
	}

	// Try in-place pod resize first if supported
	if a.inPlaceResizeSupported && rec.Details["podName"] != "" {
		err := a.resizePodInPlaceCombined(ctx, rec.TargetNamespace, rec.Details["podName"], cpuStr, memStr)
		if err == nil {
			logger.Info("Applied in-place combined pod resize",
				"pod", rec.Details["podName"],
				"cpu", cpuStr, "memory", memStr,
			)
			return nil
		}
		logger.V(1).Info("In-place combined resize failed, falling back to deployment patch", "error", err)
	}

	targetKind := rec.TargetKind
	targetName := rec.TargetName

	// Resolve ReplicaSet → Deployment
	if targetKind == "ReplicaSet" {
		deployName, err := a.resolveReplicaSetOwner(ctx, rec.TargetNamespace, targetName)
		if err != nil {
			return fmt.Errorf("resolving ReplicaSet %s/%s owner: %w", rec.TargetNamespace, targetName, err)
		}
		logger.Info("Resolved ReplicaSet to Deployment for combined patching",
			"replicaSet", targetName, "deployment", deployName)
		targetKind = "Deployment"
		targetName = deployName
	}

	switch targetKind {
	case "Deployment":
		return a.patchDeploymentCombined(ctx, rec.TargetNamespace, targetName, cpuStr, memStr)
	case "StatefulSet":
		return a.patchStatefulSetCombined(ctx, rec.TargetNamespace, targetName, cpuStr, memStr)
	default:
		logger.V(1).Info("Unsupported target kind for combined patching", "kind", targetKind)
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

	// Compute primary container target by subtracting sidecar requests.
	// The recommendation targets the whole pod; sidecars keep their current requests.
	targetValue := qty.MilliValue()
	if resourceType == "memory" {
		targetValue = qty.Value()
	}
	for _, c := range pod.Spec.Containers[1:] {
		switch resourceType {
		case "cpu":
			targetValue -= c.Resources.Requests.Cpu().MilliValue()
		case "memory":
			targetValue -= c.Resources.Requests.Memory().Value()
		}
	}
	if targetValue < 1 {
		return fmt.Errorf("target too small for primary container after subtracting sidecars")
	}

	var patchQty string
	switch resourceType {
	case "cpu":
		patchQty = resource.NewMilliQuantity(targetValue, resource.DecimalSI).String()
	case "memory":
		patchQty = resource.NewQuantity(targetValue, resource.BinarySI).String()
	}

	// Only patch the primary (first) container.
	containerPatches := []map[string]interface{}{
		{
			"name": pod.Spec.Containers[0].Name,
			"resources": map[string]interface{}{
				"requests": map[string]string{
					string(resourceName): patchQty,
				},
			},
		},
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

// resolveReplicaSetOwner looks up a ReplicaSet and returns the name of its
// owning Deployment. Returns an error if the RS doesn't exist or has no
// Deployment owner.
func (a *Actuator) resolveReplicaSetOwner(ctx context.Context, namespace, rsName string) (string, error) {
	rs := &appsv1.ReplicaSet{}
	if err := a.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: rsName}, rs); err != nil {
		return "", fmt.Errorf("getting ReplicaSet %s/%s: %w", namespace, rsName, err)
	}
	for _, ref := range rs.OwnerReferences {
		if ref.Kind == "Deployment" {
			return ref.Name, nil
		}
	}
	return "", fmt.Errorf("ReplicaSet %s/%s has no Deployment owner", namespace, rsName)
}

// resizePodInPlaceCombined patches both CPU and memory on a running pod.
func (a *Actuator) resizePodInPlaceCombined(ctx context.Context, namespace, podName, cpuValue, memValue string) error {
	pod := &corev1.Pod{}
	if err := a.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: podName}, pod); err != nil {
		return fmt.Errorf("getting pod %s/%s: %w", namespace, podName, err)
	}

	cpuQty, err := resource.ParseQuantity(cpuValue)
	if err != nil {
		return fmt.Errorf("parsing CPU quantity %q: %w", cpuValue, err)
	}
	memQty, err := resource.ParseQuantity(memValue)
	if err != nil {
		return fmt.Errorf("parsing memory quantity %q: %w", memValue, err)
	}

	// Compute primary container target by subtracting sidecar requests.
	targetCPUMilli := cpuQty.MilliValue()
	targetMemBytes := memQty.Value()
	for _, c := range pod.Spec.Containers[1:] {
		targetCPUMilli -= c.Resources.Requests.Cpu().MilliValue()
		targetMemBytes -= c.Resources.Requests.Memory().Value()
	}
	if targetCPUMilli < 10 || targetMemBytes < 32*1024*1024 {
		return fmt.Errorf("target too small for primary container after subtracting sidecars")
	}

	// Only patch the primary (first) container.
	containerPatches := []map[string]interface{}{
		{
			"name": pod.Spec.Containers[0].Name,
			"resources": map[string]interface{}{
				"requests": map[string]string{
					string(corev1.ResourceCPU):    resource.NewMilliQuantity(targetCPUMilli, resource.DecimalSI).String(),
					string(corev1.ResourceMemory): resource.NewQuantity(targetMemBytes, resource.BinarySI).String(),
				},
			},
		},
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

func (a *Actuator) patchDeploymentCombined(ctx context.Context, namespace, name, cpuValue, memValue string) error {
	deploy := &appsv1.Deployment{}
	if err := a.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, deploy); err != nil {
		return fmt.Errorf("getting deployment %s/%s: %w", namespace, name, err)
	}

	patchData := buildCombinedResourcePatch(deploy.Spec.Template.Spec.Containers, cpuValue, memValue)
	if patchData == nil {
		return nil
	}

	patch, err := json.Marshal(patchData)
	if err != nil {
		return err
	}

	return a.client.Patch(ctx, deploy, client.RawPatch(types.StrategicMergePatchType, patch))
}

func (a *Actuator) patchStatefulSetCombined(ctx context.Context, namespace, name, cpuValue, memValue string) error {
	sts := &appsv1.StatefulSet{}
	if err := a.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, sts); err != nil {
		return fmt.Errorf("getting statefulset %s/%s: %w", namespace, name, err)
	}

	patchData := buildCombinedResourcePatch(sts.Spec.Template.Spec.Containers, cpuValue, memValue)
	if patchData == nil {
		return nil
	}

	patch, err := json.Marshal(patchData)
	if err != nil {
		return err
	}

	return a.client.Patch(ctx, sts, client.RawPatch(types.StrategicMergePatchType, patch))
}

func buildCombinedResourcePatch(containers []corev1.Container, cpuValue, memValue string) map[string]interface{} {
	if len(containers) == 0 {
		return nil
	}

	cpuQty, err := resource.ParseQuantity(cpuValue)
	if err != nil {
		return nil
	}
	memQty, err := resource.ParseQuantity(memValue)
	if err != nil {
		return nil
	}

	// Compute primary container target by subtracting sidecar requests.
	// The recommendation targets the whole pod; sidecars keep their current
	// requests and only the primary (first) container is adjusted.
	targetCPUMilli := cpuQty.MilliValue()
	targetMemBytes := memQty.Value()
	for _, c := range containers[1:] {
		targetCPUMilli -= c.Resources.Requests.Cpu().MilliValue()
		targetMemBytes -= c.Resources.Requests.Memory().Value()
	}

	// Safety: if primary target is too small after subtracting sidecars, skip.
	if targetCPUMilli < 10 || targetMemBytes < 32*1024*1024 {
		return nil
	}

	// Only patch the primary (first) container. Strategic merge patch uses
	// the container name as merge key, so sidecars are left untouched.
	containerPatches := []map[string]interface{}{
		{
			"name": containers[0].Name,
			"resources": map[string]interface{}{
				"requests": map[string]string{
					string(corev1.ResourceCPU):    resource.NewMilliQuantity(targetCPUMilli, resource.DecimalSI).String(),
					string(corev1.ResourceMemory): resource.NewQuantity(targetMemBytes, resource.BinarySI).String(),
				},
			},
		},
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

	// Compute primary container target by subtracting sidecar requests.
	targetValue := qty.MilliValue()
	if resourceType == "memory" {
		targetValue = qty.Value()
	}
	for _, c := range containers[1:] {
		switch resourceType {
		case "cpu":
			targetValue -= c.Resources.Requests.Cpu().MilliValue()
		case "memory":
			targetValue -= c.Resources.Requests.Memory().Value()
		}
	}
	if targetValue < 1 {
		return nil
	}

	var patchValue string
	switch resourceType {
	case "cpu":
		patchValue = resource.NewMilliQuantity(targetValue, resource.DecimalSI).String()
	case "memory":
		patchValue = resource.NewQuantity(targetValue, resource.BinarySI).String()
	}

	// Only patch the primary (first) container.
	containerPatches := []map[string]interface{}{
		{
			"name": containers[0].Name,
			"resources": map[string]interface{}{
				"requests": map[string]string{
					string(resourceName): patchValue,
				},
			},
		},
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

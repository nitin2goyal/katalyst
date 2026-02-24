package workloadscaler

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// VerticalScaler manages vertical pod autoscaling (resource requests).
type VerticalScaler struct {
	client client.Client
	config *config.Config
}

func NewVerticalScaler(c client.Client, cfg *config.Config) *VerticalScaler {
	return &VerticalScaler{client: c, config: cfg}
}

func (v *VerticalScaler) Analyze(ctx context.Context, snapshot *optimizer.ClusterSnapshot) ([]optimizer.Recommendation, error) {
	var recs []optimizer.Recommendation

	for _, pod := range snapshot.Pods {
		if v.isExcluded(pod.Pod.Namespace) {
			continue
		}
		if pod.CPURequest == 0 {
			continue
		}

		cpuUtil := float64(pod.CPUUsage) / float64(pod.CPURequest) * 100
		memUtil := float64(0)
		if pod.MemoryRequest > 0 {
			memUtil = float64(pod.MemoryUsage) / float64(pod.MemoryRequest) * 100
		}

		// Suggest vertical scaling if usage is consistently much lower than request
		if cpuUtil < 30 && pod.CPURequest > 100 { // More than 100m and <30% used
			suggestedCPU := int64(float64(pod.CPUUsage) * 1.3)
			if suggestedCPU < 50 {
				suggestedCPU = 50
			}

			recs = append(recs, optimizer.Recommendation{
				ID:              fmt.Sprintf("vscale-cpu-%s-%s-%d", pod.Pod.Namespace, pod.OwnerName, time.Now().Unix()),
				Type:            optimizer.RecommendationWorkloadScale,
				Priority:        optimizer.PriorityMedium,
				AutoExecutable:  true,
				TargetKind:      pod.OwnerKind,
				TargetName:      pod.OwnerName,
				TargetNamespace: pod.Pod.Namespace,
				Summary:         fmt.Sprintf("Vertically scale %s/%s: reduce CPU from %dm to %dm (%.0f%% util)", pod.Pod.Namespace, pod.OwnerName, pod.CPURequest, suggestedCPU, cpuUtil),
				Details: map[string]string{
					"scalingType":    "vertical",
					"resource":       "cpu",
					"currentRequest": fmt.Sprintf("%dm", pod.CPURequest),
					"suggested":      fmt.Sprintf("%dm", suggestedCPU),
					"utilization":    fmt.Sprintf("%.1f", cpuUtil),
				},
				CreatedAt: time.Now(),
			})
		}

		// Memory analysis: suggest reducing memory if under 30% utilized and request > 64Mi
		if memUtil > 0 && memUtil < 30 && pod.MemoryRequest > 64*1024*1024 { // More than 64Mi and <30% used
			suggestedMem := int64(float64(pod.MemoryUsage) * 1.3)
			minMem := int64(64 * 1024 * 1024) // 64Mi floor
			if suggestedMem < minMem {
				suggestedMem = minMem
			}

			recs = append(recs, optimizer.Recommendation{
				ID:              fmt.Sprintf("vscale-mem-%s-%s-%d", pod.Pod.Namespace, pod.OwnerName, time.Now().Unix()),
				Type:            optimizer.RecommendationWorkloadScale,
				Priority:        optimizer.PriorityMedium,
				AutoExecutable:  true,
				TargetKind:      pod.OwnerKind,
				TargetName:      pod.OwnerName,
				TargetNamespace: pod.Pod.Namespace,
				Summary:         fmt.Sprintf("Vertically scale %s/%s: reduce memory from %dMi to %dMi (%.0f%% util)", pod.Pod.Namespace, pod.OwnerName, pod.MemoryRequest/(1024*1024), suggestedMem/(1024*1024), memUtil),
				Details: map[string]string{
					"scalingType":    "vertical",
					"resource":       "memory",
					"currentRequest": fmt.Sprintf("%dMi", pod.MemoryRequest/(1024*1024)),
					"suggested":      fmt.Sprintf("%dMi", suggestedMem/(1024*1024)),
					"utilization":    fmt.Sprintf("%.1f", memUtil),
				},
				CreatedAt: time.Now(),
			})
		}
	}

	return recs, nil
}

func (v *VerticalScaler) Execute(ctx context.Context, rec optimizer.Recommendation) error {
	logger := log.FromContext(ctx).WithName("vertical-scaler")

	resourceType := rec.Details["resource"]
	suggestedStr := rec.Details["suggested"]
	if resourceType == "" || suggestedStr == "" {
		return fmt.Errorf("missing resource or suggested value in recommendation details")
	}

	switch rec.TargetKind {
	case "Deployment":
		return v.patchDeployment(ctx, rec.TargetNamespace, rec.TargetName, resourceType, suggestedStr)
	case "StatefulSet":
		return v.patchStatefulSet(ctx, rec.TargetNamespace, rec.TargetName, resourceType, suggestedStr)
	default:
		logger.V(1).Info("Unsupported target kind for vertical scaling", "kind", rec.TargetKind)
		return nil
	}
}

func (v *VerticalScaler) patchDeployment(ctx context.Context, namespace, name, resourceType, value string) error {
	deploy := &appsv1.Deployment{}
	if err := v.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, deploy); err != nil {
		return fmt.Errorf("getting deployment %s/%s: %w", namespace, name, err)
	}

	patchData := buildVerticalPatch(deploy.Spec.Template.Spec.Containers, resourceType, value)
	if patchData == nil {
		return nil
	}

	patch, err := json.Marshal(patchData)
	if err != nil {
		return err
	}

	return v.client.Patch(ctx, deploy, client.RawPatch(types.StrategicMergePatchType, patch))
}

func (v *VerticalScaler) patchStatefulSet(ctx context.Context, namespace, name, resourceType, value string) error {
	sts := &appsv1.StatefulSet{}
	if err := v.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, sts); err != nil {
		return fmt.Errorf("getting statefulset %s/%s: %w", namespace, name, err)
	}

	patchData := buildVerticalPatch(sts.Spec.Template.Spec.Containers, resourceType, value)
	if patchData == nil {
		return nil
	}

	patch, err := json.Marshal(patchData)
	if err != nil {
		return err
	}

	return v.client.Patch(ctx, sts, client.RawPatch(types.StrategicMergePatchType, patch))
}

func buildVerticalPatch(containers []corev1.Container, resourceType, value string) map[string]interface{} {
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

func (v *VerticalScaler) isExcluded(namespace string) bool {
	for _, ns := range v.config.WorkloadScaler.ExcludeNamespaces {
		if ns == namespace {
			return true
		}
	}
	return false
}

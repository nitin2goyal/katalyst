package workloadscaler

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// HorizontalScaler manages horizontal pod autoscaling.
type HorizontalScaler struct {
	client client.Client
	config *config.Config
}

func NewHorizontalScaler(c client.Client, cfg *config.Config) *HorizontalScaler {
	return &HorizontalScaler{client: c, config: cfg}
}

func (h *HorizontalScaler) Analyze(ctx context.Context, snapshot *optimizer.ClusterSnapshot) ([]optimizer.Recommendation, error) {
	var recs []optimizer.Recommendation

	// List existing HPAs to understand current scaling config
	hpaList := &autoscalingv2.HorizontalPodAutoscalerList{}
	if err := h.client.List(ctx, hpaList); err != nil {
		return nil, fmt.Errorf("listing HPAs: %w", err)
	}

	// For workloads without HPA but with multiple replicas, suggest creating one
	// For workloads with HPA, check if targets are optimal
	for _, hpa := range hpaList.Items {
		if h.isExcluded(hpa.Namespace) {
			continue
		}

		// Check if HPA is hitting min/max bounds frequently
		if hpa.Status.CurrentReplicas == hpa.Spec.MaxReplicas {
			// Surge detection: if replicas are at max, this is a surge scenario.
			// Make it auto-executable so the system can increase HPA max replicas.
			surgeDetected := h.config.WorkloadScaler.SurgeDetection
			newMax := hpa.Spec.MaxReplicas + int32(float64(hpa.Spec.MaxReplicas)*0.5)
			if newMax == hpa.Spec.MaxReplicas {
				newMax = hpa.Spec.MaxReplicas + 1
			}
			// Safety cap: never exceed configured maximum replicas limit.
			if h.config.WorkloadScaler.MaxReplicasLimit > 0 && newMax > int32(h.config.WorkloadScaler.MaxReplicasLimit) {
				newMax = int32(h.config.WorkloadScaler.MaxReplicasLimit)
			}
			if newMax <= hpa.Spec.MaxReplicas {
				continue // already at or above the safety limit
			}

			recs = append(recs, optimizer.Recommendation{
				ID:              fmt.Sprintf("hpa-maxed-%s-%s-%d", hpa.Namespace, hpa.Name, time.Now().Unix()),
				Type:            optimizer.RecommendationWorkloadScale,
				Priority:        optimizer.PriorityHigh,
				AutoExecutable:  surgeDetected,
				TargetKind:      hpa.Spec.ScaleTargetRef.Kind,
				TargetName:      hpa.Spec.ScaleTargetRef.Name,
				TargetNamespace: hpa.Namespace,
				Summary:         fmt.Sprintf("HPA %s/%s is at max replicas (%d), increase max to %d", hpa.Namespace, hpa.Name, hpa.Spec.MaxReplicas, newMax),
				ActionSteps: []string{
					fmt.Sprintf("Increase HPA maxReplicas from %d to %d", hpa.Spec.MaxReplicas, newMax),
				},
				Details: map[string]string{
					"scalingType":     "horizontal",
					"hpaName":         hpa.Name,
					"hpaNamespace":    hpa.Namespace,
					"currentReplicas": fmt.Sprintf("%d", hpa.Status.CurrentReplicas),
					"maxReplicas":     fmt.Sprintf("%d", hpa.Spec.MaxReplicas),
					"newMaxReplicas":  fmt.Sprintf("%d", newMax),
					"surgeDetected":   fmt.Sprintf("%t", surgeDetected),
				},
				CreatedAt: time.Now(),
			})
		}
	}

	return recs, nil
}

func (h *HorizontalScaler) Execute(ctx context.Context, rec optimizer.Recommendation) error {
	logger := log.FromContext(ctx).WithName("horizontal-scaler")

	if !rec.AutoExecutable {
		logger.V(1).Info("Horizontal scaling recommendation is manual, skipping auto-execution", "id", rec.ID)
		return nil
	}

	hpaName := rec.Details["hpaName"]
	hpaNamespace := rec.Details["hpaNamespace"]
	newMaxStr := rec.Details["newMaxReplicas"]

	if hpaName == "" || hpaNamespace == "" || newMaxStr == "" {
		return fmt.Errorf("missing HPA details in recommendation: hpaName=%q, hpaNamespace=%q, newMaxReplicas=%q", hpaName, hpaNamespace, newMaxStr)
	}

	newMax, err := strconv.ParseInt(newMaxStr, 10, 32)
	if err != nil {
		return fmt.Errorf("parsing newMaxReplicas %q: %w", newMaxStr, err)
	}

	// Get the HPA
	hpa := &autoscalingv2.HorizontalPodAutoscaler{}
	if err := h.client.Get(ctx, types.NamespacedName{Namespace: hpaNamespace, Name: hpaName}, hpa); err != nil {
		return fmt.Errorf("getting HPA %s/%s: %w", hpaNamespace, hpaName, err)
	}

	// Build a patch to update maxReplicas
	newMaxInt32 := int32(newMax)
	patchData := map[string]interface{}{
		"spec": map[string]interface{}{
			"maxReplicas": newMaxInt32,
		},
	}
	patch, err := json.Marshal(patchData)
	if err != nil {
		return fmt.Errorf("marshaling HPA patch: %w", err)
	}

	if err := h.client.Patch(ctx, hpa, client.RawPatch(types.MergePatchType, patch)); err != nil {
		return fmt.Errorf("patching HPA %s/%s maxReplicas to %d: %w", hpaNamespace, hpaName, newMaxInt32, err)
	}

	logger.Info("Scaled HPA max replicas", "hpa", hpaNamespace+"/"+hpaName, "newMaxReplicas", newMaxInt32)
	return nil
}

func (h *HorizontalScaler) isExcluded(namespace string) bool {
	for _, ns := range h.config.WorkloadScaler.ExcludeNamespaces {
		if ns == namespace {
			return true
		}
	}
	return false
}

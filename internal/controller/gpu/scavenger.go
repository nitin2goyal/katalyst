package gpu

import (
	"context"
	"fmt"
	"math"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/koptimizer/koptimizer/internal/config"
	intmetrics "github.com/koptimizer/koptimizer/internal/metrics"
	"github.com/koptimizer/koptimizer/pkg/cost"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// Scavenger manages CPU scavenging on GPU nodes that have active GPU workloads
// but spare CPU capacity. Unlike fallback (which operates on idle GPU nodes),
// scavenging labels active GPU nodes so that low-priority pods with the right
// tolerations and nodeAffinity can use leftover CPU.
type Scavenger struct {
	client client.Client
	config *config.Config
}

func NewScavenger(c client.Client, cfg *config.Config) *Scavenger {
	return &Scavenger{client: c, config: cfg}
}

// DetectScavengeable analyzes GPU nodes and generates recommendations for
// enabling, updating, or disabling CPU scavenging based on available headroom.
func (s *Scavenger) DetectScavengeable(ctx context.Context, snapshot *optimizer.ClusterSnapshot) ([]optimizer.Recommendation, error) {
	var recs []optimizer.Recommendation
	scavengingCount := 0
	threshold := s.config.GPU.ScavengingCPUThresholdMillis

	for i := range snapshot.Nodes {
		node := &snapshot.Nodes[i]
		if !node.IsGPUNode {
			continue
		}

		// Skip nodes already in fallback mode (mutually exclusive)
		if node.Node.Annotations != nil {
			if _, inFallback := node.Node.Annotations[GPUFallbackAnnotation]; inFallback {
				continue
			}
		}

		headroom := ComputeCPUHeadroom(node)
		headroomMillis := headroom.MilliValue()

		isLabeled := false
		if node.Node.Labels != nil {
			_, isLabeled = node.Node.Labels[GPUScavengerLabel]
		}

		if headroomMillis >= threshold {
			if !isLabeled {
				recs = append(recs, s.buildEnableRec(node, headroomMillis))
			} else {
				// Check if headroom changed significantly (>20%)
				if s.headroomChangedSignificantly(node, headroomMillis) {
					recs = append(recs, s.buildUpdateRec(node, headroomMillis))
				}
			}
			scavengingCount++
		} else if isLabeled {
			recs = append(recs, s.buildDisableRec(node))
		}
	}

	intmetrics.GPUNodesCPUScavenging.Set(float64(scavengingCount))

	return recs, nil
}

// headroomChangedSignificantly returns true if the current headroom differs
// from the annotated value by more than 20%.
func (s *Scavenger) headroomChangedSignificantly(node *optimizer.NodeInfo, currentMillis int64) bool {
	if node.Node.Annotations == nil {
		return true
	}
	prev, ok := node.Node.Annotations[GPUScavengerHeadroom]
	if !ok {
		return true
	}
	prevMillis, err := strconv.ParseInt(prev, 10, 64)
	if err != nil {
		return true
	}
	if prevMillis == 0 {
		return currentMillis > 0
	}
	change := math.Abs(float64(currentMillis-prevMillis)) / float64(prevMillis)
	return change > 0.20
}

func (s *Scavenger) estimateSavings(node *optimizer.NodeInfo, headroomMillis int64) optimizer.SavingEstimate {
	allocatable := node.Node.Status.Allocatable[corev1.ResourceCPU]
	allocMillis := allocatable.MilliValue()
	if allocMillis == 0 {
		return optimizer.SavingEstimate{}
	}
	monthlyCost := node.HourlyCostUSD * cost.HoursPerMonth
	// Attribute savings only to the CPU cost fraction of the node, not the
	// full node cost (which on GPU nodes includes the expensive GPU).
	// Use vCPU-based pricing: approximate CPU fraction as allocMillis / totalNodeMillis.
	// For GPU nodes the GPU is the dominant cost; CPU is typically <5% of total.
	cpuFraction := cost.EstimateCPUCostFraction(allocMillis, node.IsGPUNode)
	// Conservative 30% assumed utilization of scavenged CPU
	monthly := float64(headroomMillis) / float64(allocMillis) * monthlyCost * cpuFraction * 0.3
	return optimizer.SavingEstimate{
		MonthlySavingsUSD: monthly,
		AnnualSavingsUSD:  monthly * 12,
		Currency:          "USD",
	}
}

func (s *Scavenger) buildEnableRec(node *optimizer.NodeInfo, headroomMillis int64) optimizer.Recommendation {
	return optimizer.Recommendation{
		ID:             fmt.Sprintf("gpu-scavenge-enable-%s", node.Node.Name),
		Type:           optimizer.RecommendationGPUOptimize,
		Priority:       optimizer.PriorityMedium,
		AutoExecutable: true,
		TargetKind:     "Node",
		TargetName:     node.Node.Name,
		Summary:        fmt.Sprintf("Enable CPU scavenging on GPU node %s (%dm spare CPU)", node.Node.Name, headroomMillis),
		ActionSteps: []string{
			fmt.Sprintf("Add label %s=true to node %s", GPUScavengerLabel, node.Node.Name),
			fmt.Sprintf("Add annotation %s=true", GPUScavengerAnnotation),
			fmt.Sprintf("Set headroom annotation %s=%d", GPUScavengerHeadroom, headroomMillis),
		},
		EstimatedSaving: s.estimateSavings(node, headroomMillis),
		Details: map[string]string{
			"nodeName":       node.Node.Name,
			"action":         "enable-cpu-scavenging",
			"headroomMillis": strconv.FormatInt(headroomMillis, 10),
		},
	}
}

func (s *Scavenger) buildDisableRec(node *optimizer.NodeInfo) optimizer.Recommendation {
	return optimizer.Recommendation{
		ID:             fmt.Sprintf("gpu-scavenge-disable-%s", node.Node.Name),
		Type:           optimizer.RecommendationGPUOptimize,
		Priority:       optimizer.PriorityHigh,
		AutoExecutable: true,
		TargetKind:     "Node",
		TargetName:     node.Node.Name,
		Summary:        fmt.Sprintf("Disable CPU scavenging on GPU node %s (insufficient headroom)", node.Node.Name),
		ActionSteps: []string{
			fmt.Sprintf("Remove label %s from node %s", GPUScavengerLabel, node.Node.Name),
			fmt.Sprintf("Remove annotations %s and %s", GPUScavengerAnnotation, GPUScavengerHeadroom),
			"Existing scavenger pods stay until evicted or completed; no new ones will schedule",
		},
		Details: map[string]string{
			"nodeName": node.Node.Name,
			"action":   "disable-cpu-scavenging",
		},
	}
}

func (s *Scavenger) buildUpdateRec(node *optimizer.NodeInfo, headroomMillis int64) optimizer.Recommendation {
	return optimizer.Recommendation{
		ID:             fmt.Sprintf("gpu-scavenge-update-%s", node.Node.Name),
		Type:           optimizer.RecommendationGPUOptimize,
		Priority:       optimizer.PriorityLow,
		AutoExecutable: true,
		TargetKind:     "Node",
		TargetName:     node.Node.Name,
		Summary:        fmt.Sprintf("Update CPU scavenging headroom on GPU node %s to %dm", node.Node.Name, headroomMillis),
		ActionSteps: []string{
			fmt.Sprintf("Update annotation %s=%d on node %s", GPUScavengerHeadroom, headroomMillis, node.Node.Name),
		},
		EstimatedSaving: s.estimateSavings(node, headroomMillis),
		Details: map[string]string{
			"nodeName":       node.Node.Name,
			"action":         "update-cpu-scavenging",
			"headroomMillis": strconv.FormatInt(headroomMillis, 10),
		},
	}
}

// Execute applies a scavenging recommendation to the cluster.
func (s *Scavenger) Execute(ctx context.Context, rec optimizer.Recommendation) error {
	logger := log.FromContext(ctx).WithName("gpu-scavenger")
	nodeName := rec.Details["nodeName"]
	action := rec.Details["action"]

	node := &corev1.Node{}
	if err := s.client.Get(ctx, types.NamespacedName{Name: nodeName}, node); err != nil {
		return fmt.Errorf("getting node %s: %w", nodeName, err)
	}

	switch action {
	case "enable-cpu-scavenging":
		return s.enableScavenging(ctx, node, rec.Details["headroomMillis"], logger)
	case "disable-cpu-scavenging":
		return s.disableScavenging(ctx, node, logger)
	case "update-cpu-scavenging":
		return s.updateScavenging(ctx, node, rec.Details["headroomMillis"], logger)
	default:
		return fmt.Errorf("unknown scavenging action: %s", action)
	}
}

func (s *Scavenger) enableScavenging(ctx context.Context, node *corev1.Node, headroomMillis string, logger interface{ Info(string, ...interface{}) }) error {
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		fresh := &corev1.Node{}
		if err := s.client.Get(ctx, types.NamespacedName{Name: node.Name}, fresh); err != nil {
			return err
		}
		if fresh.Labels == nil {
			fresh.Labels = make(map[string]string)
		}
		fresh.Labels[GPUScavengerLabel] = "true"
		if fresh.Annotations == nil {
			fresh.Annotations = make(map[string]string)
		}
		fresh.Annotations[GPUScavengerAnnotation] = "true"
		fresh.Annotations[GPUScavengerHeadroom] = headroomMillis
		return s.client.Update(ctx, fresh)
	})
	if err != nil {
		return fmt.Errorf("enabling CPU scavenging on %s: %w", node.Name, err)
	}

	logger.Info("Enabled CPU scavenging on GPU node",
		"node", node.Name,
		"headroomMillis", headroomMillis,
	)
	return nil
}

func (s *Scavenger) disableScavenging(ctx context.Context, node *corev1.Node, logger interface{ Info(string, ...interface{}) }) error {
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		fresh := &corev1.Node{}
		if err := s.client.Get(ctx, types.NamespacedName{Name: node.Name}, fresh); err != nil {
			return err
		}
		delete(fresh.Labels, GPUScavengerLabel)
		delete(fresh.Annotations, GPUScavengerAnnotation)
		delete(fresh.Annotations, GPUScavengerHeadroom)
		return s.client.Update(ctx, fresh)
	})
	if err != nil {
		return fmt.Errorf("disabling CPU scavenging on %s: %w", node.Name, err)
	}

	logger.Info("Disabled CPU scavenging on GPU node", "node", node.Name)
	return nil
}

func (s *Scavenger) updateScavenging(ctx context.Context, node *corev1.Node, headroomMillis string, logger interface{ Info(string, ...interface{}) }) error {
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		fresh := &corev1.Node{}
		if err := s.client.Get(ctx, types.NamespacedName{Name: node.Name}, fresh); err != nil {
			return err
		}
		if fresh.Annotations == nil {
			fresh.Annotations = make(map[string]string)
		}
		fresh.Annotations[GPUScavengerHeadroom] = headroomMillis
		return s.client.Update(ctx, fresh)
	})
	if err != nil {
		return fmt.Errorf("updating CPU scavenging headroom on %s: %w", node.Name, err)
	}

	logger.Info("Updated CPU scavenging headroom on GPU node",
		"node", node.Name,
		"headroomMillis", headroomMillis,
	)
	return nil
}

package nodegroupmgr

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	koptv1alpha1 "github.com/koptimizer/koptimizer/api/v1alpha1"
	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// Lifecycle manages creating Recommendation CRDs for node group lifecycle events.
type Lifecycle struct {
	client client.Client
	config *config.Config
}

func NewLifecycle(c client.Client, cfg *config.Config) *Lifecycle {
	return &Lifecycle{client: c, config: cfg}
}

// CreateRecommendationCRD creates a Recommendation CRD from an optimizer recommendation.
func (l *Lifecycle) CreateRecommendationCRD(ctx context.Context, rec optimizer.Recommendation) error {
	crd := &koptv1alpha1.Recommendation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      rec.ID,
			Namespace: "koptimizer-system",
		},
		Spec: koptv1alpha1.RecommendationSpec{
			Type:            string(rec.Type),
			Priority:        string(rec.Priority),
			TargetKind:      rec.TargetKind,
			TargetName:      rec.TargetName,
			TargetNamespace: rec.TargetNamespace,
			Summary:         rec.Summary,
			ActionSteps:     rec.ActionSteps,
			AutoExecutable:  rec.AutoExecutable,
			RequiresAIGate:  rec.RequiresAIGate,
			EstimatedSaving: koptv1alpha1.SavingEstimate{
				MonthlySavingsUSD: rec.EstimatedSaving.MonthlySavingsUSD,
				AnnualSavingsUSD:  rec.EstimatedSaving.AnnualSavingsUSD,
				Currency:          rec.EstimatedSaving.Currency,
			},
			EstimatedImpact: koptv1alpha1.ImpactEstimate{
				MonthlyCostChangeUSD: rec.EstimatedImpact.MonthlyCostChangeUSD,
				NodesAffected:        rec.EstimatedImpact.NodesAffected,
				PodsAffected:         rec.EstimatedImpact.PodsAffected,
				RiskLevel:            rec.EstimatedImpact.RiskLevel,
			},
			Details: rec.Details,
		},
		Status: koptv1alpha1.RecommendationStatus{
			State: "pending",
		},
	}

	if err := l.client.Create(ctx, crd); err != nil {
		return fmt.Errorf("creating recommendation CRD: %w", err)
	}
	return nil
}

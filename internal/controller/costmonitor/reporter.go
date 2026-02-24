package costmonitor

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	koptv1alpha1 "github.com/koptimizer/koptimizer/api/v1alpha1"
	"github.com/koptimizer/koptimizer/pkg/cost"
)

// Reporter creates/updates CostReport CRDs.
type Reporter struct {
	client      client.Client
	clusterName string
}

func NewReporter(c client.Client, clusterName string) *Reporter {
	return &Reporter{client: c, clusterName: clusterName}
}

func (r *Reporter) UpdateCostReport(ctx context.Context, totalMonthlyCost float64, costByNS map[string]float64, costByNG map[string]float64, topWorkloads []cost.WorkloadCost) error {
	reportName := fmt.Sprintf("%s-cost", r.clusterName)
	if r.clusterName == "" {
		reportName = "cluster-cost"
	}

	report := &koptv1alpha1.CostReport{}
	err := r.client.Get(ctx, types.NamespacedName{Name: reportName, Namespace: "koptimizer-system"}, report)
	if err != nil {
		// Create new report
		report = &koptv1alpha1.CostReport{
			ObjectMeta: metav1.ObjectMeta{
				Name:      reportName,
				Namespace: "koptimizer-system",
			},
			Spec: koptv1alpha1.CostReportSpec{
				ClusterName:  r.clusterName,
				ReportPeriod: "monthly",
			},
		}
		if err := r.client.Create(ctx, report); err != nil {
			return fmt.Errorf("creating cost report: %w", err)
		}
	}

	// Convert top workloads
	wlCosts := make([]koptv1alpha1.WorkloadCost, 0, len(topWorkloads))
	for _, wl := range topWorkloads {
		wlCosts = append(wlCosts, koptv1alpha1.WorkloadCost{
			Namespace:      wl.Namespace,
			Name:           wl.Name,
			Kind:           wl.Kind,
			MonthlyCostUSD: wl.MonthlyCostUSD,
		})
	}

	report.Status.LastUpdated = metav1.Time{Time: time.Now()}
	report.Status.TotalMonthlyCostUSD = totalMonthlyCost
	report.Status.CostByNamespace = costByNS
	report.Status.CostByNodeGroup = costByNG
	report.Status.TopWorkloads = wlCosts

	return r.client.Status().Update(ctx, report)
}

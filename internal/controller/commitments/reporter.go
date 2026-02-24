package commitments

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	koptv1alpha1 "github.com/koptimizer/koptimizer/api/v1alpha1"
	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
	"github.com/koptimizer/koptimizer/pkg/cost"
)

// Reporter creates/updates CommitmentReport CRDs.
type Reporter struct {
	client      client.Client
	clusterName string
}

func NewReporter(c client.Client, clusterName string) *Reporter {
	return &Reporter{client: c, clusterName: clusterName}
}

func (r *Reporter) UpdateCommitmentReport(ctx context.Context, commitments []*cloudprovider.Commitment, expiryWarningDays []int) error {
	reportName := fmt.Sprintf("%s-commitments", r.clusterName)
	if r.clusterName == "" {
		reportName = "cluster-commitments"
	}

	report := &koptv1alpha1.CommitmentReport{}
	err := r.client.Get(ctx, types.NamespacedName{Name: reportName, Namespace: "koptimizer-system"}, report)
	if err != nil {
		report = &koptv1alpha1.CommitmentReport{
			ObjectMeta: metav1.ObjectMeta{
				Name:      reportName,
				Namespace: "koptimizer-system",
			},
			Spec: koptv1alpha1.CommitmentReportSpec{
				ClusterName: r.clusterName,
			},
		}
		if err := r.client.Create(ctx, report); err != nil {
			return fmt.Errorf("creating commitment report: %w", err)
		}
	}

	// Build status
	totalCost := 0.0
	totalUtil := 0.0
	activeCount := 0
	var statuses []koptv1alpha1.CommitmentStatus
	var underutilized []koptv1alpha1.UnderutilizedCommitment
	var expiring []koptv1alpha1.ExpiringCommitment

	for _, c := range commitments {
		expiresAt := ""
		if !c.ExpiresAt.IsZero() {
			expiresAt = c.ExpiresAt.Format(time.RFC3339)
		}
		statuses = append(statuses, koptv1alpha1.CommitmentStatus{
			ID:              c.ID,
			Type:            c.Type,
			InstanceFamily:  c.InstanceFamily,
			InstanceType:    c.InstanceType,
			Region:          c.Region,
			Count:           c.Count,
			HourlyCostUSD:   c.HourlyCostUSD,
			OnDemandCostUSD: c.OnDemandCostUSD,
			UtilizationPct:  c.UtilizationPct,
			ExpiresAt:       expiresAt,
			Status:          c.Status,
		})

		if c.Status == "active" {
			totalCost += c.HourlyCostUSD * cost.HoursPerMonth
			totalUtil += c.UtilizationPct
			activeCount++

			// Check underutilized (< 50%)
			if c.UtilizationPct < 50 {
				wastedPct := (100 - c.UtilizationPct) / 100
				underutilized = append(underutilized, koptv1alpha1.UnderutilizedCommitment{
					CommitmentID:     c.ID,
					Type:             c.Type,
					InstanceType:     c.InstanceType,
					UtilizationPct:   c.UtilizationPct,
					WastedMonthlyUSD: c.HourlyCostUSD * cost.HoursPerMonth * wastedPct,
					Suggestion:       fmt.Sprintf("Consider scaling up %s node group to utilize this %s", c.InstanceFamily, c.Type),
				})
			}

			// Check expiring — use the tightest matching threshold so
			// "5 days left" reports correctly, not as the widest window.
			if !c.ExpiresAt.IsZero() {
				matched := false
				for _, days := range expiryWarningDays {
					if time.Until(c.ExpiresAt) < time.Duration(days)*24*time.Hour {
						matched = true
					}
				}
				if matched {
					expiring = append(expiring, koptv1alpha1.ExpiringCommitment{
						CommitmentID:    c.ID,
						Type:            c.Type,
						ExpiresIn:       fmt.Sprintf("%dd", int(time.Until(c.ExpiresAt).Hours()/24)),
						MonthlyValueUSD: c.HourlyCostUSD * cost.HoursPerMonth,
					})
				}
			}
		}
	}

	avgUtil := 0.0
	if activeCount > 0 {
		avgUtil = totalUtil / float64(activeCount)
	}

	report.Status.LastUpdated = metav1.Time{Time: time.Now()}
	report.Status.TotalCommitments = len(commitments)
	report.Status.TotalMonthlyCommitmentCostUSD = totalCost
	report.Status.AvgUtilizationPct = avgUtil
	report.Status.Commitments = statuses
	report.Status.Underutilized = underutilized
	report.Status.ExpiringSoon = expiring

	return r.client.Status().Update(ctx, report)
}

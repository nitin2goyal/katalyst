package storage

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/koptimizer/koptimizer/internal/config"
	intmetrics "github.com/koptimizer/koptimizer/internal/metrics"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// StoragePricing holds per-GB-month cost by storage class.
var StoragePricing = map[string]float64{
	"gp3":        0.08,  // AWS EBS gp3
	"gp2":        0.10,  // AWS EBS gp2
	"io1":        0.125, // AWS EBS io1
	"io2":        0.125, // AWS EBS io2
	"st1":        0.045, // AWS EBS st1
	"sc1":        0.015, // AWS EBS sc1
	"standard":   0.10,  // default
	"pd-standard": 0.04, // GCP
	"pd-ssd":     0.17,  // GCP
	"pd-balanced": 0.10, // GCP
	"managed-premium": 0.135, // Azure Premium SSD
	"managed-standard": 0.05, // Azure Standard HDD
}

// Controller monitors PersistentVolumeClaims for overprovisioning, unused
// volumes, and storage cost tracking.
type Controller struct {
	client client.Client
	config *config.Config
}

func NewController(mgr ctrl.Manager, cfg *config.Config) *Controller {
	return &Controller{
		client: mgr.GetClient(),
		config: cfg,
	}
}

func (c *Controller) SetupWithManager(mgr ctrl.Manager) error {
	return mgr.Add(c)
}

// Start implements manager.Runnable.
func (c *Controller) Start(ctx context.Context) error {
	c.run(ctx)
	return nil
}

func (c *Controller) Name() string { return "storage-monitor" }

func (c *Controller) Analyze(ctx context.Context, _ *optimizer.ClusterSnapshot) ([]optimizer.Recommendation, error) {
	var recs []optimizer.Recommendation

	// List all PVCs
	pvcList := &corev1.PersistentVolumeClaimList{}
	if err := c.client.List(ctx, pvcList); err != nil {
		return nil, fmt.Errorf("listing PVCs: %w", err)
	}

	// List all PVs
	pvList := &corev1.PersistentVolumeList{}
	if err := c.client.List(ctx, pvList); err != nil {
		return nil, fmt.Errorf("listing PVs: %w", err)
	}

	// Build set of PVC names that are mounted by running pods.
	// Use pagination to avoid OOM on large clusters.
	mountedPVCs := make(map[string]bool) // "namespace/name" -> true
	podOpts := &client.ListOptions{Limit: 500}
	for {
		podList := &corev1.PodList{}
		if err := c.client.List(ctx, podList, podOpts); err != nil {
			return nil, fmt.Errorf("listing pods: %w", err)
		}
		for i := range podList.Items {
			pod := &podList.Items[i]
			if pod.Status.Phase != corev1.PodRunning && pod.Status.Phase != corev1.PodPending {
				continue
			}
			for _, vol := range pod.Spec.Volumes {
				if vol.PersistentVolumeClaim != nil {
					key := fmt.Sprintf("%s/%s", pod.Namespace, vol.PersistentVolumeClaim.ClaimName)
					mountedPVCs[key] = true
				}
			}
		}
		if podList.Continue == "" {
			break
		}
		podOpts.Continue = podList.Continue
	}

	var totalStorageCostMonthly float64
	overprovisionedCount := 0
	unusedCount := 0
	unusedPVCNames := make(map[string]bool) // Track unused PVCs to avoid double-counting with overprovisioned

	// Build PV capacity map for bound PVCs
	pvCapacity := make(map[string]resource.Quantity)
	for i := range pvList.Items {
		pv := &pvList.Items[i]
		if cap, ok := pv.Spec.Capacity[corev1.ResourceStorage]; ok {
			pvCapacity[pv.Name] = cap
		}
	}

	for i := range pvcList.Items {
		pvc := &pvcList.Items[i]

		// Calculate cost
		requestedStorage := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
		requestedGB := float64(requestedStorage.Value()) / (1024 * 1024 * 1024)
		storageClass := ""
		if pvc.Spec.StorageClassName != nil {
			storageClass = *pvc.Spec.StorageClassName
		}
		pricePerGBMonth := StoragePricing["standard"]
		if p, ok := StoragePricing[storageClass]; ok {
			pricePerGBMonth = p
		}
		monthlyCost := requestedGB * pricePerGBMonth
		totalStorageCostMonthly += monthlyCost

		pvcKey := fmt.Sprintf("%s/%s", pvc.Namespace, pvc.Name)

		// Check if unused (bound but not mounted by any running pod)
		if pvc.Status.Phase == corev1.ClaimBound && !mountedPVCs[pvcKey] {
			unusedCount++
			unusedPVCNames[pvcKey] = true
			recs = append(recs, optimizer.Recommendation{
				ID:             fmt.Sprintf("storage-unused-%s-%s", pvc.Namespace, pvc.Name),
				Type:           optimizer.RecommendationStorage,
				Priority:       optimizer.PriorityLow,
				AutoExecutable: false,
				TargetKind:     "PersistentVolumeClaim",
				TargetName:     pvc.Name,
				TargetNamespace: pvc.Namespace,
				Summary:        fmt.Sprintf("PVC %s/%s (%s) is bound but not mounted by any running pod", pvc.Namespace, pvc.Name, requestedStorage.String()),
				ActionSteps: []string{
					fmt.Sprintf("Verify PVC %s/%s is no longer needed", pvc.Namespace, pvc.Name),
					"If unused, delete the PVC to release storage and stop charges",
					fmt.Sprintf("Current cost: $%.2f/month", monthlyCost),
				},
				EstimatedSaving: optimizer.SavingEstimate{
					MonthlySavingsUSD: monthlyCost,
					AnnualSavingsUSD:  monthlyCost * 12,
					Currency:          "USD",
				},
				Details: map[string]string{
					"action":       "delete-unused-pvc",
					"namespace":    pvc.Namespace,
					"pvcName":      pvc.Name,
					"storageClass": storageClass,
					"requestedGB":  fmt.Sprintf("%.1f", requestedGB),
				},
			})
		}

		// Check for overprovisioning (PV capacity >> PVC request)
		// Skip PVCs already flagged as unused to avoid double-counting savings.
		if pvc.Status.Phase == corev1.ClaimBound && pvc.Spec.VolumeName != "" && !unusedPVCNames[pvcKey] {
			if pvCap, ok := pvCapacity[pvc.Spec.VolumeName]; ok {
				pvGB := float64(pvCap.Value()) / (1024 * 1024 * 1024)
				if requestedGB > 0 && pvGB > requestedGB*1.5 {
					// PV is significantly larger than requested
					wastedGB := pvGB - requestedGB
					wastedCost := wastedGB * pricePerGBMonth
					overprovisionedCount++

					recs = append(recs, optimizer.Recommendation{
						ID:             fmt.Sprintf("storage-oversized-%s-%s", pvc.Namespace, pvc.Name),
						Type:           optimizer.RecommendationStorage,
						Priority:       optimizer.PriorityLow,
						AutoExecutable: false,
						TargetKind:     "PersistentVolumeClaim",
						TargetName:     pvc.Name,
						TargetNamespace: pvc.Namespace,
						Summary:        fmt.Sprintf("PV for %s/%s is %.0fGi but only %.0fGi requested (%.0fGi wasted)", pvc.Namespace, pvc.Name, pvGB, requestedGB, wastedGB),
						ActionSteps: []string{
							fmt.Sprintf("Consider resizing PVC %s/%s to match actual needs", pvc.Namespace, pvc.Name),
							"Check if StorageClass supports volume expansion",
							fmt.Sprintf("Potential savings: $%.2f/month", wastedCost),
						},
						EstimatedSaving: optimizer.SavingEstimate{
							MonthlySavingsUSD: wastedCost,
							AnnualSavingsUSD:  wastedCost * 12,
							Currency:          "USD",
						},
						Details: map[string]string{
							"action":      "resize-pvc",
							"namespace":   pvc.Namespace,
							"pvcName":     pvc.Name,
							"requestedGB": fmt.Sprintf("%.1f", requestedGB),
							"actualGB":    fmt.Sprintf("%.1f", pvGB),
						},
					})
				}
			}
		}

		// Cloud-aware storage class upgrade recommendations
		type storageUpgrade struct {
			from, to string
			cloud    string
		}
		upgrades := []storageUpgrade{
			{"gp2", "gp3", "aws"},
			{"io1", "gp3", "aws"},
			{"pd-standard", "pd-balanced", "gcp"},
			{"pd-ssd", "pd-balanced", "gcp"},
			{"managed-standard", "managed-premium", "azure"}, // Only if performance needed
		}
		for _, upgrade := range upgrades {
			if storageClass == upgrade.from && requestedGB >= 100 {
				fromPrice := StoragePricing[upgrade.from]
				toPrice := StoragePricing[upgrade.to]
				if toPrice < fromPrice {
					savings := (fromPrice - toPrice) * requestedGB
					recs = append(recs, optimizer.Recommendation{
						ID:              fmt.Sprintf("storage-class-%s-%s", pvc.Namespace, pvc.Name),
						Type:            optimizer.RecommendationStorage,
						Priority:        optimizer.PriorityLow,
						AutoExecutable:  false,
						TargetKind:      "PersistentVolumeClaim",
						TargetName:      pvc.Name,
						TargetNamespace: pvc.Namespace,
						Summary:         fmt.Sprintf("PVC %s/%s uses %s (%.0fGi) — migrate to %s to save $%.2f/month", pvc.Namespace, pvc.Name, upgrade.from, requestedGB, upgrade.to, savings),
						ActionSteps: []string{
							"Create snapshot of the existing volume",
							fmt.Sprintf("Create a new %s volume from the snapshot", upgrade.to),
							"Update PVC to use the new volume",
						},
						EstimatedSaving: optimizer.SavingEstimate{
							MonthlySavingsUSD: savings,
							AnnualSavingsUSD:  savings * 12,
							Currency:          "USD",
						},
						Details: map[string]string{
							"action":    "upgrade-storage-class",
							"namespace": pvc.Namespace,
							"pvcName":   pvc.Name,
							"fromClass": upgrade.from,
							"toClass":   upgrade.to,
						},
					})
					break // only recommend one upgrade per PVC
				}
			}
		}
	}

	intmetrics.StoragePVCCount.Set(float64(len(pvcList.Items)))
	intmetrics.StorageOverprovisionedPVCs.Set(float64(overprovisionedCount))
	intmetrics.StorageUnusedPVCs.Set(float64(unusedCount))
	intmetrics.StorageMonthlyCostUSD.Set(totalStorageCostMonthly)

	return recs, nil
}

func (c *Controller) Execute(ctx context.Context, rec optimizer.Recommendation) error {
	// Storage changes (delete PVC, resize, class migration) are dangerous
	// and require manual confirmation. All recommendations are non-auto-executable.
	return nil
}

func (c *Controller) run(ctx context.Context) {
	logger := log.FromContext(ctx).WithName("storage")
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			recs, err := c.Analyze(ctx, nil)
			if err != nil {
				logger.Error(err, "Storage analysis failed")
				continue
			}
			_ = recs // Recommendations are emitted via CRD or API
		case <-ctx.Done():
			return
		}
	}
}

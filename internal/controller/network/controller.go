package network

import (
	"context"
	"fmt"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/koptimizer/koptimizer/internal/config"
	intmetrics "github.com/koptimizer/koptimizer/internal/metrics"
	"github.com/koptimizer/koptimizer/internal/state"
	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
	"github.com/koptimizer/koptimizer/pkg/cost"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// Controller monitors cross-AZ network traffic patterns and identifies
// opportunities to reduce egress costs by co-locating communicating services.
type Controller struct {
	client   client.Client
	provider cloudprovider.CloudProvider
	state    *state.ClusterState
	config   *config.Config
}

func NewController(mgr ctrl.Manager, provider cloudprovider.CloudProvider, st *state.ClusterState, cfg *config.Config) *Controller {
	return &Controller{
		client:   mgr.GetClient(),
		provider: provider,
		state:    st,
		config:   cfg,
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

func (c *Controller) Name() string { return "network-monitor" }

func (c *Controller) Analyze(ctx context.Context, snapshot *optimizer.ClusterSnapshot) ([]optimizer.Recommendation, error) {
	var recs []optimizer.Recommendation

	// Build node-to-AZ mapping
	nodeAZ := make(map[string]string)
	for _, node := range snapshot.Nodes {
		if node.Node.Labels != nil {
			if az, ok := node.Node.Labels["topology.kubernetes.io/zone"]; ok {
				nodeAZ[node.Node.Name] = az
			} else if az, ok := node.Node.Labels["failure-domain.beta.kubernetes.io/zone"]; ok {
				nodeAZ[node.Node.Name] = az
			}
		}
	}

	// Detect services with pods spread across multiple AZs
	// Group pods by owner (deployment/statefulset)
	type workloadAZSpread struct {
		name      string
		namespace string
		kind      string
		azs       map[string]int // az -> pod count
		totalPods int
	}

	workloads := make(map[string]*workloadAZSpread) // "namespace/kind/name" -> spread

	for _, pod := range snapshot.Pods {
		if pod.Pod.Spec.NodeName == "" {
			continue
		}
		az, ok := nodeAZ[pod.Pod.Spec.NodeName]
		if !ok {
			continue
		}

		key := fmt.Sprintf("%s/%s/%s", pod.Pod.Namespace, pod.OwnerKind, pod.OwnerName)
		if pod.OwnerKind == "" || pod.OwnerName == "" {
			continue
		}

		wl, ok := workloads[key]
		if !ok {
			wl = &workloadAZSpread{
				name:      pod.OwnerName,
				namespace: pod.Pod.Namespace,
				kind:      pod.OwnerKind,
				azs:       make(map[string]int),
			}
			workloads[key] = wl
		}
		wl.azs[az]++
		wl.totalPods++
	}

	// Identify pairs of services in the same namespace that communicate
	// (heuristic: services with matching label selectors in the same namespace
	// and pods in different AZs). For a full solution, eBPF flow data is needed.
	// Here we flag workloads with multi-AZ spread that may benefit from
	// topology-aware routing.
	crossAZWorkloads := 0
	for _, wl := range workloads {
		if len(wl.azs) > 1 && wl.totalPods >= 3 {
			crossAZWorkloads++

			// Estimate cross-AZ cost: assume 10% of pod traffic is inter-pod within
			// the same service and flows cross-AZ proportional to AZ imbalance
			estimatedCrossAZGB := float64(wl.totalPods) * 0.5 * cost.HoursPerMonth // rough: 0.5 GB/hr per pod
			estimatedMonthlyCost := estimatedCrossAZGB * c.config.NetworkMonitor.CrossAZCostPerGBUSD

			recs = append(recs, optimizer.Recommendation{
				ID:             fmt.Sprintf("network-crossaz-%s-%s-%s", wl.namespace, wl.kind, wl.name),
				Type:           optimizer.RecommendationNetwork,
				Priority:       optimizer.PriorityLow,
				AutoExecutable: false,
				TargetKind:     wl.kind,
				TargetName:     wl.name,
				TargetNamespace: wl.namespace,
				Summary:        fmt.Sprintf("%s %s/%s has pods in %d AZs — enable topology-aware routing to reduce cross-AZ traffic", wl.kind, wl.namespace, wl.name, len(wl.azs)),
				ActionSteps: []string{
					fmt.Sprintf("Add topologySpreadConstraints or enable topology-aware routing for %s/%s", wl.namespace, wl.name),
					"Set service.kubernetes.io/topology-mode=Auto on associated Service",
					"Or use topologyKeys in Service spec for zone-local traffic preference",
					fmt.Sprintf("Estimated cross-AZ cost: $%.2f/month", estimatedMonthlyCost),
				},
				EstimatedSaving: optimizer.SavingEstimate{
					MonthlySavingsUSD: estimatedMonthlyCost * 0.7, // 70% reduction possible
					AnnualSavingsUSD:  estimatedMonthlyCost * 0.7 * 12,
					Currency:          "USD",
				},
				Details: map[string]string{
					"action":    "enable-topology-routing",
					"namespace": wl.namespace,
					"workload":  wl.name,
					"azCount":   fmt.Sprintf("%d", len(wl.azs)),
				},
			})
		}
	}

	// Count unique AZs
	azSet := make(map[string]bool)
	for _, az := range nodeAZ {
		azSet[az] = true
	}

	if len(azSet) > 1 {
		// Rough estimate of cross-AZ monthly cost
		totalNodes := len(snapshot.Nodes)
		estimatedCrossAZMonthly := float64(totalNodes) * 5.0 * cost.HoursPerMonth * c.config.NetworkMonitor.CrossAZCostPerGBUSD // 5GB/hr per node estimate
		intmetrics.NetworkCrossAZCostUSD.Set(estimatedCrossAZMonthly)
	}

	return recs, nil
}

func (c *Controller) Execute(ctx context.Context, rec optimizer.Recommendation) error {
	// Network optimizations are topology changes that require manual review.
	return nil
}

func (c *Controller) run(ctx context.Context) {
	logger := log.FromContext(ctx).WithName("network")
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			snapshot := c.state.Snapshot()
			if _, err := c.Analyze(ctx, snapshot); err != nil {
				logger.Error(err, "Network analysis failed")
			}
		case <-ctx.Done():
			return
		}
	}
}

package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// Cluster-level metrics
	ClusterNodeCount = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "koptimizer",
		Name:      "cluster_node_count",
		Help:      "Total number of nodes in the cluster",
	})

	ClusterMonthlyCostUSD = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "koptimizer",
		Name:      "cluster_monthly_cost_usd",
		Help:      "Estimated monthly cost of the cluster in USD",
	})

	ClusterPotentialSavingsUSD = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "koptimizer",
		Name:      "cluster_potential_savings_usd",
		Help:      "Potential monthly savings in USD",
	})

	// Node group metrics
	NodeGroupDesiredCount = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "koptimizer",
		Name:      "nodegroup_desired_count",
		Help:      "Desired node count per node group",
	}, []string{"nodegroup", "instance_type", "family"})

	NodeGroupCPUUtilization = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "koptimizer",
		Name:      "nodegroup_cpu_utilization_pct",
		Help:      "CPU utilization percentage per node group",
	}, []string{"nodegroup"})

	NodeGroupMemoryUtilization = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "koptimizer",
		Name:      "nodegroup_memory_utilization_pct",
		Help:      "Memory utilization percentage per node group",
	}, []string{"nodegroup"})

	// Recommendation metrics
	RecommendationsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "koptimizer",
		Name:      "recommendations_total",
		Help:      "Total number of recommendations generated",
	}, []string{"type", "priority"})

	RecommendationsExecuted = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "koptimizer",
		Name:      "recommendations_executed_total",
		Help:      "Total number of recommendations auto-executed",
	}, []string{"type"})

	// AI Gate metrics
	AIGateValidations = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "koptimizer",
		Name:      "aigate_validations_total",
		Help:      "Total AI Gate validations",
	}, []string{"result"}) // "approved", "rejected", "error"

	AIGateLatency = promauto.NewHistogram(prometheus.HistogramOpts{
		Namespace: "koptimizer",
		Name:      "aigate_latency_seconds",
		Help:      "AI Gate validation latency",
		Buckets:   prometheus.DefBuckets,
	})

	// Family lock metrics
	FamilyLockBlocked = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "koptimizer",
		Name:      "familylock_blocked_total",
		Help:      "Total operations blocked by family lock",
	}, []string{"action"})

	// Commitment metrics
	CommitmentUtilization = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "koptimizer",
		Name:      "commitment_utilization_pct",
		Help:      "Utilization percentage per commitment",
	}, []string{"id", "type", "instance_family"})

	CommitmentWastedUSD = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "koptimizer",
		Name:      "commitment_wasted_monthly_usd",
		Help:      "Monthly wasted cost per underutilized commitment",
	}, []string{"id", "type"})

	// Evictor metrics
	EvictionsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "koptimizer",
		Name:      "evictions_total",
		Help:      "Total pod evictions for consolidation",
	})

	NodesConsolidated = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "koptimizer",
		Name:      "nodes_consolidated_total",
		Help:      "Total nodes consolidated (drained)",
	})

	// GPU metrics
	GPUNodesIdle = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "koptimizer",
		Name:      "gpu_nodes_idle",
		Help:      "Number of GPU nodes currently idle",
	})

	GPUNodesCPUFallback = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "koptimizer",
		Name:      "gpu_nodes_cpu_fallback",
		Help:      "Number of GPU nodes serving CPU workloads as fallback",
	})

	GPUNodesCPUScavenging = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "koptimizer",
		Name:      "gpu_nodes_cpu_scavenging",
		Help:      "Number of GPU nodes with active CPU scavenging",
	})

	// Spot instance metrics
	SpotNodesTotal = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "koptimizer",
		Name:      "spot_nodes_total",
		Help:      "Total number of spot instance nodes",
	})

	SpotSavingsUSD = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "koptimizer",
		Name:      "spot_savings_monthly_usd",
		Help:      "Monthly savings from spot instances vs on-demand",
	})

	SpotInterruptions = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "koptimizer",
		Name:      "spot_interruptions_total",
		Help:      "Total spot instance interruptions handled",
	})

	SpotFallbacks = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "koptimizer",
		Name:      "spot_fallbacks_total",
		Help:      "Total fallbacks from spot to on-demand",
	})

	// Hibernation metrics
	HibernatedNodeGroups = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "koptimizer",
		Name:      "hibernated_nodegroups",
		Help:      "Number of currently hibernated node groups",
	})

	HibernationSavingsUSD = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "koptimizer",
		Name:      "hibernation_savings_monthly_usd",
		Help:      "Estimated monthly savings from cluster hibernation",
	})

	// Storage metrics
	StoragePVCCount = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "koptimizer",
		Name:      "storage_pvc_count",
		Help:      "Total number of PersistentVolumeClaims",
	})

	StorageOverprovisionedPVCs = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "koptimizer",
		Name:      "storage_overprovisioned_pvcs",
		Help:      "Number of overprovisioned PVCs",
	})

	StorageUnusedPVCs = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "koptimizer",
		Name:      "storage_unused_pvcs",
		Help:      "Number of unused PVCs",
	})

	StorageMonthlyCostUSD = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "koptimizer",
		Name:      "storage_monthly_cost_usd",
		Help:      "Estimated monthly cost of persistent storage",
	})

	// Network metrics
	NetworkCrossAZBytesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "koptimizer",
		Name:      "network_cross_az_bytes_total",
		Help:      "Total cross-AZ network traffic in bytes",
	}, []string{"source_az", "dest_az"})

	NetworkCrossAZCostUSD = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "koptimizer",
		Name:      "network_cross_az_monthly_cost_usd",
		Help:      "Estimated monthly cost of cross-AZ traffic",
	})

	// Pricing metrics
	PricingFallbackTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "koptimizer",
		Name:      "pricing_fallback_total",
		Help:      "Total times fallback (hardcoded) pricing was used instead of live API",
	}, []string{"provider", "region"})

	PricingFallbackActive = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "koptimizer",
		Name:      "pricing_fallback_active",
		Help:      "Set to 1 when fallback pricing is currently in use for a provider/region",
	}, []string{"provider", "region"})

	PricingLastLiveUpdate = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "koptimizer",
		Name:      "pricing_last_live_update_timestamp",
		Help:      "Unix timestamp of last successful live pricing API update",
	}, []string{"provider", "region"})

	// Alert metrics
	AlertsFired = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "koptimizer",
		Name:      "alerts_fired_total",
		Help:      "Total alerts fired",
	}, []string{"type", "severity"})
)

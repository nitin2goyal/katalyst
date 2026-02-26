package cloudprovider

import (
	"context"
	"time"
	corev1 "k8s.io/api/core/v1"
)

// CloudProvider defines the interface for cloud-specific operations.
type CloudProvider interface {
	Name() string

	// Pricing & Catalog (READ)
	GetInstanceTypes(ctx context.Context, region string) ([]*InstanceType, error)
	GetCurrentPricing(ctx context.Context, region string) (*PricingInfo, error)
	GetNodeCost(ctx context.Context, node *corev1.Node) (*NodeCost, error)
	GetGPUInstanceTypes(ctx context.Context, region string) ([]*GPUInstanceType, error)

	// Instance Identification (READ)
	GetNodeInstanceType(ctx context.Context, node *corev1.Node) (string, error)
	GetNodeRegion(ctx context.Context, node *corev1.Node) (string, error)
	GetNodeZone(ctx context.Context, node *corev1.Node) (string, error)

	// Node Group Operations (CONSTRAINED WRITE)
	DiscoverNodeGroups(ctx context.Context) ([]*NodeGroup, error)
	GetNodeGroup(ctx context.Context, id string) (*NodeGroup, error)
	ScaleNodeGroup(ctx context.Context, id string, desiredCount int) error
	SetNodeGroupMinCount(ctx context.Context, id string, minCount int) error
	SetNodeGroupMaxCount(ctx context.Context, id string, maxCount int) error
	GetFamilySizes(ctx context.Context, instanceType string) ([]*InstanceType, error)

	// Commitment Information (READ)
	GetReservedInstances(ctx context.Context) ([]*Commitment, error)
	GetSavingsPlans(ctx context.Context) ([]*Commitment, error)
	GetCommittedUseDiscounts(ctx context.Context) ([]*Commitment, error)
	GetReservations(ctx context.Context) ([]*Commitment, error)
}

type InstanceType struct {
	Name         string
	Family       string
	CPUCores     int
	MemoryMiB    int
	GPUs         int
	GPUType      string
	PricePerHour float64
	Architecture string // "amd64", "arm64"
}

type GPUInstanceType struct {
	InstanceType
	GPUMemoryMiB int
	GPUModel     string
}

type PricingInfo struct {
	Region    string
	Prices    map[string]float64 // instanceType -> hourly price
	UpdatedAt time.Time
}

type NodeCost struct {
	NodeName       string
	InstanceType   string
	HourlyCostUSD  float64
	MonthlyCostUSD float64
	IsSpot         bool
	SpotDiscount   float64
}

type NodeGroup struct {
	ID             string
	Name           string
	InstanceType   string
	InstanceFamily string
	CurrentCount   int
	MinCount       int
	MaxCount       int
	DesiredCount   int
	Zone           string
	Region         string
	Labels         map[string]string
	Taints         []corev1.Taint
	Lifecycle      string   // "on-demand", "spot", "mixed"
	SpotPercentage int      // 0-100, for mixed instance groups
	InstanceTypes  []string // Multiple instance types for spot diversity
	DiskType       string   // e.g. "pd-balanced", "hyperdisk-balanced", "gp3"
	DiskSizeGB     int      // boot disk size in GiB
}

// SpotInstanceInfo provides spot-specific pricing and reliability data.
type SpotInstanceInfo struct {
	InstanceType         string
	AvailabilityZone     string
	SpotPrice            float64
	OnDemandPrice        float64
	SavingsPercent       float64
	InterruptionFreqPct  float64 // estimated interruption frequency
	AvgLifetimeMinutes   int     // average spot instance lifetime
}

// SpotProvider extends CloudProvider with spot-specific operations.
type SpotProvider interface {
	// GetSpotPricing returns current spot prices for the given instance types in a region.
	GetSpotPricing(ctx context.Context, region string, instanceTypes []string) ([]*SpotInstanceInfo, error)
	// GetSpotInterruptionRate returns historical interruption rates for instance types.
	GetSpotInterruptionRate(ctx context.Context, region string, instanceTypes []string) (map[string]float64, error)
}

// BackgroundRefresher is implemented by providers that support proactive
// cache refresh to avoid latency spikes on first request after cache expiry.
type BackgroundRefresher interface {
	StartBackgroundRefresh(ctx context.Context)
}

// SpotDiscountEstimator provides per-provider, per-family spot discount estimates.
// This is a unified interface so all code paths use the same discount strategy
// instead of inconsistent hardcoded values.
type SpotDiscountEstimator interface {
	// EstimateSpotDiscount returns the estimated spot discount fraction (0-1) for
	// the given instance type. E.g., 0.70 means spot is ~70% cheaper than on-demand,
	// so the spot price is approximately on-demand * (1 - 0.70) = on-demand * 0.30.
	EstimateSpotDiscount(instanceType string) float64
}

// FallbackPricer estimates node cost from actual CPU/memory capacity when the
// normal pricing API path is unavailable (e.g., Compute Engine machine types
// API or Cloud Billing Catalog API unreachable).
type FallbackPricer interface {
	EstimatePriceFromCapacity(instanceType, region string, cpuMilli int64, memBytes int64) float64
}

type Commitment struct {
	ID              string
	Type            string  // "reserved-instance", "savings-plan", "cud", "reservation"
	InstanceFamily  string
	InstanceType    string
	Region          string
	Count           int
	HourlyCostUSD   float64
	OnDemandCostUSD float64
	UtilizationPct  float64
	ExpiresAt       time.Time
	Status          string // "active", "expired"
}

// IsSpotNode returns true if the node is a spot/preemptible instance.
// Works across all cloud providers by checking provider-specific labels.
func IsSpotNode(node *corev1.Node) bool {
	if node.Labels == nil {
		return false
	}
	// AWS
	if lc, ok := node.Labels["node.kubernetes.io/lifecycle"]; ok && lc == "spot" {
		return true
	}
	// GCP Spot VMs
	if v, ok := node.Labels["cloud.google.com/gke-spot"]; ok && v == "true" {
		return true
	}
	// GCP Preemptible VMs (legacy)
	if v, ok := node.Labels["cloud.google.com/gke-preemptible"]; ok && v == "true" {
		return true
	}
	// Azure Spot VMs
	if v, ok := node.Labels["kubernetes.azure.com/scalesetpriority"]; ok && v == "spot" {
		return true
	}
	return false
}

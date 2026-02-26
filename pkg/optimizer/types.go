package optimizer

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
)

// Optimizer defines the interface for all optimization controllers.
type Optimizer interface {
	Name() string
	Analyze(ctx context.Context, state *ClusterSnapshot) ([]Recommendation, error)
	Execute(ctx context.Context, rec Recommendation) error
}

type RecommendationType string

const (
	RecommendationNodeScale       RecommendationType = "node-scale"
	RecommendationNodeGroupAdjust RecommendationType = "node-group-adjust"
	RecommendationNodeGroupDelete RecommendationType = "node-group-delete"
	RecommendationPodRightsize    RecommendationType = "pod-rightsize"
	RecommendationWorkloadScale   RecommendationType = "workload-scale"
	RecommendationEviction        RecommendationType = "eviction"
	RecommendationRebalance       RecommendationType = "rebalance"
	RecommendationGPUOptimize     RecommendationType = "gpu-optimize"
	RecommendationSpotOptimize    RecommendationType = "spot-optimize"
	RecommendationHibernation     RecommendationType = "hibernation"
	RecommendationStorage         RecommendationType = "storage"
	RecommendationNetwork         RecommendationType = "network"
	RecommendationCostAnomaly     RecommendationType = "cost-anomaly"
)

type Priority string

const (
	PriorityCritical Priority = "critical"
	PriorityHigh     Priority = "high"
	PriorityMedium   Priority = "medium"
	PriorityLow      Priority = "low"
)

type Recommendation struct {
	ID              string
	Type            RecommendationType
	Priority        Priority
	AutoExecutable  bool
	RequiresAIGate  bool
	AIGateResult    *AIGateResult
	TargetKind      string
	TargetName      string
	TargetNamespace string
	Summary         string
	ActionSteps     []string
	EstimatedSaving SavingEstimate
	EstimatedImpact ImpactEstimate
	Details         map[string]string
	CreatedAt       time.Time
}

type AIGateResult struct {
	Approved    bool
	Confidence  float64
	Reasoning   string
	Warnings    []string
	ValidatedAt time.Time
}

type SavingEstimate struct {
	MonthlySavingsUSD float64
	AnnualSavingsUSD  float64
	Currency          string
}

type ImpactEstimate struct {
	MonthlyCostChangeUSD float64
	NodesAffected        int
	PodsAffected         int
	RiskLevel            string
}

// ClusterSnapshot is a point-in-time view of the cluster state.
type ClusterSnapshot struct {
	Nodes      []NodeInfo
	Pods       []PodInfo
	NodeGroups []*cloudprovider.NodeGroup
	Timestamp  time.Time
}

type NodeInfo struct {
	Node            *corev1.Node
	Pods            []*corev1.Pod
	InstanceType    string
	InstanceFamily  string
	CPUCapacity     int64 // millicores
	MemoryCapacity  int64 // bytes
	CPURequested    int64
	MemoryRequested int64
	CPUUsed         int64
	MemoryUsed      int64
	GPUs            int
	GPUsUsed        int
	HourlyCostUSD   float64
	IsGPUNode       bool
	NodeGroup       string
}

type PodInfo struct {
	Pod           *corev1.Pod
	CPURequest    int64
	MemoryRequest int64
	CPUUsage      int64
	MemoryUsage   int64
	CPULimit      int64
	MemoryLimit   int64
	OwnerKind     string
	OwnerName     string
	IsGPUWorkload bool
	GPURequest    int
	ReplicaCount  int // Number of replicas for the owning workload (0 = unknown)
}

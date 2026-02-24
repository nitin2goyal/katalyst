package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// OptimizerConfigSpec defines the desired state of OptimizerConfig.
type OptimizerConfigSpec struct {
	// Mode determines the operational mode of the optimizer.
	// +kubebuilder:validation:Enum=monitor;recommend;active
	// +kubebuilder:default=monitor
	Mode string `json:"mode"`

	// CloudProvider is the cloud provider to use (aws, gcp, azure).
	// +kubebuilder:validation:Enum=aws;gcp;azure
	CloudProvider string `json:"cloudProvider"`

	// Region is the cloud provider region.
	Region string `json:"region"`

	// ReconcileInterval is the interval between reconciliation loops (e.g. "5m", "1h").
	// +kubebuilder:default="5m"
	ReconcileInterval string `json:"reconcileInterval,omitempty"`

	// CostMonitor holds configuration for the cost monitor controller.
	// +optional
	CostMonitor CostMonitorConfig `json:"costMonitor,omitempty"`

	// NodeAutoscaler holds configuration for the node autoscaler controller.
	// +optional
	NodeAutoscaler NodeAutoscalerConfig `json:"nodeAutoscaler,omitempty"`

	// NodeGroupManager holds configuration for the node group manager controller.
	// +optional
	NodeGroupManager NodeGroupManagerConfig `json:"nodegroupManager,omitempty"`

	// Rightsizer holds configuration for the rightsizer controller.
	// +optional
	Rightsizer RightsizerConfig `json:"rightsizer,omitempty"`

	// WorkloadScaler holds configuration for the workload scaler controller.
	// +optional
	WorkloadScaler WorkloadScalerConfig `json:"workloadScaler,omitempty"`

	// Evictor holds configuration for the evictor controller.
	// +optional
	Evictor EvictorConfig `json:"evictor,omitempty"`

	// Rebalancer holds configuration for the rebalancer controller.
	// +optional
	Rebalancer RebalancerConfig `json:"rebalancer,omitempty"`

	// GPU holds configuration for GPU optimization.
	// +optional
	GPU GPUConfig `json:"gpu,omitempty"`

	// Commitments holds configuration for commitment (RI/SP) management.
	// +optional
	Commitments CommitmentsConfig `json:"commitments,omitempty"`

	// AIGate holds configuration for the AI gate that validates recommendations.
	// +optional
	AIGate AIGateConfig `json:"aiGate,omitempty"`
}

// CostMonitorConfig holds configuration for the cost monitor controller.
type CostMonitorConfig struct {
	// Enabled determines whether the cost monitor is active.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled,omitempty"`

	// Interval is the polling interval for cost data (e.g. "1h").
	// +kubebuilder:default="1h"
	Interval string `json:"interval,omitempty"`
}

// NodeAutoscalerConfig holds configuration for the node autoscaler controller.
type NodeAutoscalerConfig struct {
	// Enabled determines whether the node autoscaler is active.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled,omitempty"`

	// MinNodes is the minimum number of nodes to maintain.
	// +kubebuilder:default=1
	MinNodes int `json:"minNodes,omitempty"`

	// MaxNodes is the maximum number of nodes allowed.
	// +kubebuilder:default=100
	MaxNodes int `json:"maxNodes,omitempty"`

	// ScaleDownUtilizationThreshold is the utilization below which nodes are candidates for scale-down.
	// +kubebuilder:default="0.5"
	ScaleDownUtilizationThreshold string `json:"scaleDownUtilizationThreshold,omitempty"`
}

// NodeGroupManagerConfig holds configuration for the node group manager controller.
type NodeGroupManagerConfig struct {
	// Enabled determines whether the node group manager is active.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled,omitempty"`
}

// RightsizerConfig holds configuration for the rightsizer controller.
type RightsizerConfig struct {
	// Enabled determines whether the rightsizer is active.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled,omitempty"`

	// LookbackDays is the number of days of historical data to consider.
	// +kubebuilder:default=7
	LookbackDays int `json:"lookbackDays,omitempty"`

	// CPUTargetUtilization is the target CPU utilization percentage.
	// +kubebuilder:default="0.7"
	CPUTargetUtilization string `json:"cpuTargetUtilization,omitempty"`

	// MemoryTargetUtilization is the target memory utilization percentage.
	// +kubebuilder:default="0.8"
	MemoryTargetUtilization string `json:"memoryTargetUtilization,omitempty"`
}

// WorkloadScalerConfig holds configuration for the workload scaler controller.
type WorkloadScalerConfig struct {
	// Enabled determines whether the workload scaler is active.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled,omitempty"`

	// MinReplicas is the minimum number of replicas.
	// +kubebuilder:default=1
	MinReplicas int `json:"minReplicas,omitempty"`

	// MaxReplicas is the maximum number of replicas.
	// +kubebuilder:default=100
	MaxReplicas int `json:"maxReplicas,omitempty"`
}

// EvictorConfig holds configuration for the evictor controller.
type EvictorConfig struct {
	// Enabled determines whether the evictor is active.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled,omitempty"`

	// MaxPodsPerEviction is the maximum number of pods to evict per cycle.
	// +kubebuilder:default=5
	MaxPodsPerEviction int `json:"maxPodsPerEviction,omitempty"`
}

// RebalancerConfig holds configuration for the rebalancer controller.
type RebalancerConfig struct {
	// Enabled determines whether the rebalancer is active.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled,omitempty"`

	// ImbalanceThreshold is the threshold above which rebalancing is triggered.
	// +kubebuilder:default="0.2"
	ImbalanceThreshold string `json:"imbalanceThreshold,omitempty"`
}

// GPUConfig holds configuration for GPU optimization.
type GPUConfig struct {
	// Enabled determines whether GPU optimization is active.
	// +kubebuilder:default=false
	Enabled bool `json:"enabled,omitempty"`

	// TimesharingEnabled enables GPU time-sharing recommendations.
	// +kubebuilder:default=false
	TimesharingEnabled bool `json:"timesharingEnabled,omitempty"`
}

// CommitmentsConfig holds configuration for commitment (RI/SP) management.
type CommitmentsConfig struct {
	// Enabled determines whether commitment management is active.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled,omitempty"`

	// UtilizationThreshold is the minimum utilization percentage for commitments.
	// +kubebuilder:default="0.8"
	UtilizationThreshold string `json:"utilizationThreshold,omitempty"`

	// ExpirationWarningDays is the number of days before expiration to start warning.
	// +kubebuilder:default=30
	ExpirationWarningDays int `json:"expirationWarningDays,omitempty"`
}

// AIGateConfig holds configuration for the AI gate that validates recommendations.
type AIGateConfig struct {
	// Enabled determines whether the AI gate is active.
	// +kubebuilder:default=false
	Enabled bool `json:"enabled,omitempty"`

	// Model is the AI model to use for validation (e.g. "claude-sonnet-4-20250514").
	Model string `json:"model,omitempty"`

	// Timeout is the maximum duration for an AI gate call (e.g. "30s").
	// +kubebuilder:default="30s"
	Timeout string `json:"timeout,omitempty"`

	// CostThresholdUSD is the minimum estimated cost impact to trigger AI gate review.
	// +kubebuilder:default=100
	CostThresholdUSD float64 `json:"costThresholdUSD,omitempty"`

	// ScaleThresholdPct is the minimum scale change percentage to trigger AI gate review.
	// +kubebuilder:default=20
	ScaleThresholdPct float64 `json:"scaleThresholdPct,omitempty"`
}

// OptimizerConfigStatus defines the observed state of OptimizerConfig.
type OptimizerConfigStatus struct {
	// Phase represents the current lifecycle phase of the optimizer.
	// +kubebuilder:validation:Enum=Initializing;Running;Paused;Error
	Phase string `json:"phase,omitempty"`

	// LastReconcile is the timestamp of the last successful reconciliation.
	// +optional
	LastReconcile metav1.Time `json:"lastReconcile,omitempty"`

	// ActiveOptimizers is the number of currently active optimizer controllers.
	ActiveOptimizers int `json:"activeOptimizers,omitempty"`

	// Message provides additional human-readable status information.
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Mode",type=string,JSONPath=`.spec.mode`
// +kubebuilder:printcolumn:name="Provider",type=string,JSONPath=`.spec.cloudProvider`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Active",type=integer,JSONPath=`.status.activeOptimizers`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// OptimizerConfig is the Schema for the optimizerconfigs API.
type OptimizerConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OptimizerConfigSpec   `json:"spec,omitempty"`
	Status OptimizerConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// OptimizerConfigList contains a list of OptimizerConfig.
type OptimizerConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OptimizerConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&OptimizerConfig{}, &OptimizerConfigList{})
}

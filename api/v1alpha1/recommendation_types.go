package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RecommendationSpec defines the desired state of Recommendation.
type RecommendationSpec struct {
	// Type is the category of the recommendation.
	// +kubebuilder:validation:Enum=node-scale;node-group-adjust;node-group-delete;pod-rightsize;workload-scale;eviction;rebalance;gpu-optimize
	Type string `json:"type"`

	// Priority indicates the urgency of the recommendation.
	// +kubebuilder:validation:Enum=critical;high;medium;low
	Priority string `json:"priority"`

	// TargetKind is the Kubernetes resource kind being targeted (e.g. Deployment, Node).
	TargetKind string `json:"targetKind"`

	// TargetName is the name of the targeted resource.
	TargetName string `json:"targetName"`

	// TargetNamespace is the namespace of the targeted resource.
	// +optional
	TargetNamespace string `json:"targetNamespace,omitempty"`

	// Summary is a human-readable summary of the recommendation.
	Summary string `json:"summary"`

	// ActionSteps is an ordered list of steps required to execute this recommendation.
	// +optional
	ActionSteps []string `json:"actionSteps,omitempty"`

	// AutoExecutable indicates whether this recommendation can be executed automatically.
	// +kubebuilder:default=false
	AutoExecutable bool `json:"autoExecutable,omitempty"`

	// RequiresAIGate indicates whether this recommendation must pass AI gate validation before execution.
	// +kubebuilder:default=false
	RequiresAIGate bool `json:"requiresAIGate,omitempty"`

	// EstimatedSaving is the estimated cost savings from executing this recommendation.
	// +optional
	EstimatedSaving SavingEstimate `json:"estimatedSaving,omitempty"`

	// EstimatedImpact is the estimated operational impact of executing this recommendation.
	// +optional
	EstimatedImpact ImpactEstimate `json:"estimatedImpact,omitempty"`

	// Details contains additional key-value metadata about the recommendation.
	// +optional
	Details map[string]string `json:"details,omitempty"`
}

// AIGateResult holds the result of an AI gate validation.
type AIGateResult struct {
	// Approved indicates whether the AI gate approved the recommendation.
	Approved bool `json:"approved"`

	// Confidence is the AI model's confidence score (0.0 to 1.0).
	Confidence float64 `json:"confidence"`

	// Reasoning is the AI model's explanation for its decision.
	Reasoning string `json:"reasoning"`

	// Warnings contains any warnings raised during AI gate validation.
	// +optional
	Warnings []string `json:"warnings,omitempty"`

	// ValidatedAt is the timestamp when the AI gate validation was performed.
	ValidatedAt metav1.Time `json:"validatedAt"`
}

// RecommendationStatus defines the observed state of Recommendation.
type RecommendationStatus struct {
	// State is the current state of the recommendation lifecycle.
	// +kubebuilder:validation:Enum=pending;approved;executing;executed;failed;dismissed
	// +kubebuilder:default=pending
	State string `json:"state,omitempty"`

	// AIGateResult holds the result of the AI gate validation, if applicable.
	// +optional
	AIGateResult *AIGateResult `json:"aiGateResult,omitempty"`

	// ExecutedAt is the timestamp when the recommendation was executed.
	// +optional
	ExecutedAt metav1.Time `json:"executedAt,omitempty"`

	// ExecutionResult holds a summary of the execution outcome.
	// +optional
	ExecutionResult string `json:"executionResult,omitempty"`

	// Error holds any error message from a failed execution.
	// +optional
	Error string `json:"error,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Priority",type=string,JSONPath=`.spec.priority`
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.targetName`
// +kubebuilder:printcolumn:name="State",type=string,JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="Savings/Mo",type=number,JSONPath=`.spec.estimatedSaving.monthlySavingsUSD`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Recommendation is the Schema for the recommendations API.
type Recommendation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RecommendationSpec   `json:"spec,omitempty"`
	Status RecommendationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// RecommendationList contains a list of Recommendation.
type RecommendationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Recommendation `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Recommendation{}, &RecommendationList{})
}

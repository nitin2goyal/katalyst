package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CommitmentReportSpec defines the desired state of CommitmentReport.
type CommitmentReportSpec struct {
	// ClusterName is the name of the cluster this report covers.
	ClusterName string `json:"clusterName"`
}

// CommitmentReportStatus defines the observed state of CommitmentReport.
type CommitmentReportStatus struct {
	// LastUpdated is the timestamp of the last report update.
	// +optional
	LastUpdated metav1.Time `json:"lastUpdated,omitempty"`

	// TotalCommitments is the total number of active commitments.
	TotalCommitments int `json:"totalCommitments,omitempty"`

	// TotalMonthlyCommitmentCostUSD is the total monthly cost of all commitments.
	TotalMonthlyCommitmentCostUSD float64 `json:"totalMonthlyCommitmentCostUSD,omitempty"`

	// AvgUtilizationPct is the average utilization across all commitments.
	AvgUtilizationPct float64 `json:"avgUtilizationPct,omitempty"`

	// Commitments lists all active commitments and their statuses.
	// +optional
	Commitments []CommitmentStatus `json:"commitments,omitempty"`

	// Underutilized lists commitments that are below the utilization threshold.
	// +optional
	Underutilized []UnderutilizedCommitment `json:"underutilized,omitempty"`

	// ExpiringSoon lists commitments that are expiring within the warning window.
	// +optional
	ExpiringSoon []ExpiringCommitment `json:"expiringSoon,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.spec.clusterName`
// +kubebuilder:printcolumn:name="Commitments",type=integer,JSONPath=`.status.totalCommitments`
// +kubebuilder:printcolumn:name="Monthly Cost",type=number,JSONPath=`.status.totalMonthlyCommitmentCostUSD`
// +kubebuilder:printcolumn:name="Avg Util %",type=number,JSONPath=`.status.avgUtilizationPct`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// CommitmentReport is the Schema for the commitmentreports API.
type CommitmentReport struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CommitmentReportSpec   `json:"spec,omitempty"`
	Status CommitmentReportStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// CommitmentReportList contains a list of CommitmentReport.
type CommitmentReportList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CommitmentReport `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CommitmentReport{}, &CommitmentReportList{})
}

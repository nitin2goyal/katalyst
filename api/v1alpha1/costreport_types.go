package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CostReportSpec defines the desired state of CostReport.
type CostReportSpec struct {
	// ClusterName is the name of the cluster this report covers.
	ClusterName string `json:"clusterName"`

	// ReportPeriod defines the reporting time window (e.g. "30d", "7d").
	ReportPeriod string `json:"reportPeriod"`
}

// CostReportStatus defines the observed state of CostReport.
type CostReportStatus struct {
	// LastUpdated is the timestamp of the last report update.
	// +optional
	LastUpdated metav1.Time `json:"lastUpdated,omitempty"`

	// TotalMonthlyCostUSD is the total monthly cost in USD.
	TotalMonthlyCostUSD float64 `json:"totalMonthlyCostUSD,omitempty"`

	// ProjectedMonthlyCostUSD is the projected monthly cost based on current trends.
	ProjectedMonthlyCostUSD float64 `json:"projectedMonthlyCostUSD,omitempty"`

	// PotentialSavingsUSD is the total potential savings identified.
	PotentialSavingsUSD float64 `json:"potentialSavingsUSD,omitempty"`

	// CostByNamespace maps namespace names to their monthly cost in USD.
	// +optional
	CostByNamespace map[string]float64 `json:"costByNamespace,omitempty"`

	// CostByNodeGroup maps node group names to their monthly cost in USD.
	// +optional
	CostByNodeGroup map[string]float64 `json:"costByNodeGroup,omitempty"`

	// DailyCostTrend holds daily cost data points for trend analysis.
	// +optional
	DailyCostTrend []DailyCost `json:"dailyCostTrend,omitempty"`

	// TopWorkloads lists the most expensive workloads.
	// +optional
	TopWorkloads []WorkloadCost `json:"topWorkloads,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.spec.clusterName`
// +kubebuilder:printcolumn:name="Period",type=string,JSONPath=`.spec.reportPeriod`
// +kubebuilder:printcolumn:name="Monthly Cost",type=number,JSONPath=`.status.totalMonthlyCostUSD`
// +kubebuilder:printcolumn:name="Projected",type=number,JSONPath=`.status.projectedMonthlyCostUSD`
// +kubebuilder:printcolumn:name="Savings",type=number,JSONPath=`.status.potentialSavingsUSD`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// CostReport is the Schema for the costreports API.
type CostReport struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CostReportSpec   `json:"spec,omitempty"`
	Status CostReportStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// CostReportList contains a list of CostReport.
type CostReportList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CostReport `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CostReport{}, &CostReportList{})
}

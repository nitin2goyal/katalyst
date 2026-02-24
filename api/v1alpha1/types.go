package v1alpha1

// Common types shared across CRDs

// ResourceUsage represents CPU, memory, and GPU resource usage.
type ResourceUsage struct {
	CPUCores    string `json:"cpuCores"`
	MemoryBytes string `json:"memoryBytes"`
	GPUs        int    `json:"gpus,omitempty"`
}

// SavingEstimate represents estimated cost savings.
type SavingEstimate struct {
	MonthlySavingsUSD float64 `json:"monthlySavingsUSD"`
	AnnualSavingsUSD  float64 `json:"annualSavingsUSD"`
	Currency          string  `json:"currency"`
}

// ImpactEstimate represents the estimated impact of a recommendation.
type ImpactEstimate struct {
	MonthlyCostChangeUSD float64 `json:"monthlyCostChangeUSD"`
	NodesAffected        int     `json:"nodesAffected"`
	PodsAffected         int     `json:"podsAffected"`
	// +kubebuilder:validation:Enum=low;medium;high
	RiskLevel string `json:"riskLevel"`
}

// DailyCost represents cost data for a single day.
type DailyCost struct {
	Date    string  `json:"date"`
	CostUSD float64 `json:"costUSD"`
}

// WorkloadCost represents the cost attribution for a single workload.
type WorkloadCost struct {
	Namespace    string  `json:"namespace"`
	Name         string  `json:"name"`
	Kind         string  `json:"kind"`
	MonthlyCostUSD float64 `json:"monthlyCostUSD"`
}

// CommitmentStatus represents the current status of a cloud commitment (RI/SP).
type CommitmentStatus struct {
	ID              string  `json:"id"`
	Type            string  `json:"type"`
	InstanceFamily  string  `json:"instanceFamily,omitempty"`
	InstanceType    string  `json:"instanceType,omitempty"`
	Region          string  `json:"region"`
	Count           int     `json:"count"`
	HourlyCostUSD   float64 `json:"hourlyCostUSD"`
	OnDemandCostUSD float64 `json:"onDemandCostUSD"`
	UtilizationPct  float64 `json:"utilizationPct"`
	ExpiresAt       string  `json:"expiresAt"`
	Status          string  `json:"status"`
}

// UnderutilizedCommitment represents a commitment that is not being fully used.
type UnderutilizedCommitment struct {
	CommitmentID   string  `json:"commitmentID"`
	Type           string  `json:"type"`
	InstanceType   string  `json:"instanceType"`
	UtilizationPct float64 `json:"utilizationPct"`
	WastedMonthlyUSD float64 `json:"wastedMonthlyUSD"`
	Suggestion     string  `json:"suggestion"`
}

// ExpiringCommitment represents a commitment that is expiring soon.
type ExpiringCommitment struct {
	CommitmentID  string  `json:"commitmentID"`
	Type          string  `json:"type"`
	ExpiresIn     string  `json:"expiresIn"`
	MonthlyValueUSD float64 `json:"monthlyValueUSD"`
}

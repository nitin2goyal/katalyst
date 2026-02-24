package cost

import "time"

// HoursPerMonth is the average number of hours in a calendar month
// (365.2425 days/year * 24 hours/day / 12 months = 730.485).
// Using a precise constant avoids the systematic underestimation that
// the commonly-used 730 introduces across cost calculations.
const HoursPerMonth = 730.5

// CostModel represents the cost calculation model for cluster resources.
type CostModel struct {
	HourlyCPUCostUSD     float64
	HourlyMemoryCostUSD  float64 // per GiB
	HourlyGPUCostUSD     float64
	StorageCostPerGiBUSD float64 // monthly
}

type NamespaceCost struct {
	Namespace      string
	MonthlyCostUSD float64
	CPUCostUSD     float64
	MemoryCostUSD  float64
	GPUCostUSD     float64
	StorageCostUSD float64
}

type WorkloadCost struct {
	Namespace      string
	Name           string
	Kind           string
	MonthlyCostUSD float64
	CPUCostUSD     float64
	MemoryCostUSD  float64
	GPUCostUSD     float64
	Replicas       int
	Efficiency     float64 // 0-1, usage/request ratio
}

type DailyCost struct {
	Date    time.Time
	CostUSD float64
}

type CostSummary struct {
	TotalMonthlyCostUSD     float64
	ProjectedMonthlyCostUSD float64
	PotentialSavingsUSD     float64
	SavingsByCategory       map[string]float64
	Timestamp               time.Time
}

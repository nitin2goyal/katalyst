package handler

import (
	"net/http"

	"github.com/koptimizer/koptimizer/internal/state"
	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
)

type SpotHandler struct {
	state    *state.ClusterState
	provider cloudprovider.CloudProvider
}

func NewSpotHandler(st *state.ClusterState, provider cloudprovider.CloudProvider) *SpotHandler {
	return &SpotHandler{state: st, provider: provider}
}

func (h *SpotHandler) GetSummary(w http.ResponseWriter, r *http.Request) {
	nodes := h.state.GetAllNodes()

	spotNodes := 0
	onDemandNodes := 0
	var spotHourlyCost, odHourlyCost float64

	for _, n := range nodes {
		if n.IsSpot || cloudprovider.IsSpotNode(n.Node) {
			spotNodes++
			spotHourlyCost += n.HourlyCostUSD
		} else {
			onDemandNodes++
			odHourlyCost += n.HourlyCostUSD
		}
	}

	totalNodes := spotNodes + onDemandNodes
	spotPct := 0.0
	if totalNodes > 0 {
		spotPct = float64(spotNodes) / float64(totalNodes) * 100
	}

	// Estimate savings using per-provider, per-family discount estimates.
	// Reverse-engineer on-demand equivalent from actual spot cost.
	avgDiscount := 0.65 // conservative default
	if sde, ok := h.provider.(cloudprovider.SpotDiscountEstimator); ok {
		// Compute weighted average discount across spot nodes
		totalDiscount := 0.0
		spotNodeCount := 0
		for _, n := range nodes {
			if n.IsSpot || cloudprovider.IsSpotNode(n.Node) {
				totalDiscount += sde.EstimateSpotDiscount(n.InstanceType)
				spotNodeCount++
			}
		}
		if spotNodeCount > 0 {
			avgDiscount = totalDiscount / float64(spotNodeCount)
		}
	}
	estimatedODEquivalent := 0.0
	if avgDiscount > 0 && avgDiscount < 1 {
		estimatedODEquivalent = spotHourlyCost / (1 - avgDiscount)
	}
	estimatedSavings := (estimatedODEquivalent - spotHourlyCost) * 730

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"spotNodes":                    spotNodes,
		"onDemandNodes":                onDemandNodes,
		"spotPercentage":               spotPct,
		"spotHourlyCostUSD":            spotHourlyCost,
		"onDemandHourlyCostUSD":        odHourlyCost,
		"estimatedMonthlySavingsUSD":   estimatedSavings,
	})
}

func (h *SpotHandler) GetNodes(w http.ResponseWriter, r *http.Request) {
	nodes := h.state.GetAllNodes()

	var result []map[string]interface{}
	for _, n := range nodes {
		lifecycle := "on-demand"
		if n.IsSpot || cloudprovider.IsSpotNode(n.Node) {
			lifecycle = "spot"
		}

		zone := ""
		if z, ok := n.Node.Labels["topology.kubernetes.io/zone"]; ok {
			zone = z
		}

		result = append(result, map[string]interface{}{
			"name":          n.Node.Name,
			"instanceType":  n.InstanceType,
			"lifecycle":     lifecycle,
			"zone":          zone,
			"hourlyCostUSD": n.HourlyCostUSD,
		})
	}

	if result == nil {
		result = []map[string]interface{}{}
	}
	writePaginatedJSON(w, r, result)
}

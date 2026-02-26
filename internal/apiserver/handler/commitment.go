package handler

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
	"github.com/koptimizer/koptimizer/pkg/cost"
)

type CommitmentHandler struct {
	provider cloudprovider.CloudProvider
}

func NewCommitmentHandler(provider cloudprovider.CloudProvider) *CommitmentHandler {
	return &CommitmentHandler{provider: provider}
}

// collectCommitments gathers all commitment types from the provider, logging
// any errors but continuing with partial results for resilience.
func (h *CommitmentHandler) collectCommitments(ctx context.Context) []*cloudprovider.Commitment {
	var all []*cloudprovider.Commitment

	ris, err := h.provider.GetReservedInstances(ctx)
	if err != nil {
		slog.Warn("Failed to get reserved instances", "error", err)
	}
	all = append(all, ris...)

	sps, err := h.provider.GetSavingsPlans(ctx)
	if err != nil {
		slog.Warn("Failed to get savings plans", "error", err)
	}
	all = append(all, sps...)

	cuds, err := h.provider.GetCommittedUseDiscounts(ctx)
	if err != nil {
		slog.Warn("Failed to get committed use discounts", "error", err)
	}
	all = append(all, cuds...)

	res, err := h.provider.GetReservations(ctx)
	if err != nil {
		slog.Warn("Failed to get reservations", "error", err)
	}
	all = append(all, res...)

	return all
}

func (h *CommitmentHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	all := h.collectCommitments(ctx)
	items := make([]map[string]interface{}, 0, len(all))
	for _, c := range all {
		items = append(items, map[string]interface{}{
			"id":              c.ID,
			"type":            c.Type,
			"instanceFamily":  c.InstanceFamily,
			"instanceType":    c.InstanceType,
			"region":          c.Region,
			"count":           c.Count,
			"hourlyCostUSD":   c.HourlyCostUSD,
			"onDemandCostUSD": c.OnDemandCostUSD,
			"utilizationPct":  c.UtilizationPct,
			"expiresAt":       c.ExpiresAt.Format(time.RFC3339),
			"status":          c.Status,
		})
	}
	writePaginatedJSON(w, r, items)
}

func (h *CommitmentHandler) GetUnderutilized(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	all := h.collectCommitments(ctx)

	type underutilized struct {
		CommitmentID     string  `json:"commitmentID"`
		Type             string  `json:"type"`
		InstanceType     string  `json:"instanceType"`
		UtilizationPct   float64 `json:"utilizationPct"`
		WastedMonthlyUSD float64 `json:"wastedMonthlyUSD"`
	}

	var result []underutilized
	for _, c := range all {
		if c.UtilizationPct < 50 {
			wastedFraction := 1.0 - (c.UtilizationPct / 100.0)
			wastedMonthly := c.HourlyCostUSD * cost.HoursPerMonth * wastedFraction
			result = append(result, underutilized{
				CommitmentID:     c.ID,
				Type:             c.Type,
				InstanceType:     c.InstanceType,
				UtilizationPct:   c.UtilizationPct,
				WastedMonthlyUSD: wastedMonthly,
			})
		}
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *CommitmentHandler) GetExpiring(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	all := h.collectCommitments(ctx)

	type expiring struct {
		CommitmentID    string  `json:"commitmentID"`
		Type            string  `json:"type"`
		ExpiresAt       string  `json:"expiresAt"`
		MonthlyValueUSD float64 `json:"monthlyValueUSD"`
	}

	now := time.Now()
	cutoff := now.Add(90 * 24 * time.Hour)

	var result []expiring
	for _, c := range all {
		if c.ExpiresAt.After(now) && c.ExpiresAt.Before(cutoff) {
			result = append(result, expiring{
				CommitmentID:    c.ID,
				Type:            c.Type,
				ExpiresAt:       c.ExpiresAt.Format(time.RFC3339),
				MonthlyValueUSD: c.HourlyCostUSD * cost.HoursPerMonth,
			})
		}
	}
	writeJSON(w, http.StatusOK, result)
}

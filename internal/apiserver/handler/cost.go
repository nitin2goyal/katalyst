package handler

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	koptv1alpha1 "github.com/koptimizer/koptimizer/api/v1alpha1"
	"github.com/koptimizer/koptimizer/internal/state"
	"github.com/koptimizer/koptimizer/internal/store"
	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
	"github.com/koptimizer/koptimizer/pkg/cost"
)

type CostHandler struct {
	state     *state.ClusterState
	provider  cloudprovider.CloudProvider
	client    client.Client
	costStore *store.CostStore
}

func NewCostHandler(st *state.ClusterState, provider cloudprovider.CloudProvider, c client.Client, costStore *store.CostStore) *CostHandler {
	return &CostHandler{state: st, provider: provider, client: c, costStore: costStore}
}

func (h *CostHandler) GetSummary(w http.ResponseWriter, r *http.Request) {
	nodes := h.state.GetAllNodes()
	totalHourly := 0.0
	for _, n := range nodes {
		totalHourly += n.HourlyCostUSD
	}

	// Fetch potential savings from Recommendation CRDs
	potentialSavings := 0.0
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var recList koptv1alpha1.RecommendationList
	if err := h.client.List(ctx, &recList, client.InNamespace("koptimizer-system")); err == nil {
		for _, rec := range recList.Items {
			if rec.Status.State == "pending" || rec.Status.State == "approved" || rec.Status.State == "" {
				potentialSavings += rec.Spec.EstimatedSaving.MonthlySavingsUSD
			}
		}
	}

	// Fallback: compute savings from engine when CRDs yield nothing
	if potentialSavings == 0 {
		computed := ComputeRecommendations(h.state)
		potentialSavings = ComputeTotalPotentialSavings(computed)
	}

	resp := map[string]interface{}{
		"totalMonthlyCostUSD":     totalHourly * cost.HoursPerMonth,
		"projectedMonthlyCostUSD": totalHourly * cost.HoursPerMonth,
		"nodeCount":               len(nodes),
		"potentialSavings":        potentialSavings,
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *CostHandler) GetByNamespace(w http.ResponseWriter, r *http.Request) {
	nodes := h.state.GetAllNodes()
	costs := make(map[string]float64)

	for _, n := range nodes {
		if n.CPUCapacity == 0 && n.MemoryCapacity == 0 {
			continue
		}
		nodeCost := n.HourlyCostUSD * cost.HoursPerMonth
		if nodeCost == 0 {
			continue
		}
		// Two-pass: compute per-pod weights then distribute full node cost
		// proportionally so namespace costs sum to total cluster cost.
		weights := make([]float64, len(n.Pods))
		totalW := 0.0
		for i, pod := range n.Pods {
			w := podWeight(pod.Spec.Containers, n.CPUCapacity, n.MemoryCapacity)
			weights[i] = w
			totalW += w
		}
		if totalW == 0 {
			continue
		}
		for i, pod := range n.Pods {
			costs[pod.Namespace] += nodeCost * weights[i] / totalW
		}
	}

	type nsCost struct {
		Namespace      string  `json:"namespace"`
		MonthlyCostUSD float64 `json:"monthlyCostUSD"`
	}
	result := make([]nsCost, 0, len(costs))
	for ns, c := range costs {
		result = append(result, nsCost{Namespace: ns, MonthlyCostUSD: c})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].MonthlyCostUSD > result[j].MonthlyCostUSD
	})
	page, pageSize := parsePagination(r)
	start, end, resp := paginateSlice(len(result), page, pageSize)
	resp.Data = result[start:end]
	writeJSON(w, http.StatusOK, resp)
}

func (h *CostHandler) GetByWorkload(w http.ResponseWriter, r *http.Request) {
	nodes := h.state.GetAllNodes()
	type workloadCost struct {
		Namespace      string  `json:"namespace"`
		Kind           string  `json:"kind"`
		Name           string  `json:"name"`
		MonthlyCostUSD float64 `json:"monthlyCostUSD"`
	}
	costs := make(map[string]*workloadCost)

	for _, n := range nodes {
		if n.CPUCapacity == 0 && n.MemoryCapacity == 0 {
			continue
		}
		nodeCost := n.HourlyCostUSD * cost.HoursPerMonth
		if nodeCost == 0 {
			continue
		}
		weights := make([]float64, len(n.Pods))
		totalW := 0.0
		for i, pod := range n.Pods {
			w := podWeight(pod.Spec.Containers, n.CPUCapacity, n.MemoryCapacity)
			weights[i] = w
			totalW += w
		}
		if totalW == 0 {
			continue
		}
		for i, pod := range n.Pods {
			ownerKind, ownerName := "", ""
			if len(pod.OwnerReferences) > 0 {
				ownerKind = pod.OwnerReferences[0].Kind
				ownerName = pod.OwnerReferences[0].Name
			}
			if ownerName == "" {
				ownerName = pod.Name
				ownerKind = "Pod"
			}
			key := pod.Namespace + "/" + ownerKind + "/" + ownerName
			monthlyCost := nodeCost * weights[i] / totalW
			if existing, ok := costs[key]; ok {
				existing.MonthlyCostUSD += monthlyCost
			} else {
				costs[key] = &workloadCost{
					Namespace:      pod.Namespace,
					Kind:           ownerKind,
					Name:           ownerName,
					MonthlyCostUSD: monthlyCost,
				}
			}
		}
	}

	result := make([]*workloadCost, 0, len(costs))
	for _, wc := range costs {
		result = append(result, wc)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].MonthlyCostUSD > result[j].MonthlyCostUSD
	})
	page, pageSize := parsePagination(r)
	start, end, resp := paginateSlice(len(result), page, pageSize)
	resp.Data = result[start:end]
	writeJSON(w, http.StatusOK, resp)
}

// GetByLabel returns cost breakdown grouped by pod labels.
// Supports ?key=<label-key> to filter by a specific label key (e.g., ?key=team).
func (h *CostHandler) GetByLabel(w http.ResponseWriter, r *http.Request) {
	filterKey := r.URL.Query().Get("key")
	nodes := h.state.GetAllNodes()

	// labelKey -> labelValue -> cost
	costs := make(map[string]map[string]float64)

	for _, n := range nodes {
		if n.CPUCapacity == 0 && n.MemoryCapacity == 0 {
			continue
		}
		nodeCost := n.HourlyCostUSD * cost.HoursPerMonth
		if nodeCost == 0 {
			continue
		}
		weights := make([]float64, len(n.Pods))
		totalW := 0.0
		for i, pod := range n.Pods {
			w := podWeight(pod.Spec.Containers, n.CPUCapacity, n.MemoryCapacity)
			weights[i] = w
			totalW += w
		}
		if totalW == 0 {
			continue
		}
		for i, pod := range n.Pods {
			monthlyCost := nodeCost * weights[i] / totalW
			for labelKey, labelValue := range pod.Labels {
				if isInternalLabel(labelKey) {
					continue
				}
				if filterKey != "" && labelKey != filterKey {
					continue
				}
				if costs[labelKey] == nil {
					costs[labelKey] = make(map[string]float64)
				}
				costs[labelKey][labelValue] += monthlyCost
			}
		}
	}

	writeJSON(w, http.StatusOK, costs)
}

func (h *CostHandler) GetTrend(w http.ResponseWriter, r *http.Request) {
	// Try SQLite first (GetTrend is nil-safe, returns nil if db is nil)
	days := 30
	if d := r.URL.Query().Get("days"); d != "" {
		if parsed, err := strconv.Atoi(d); err == nil && parsed > 0 {
			days = parsed
		}
	}
	if trend := h.costStore.GetTrend(days); len(trend) > 0 {
		writeJSON(w, http.StatusOK, map[string]interface{}{"dataPoints": trend})
		return
	}

	// Fallback to CRD
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	var reportList koptv1alpha1.CostReportList
	if err := h.client.List(ctx, &reportList, client.InNamespace("koptimizer-system")); err != nil {
		slog.Warn("Failed to list CostReports for trend fallback", "error", err)
	} else if len(reportList.Items) > 0 {
		for _, report := range reportList.Items {
			if len(report.Status.DailyCostTrend) > 0 {
				writeJSON(w, http.StatusOK, map[string]interface{}{"dataPoints": report.Status.DailyCostTrend})
				return
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"dataPoints": []interface{}{}})
}

func (h *CostHandler) GetSavings(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	var recList koptv1alpha1.RecommendationList

	var opportunities []map[string]interface{}

	if err := h.client.List(ctx, &recList, client.InNamespace("koptimizer-system")); err != nil {
		slog.Warn("Failed to list Recommendations for savings", "error", err)
	} else {
		for _, rec := range recList.Items {
			// Only include actionable (non-dismissed, non-executed) recommendations
			st := rec.Status.State
			if st == "dismissed" || st == "executed" || st == "failed" {
				continue
			}
			target := rec.Spec.TargetName
			if rec.Spec.TargetNamespace != "" {
				target = rec.Spec.TargetNamespace + "/" + target
			}
			opportunities = append(opportunities, map[string]interface{}{
				"type":             rec.Spec.Type,
				"name":             target,
				"description":      rec.Spec.Summary,
				"estimatedSavings": rec.Spec.EstimatedSaving.MonthlySavingsUSD,
			})
		}
	}

	// Fallback: compute savings opportunities from engine when CRDs yield nothing
	if len(opportunities) == 0 {
		computed := ComputeRecommendations(h.state)
		for _, opp := range ComputeSavingsOpportunities(computed) {
			opportunities = append(opportunities, map[string]interface{}{
				"type":             opp.Type,
				"name":             opp.Name,
				"description":      opp.Description,
				"estimatedSavings": opp.EstimatedSavings,
			})
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"opportunities": opportunities,
	})
}

// GetComparison returns a cost comparison between current and previous periods.
func (h *CostHandler) GetComparison(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	currentPeriod := now.Format("2006-01")
	previousPeriod := now.AddDate(0, -1, 0).Format("2006-01")

	// Try to get 60 days of trend data for comparison
	trend := h.costStore.GetTrend(60)

	// Get per-namespace breakdown for current vs previous period
	currentByNS := h.costStore.GetByNamespaceForPeriod(
		now.AddDate(0, 0, -30),
		now,
	)
	previousByNS := h.costStore.GetByNamespaceForPeriod(
		now.AddDate(0, 0, -60),
		now.AddDate(0, 0, -30),
	)

	// Compute current period cost from live state
	nodes := h.state.GetAllNodes()
	currentTotalHourly := 0.0
	for _, n := range nodes {
		currentTotalHourly += n.HourlyCostUSD
	}
	currentTotal := currentTotalHourly * cost.HoursPerMonth

	// Compute previous total from trend data if available
	var previousTotal float64
	if len(trend) > 30 {
		sum := 0.0
		count := 0
		for i := 0; i < len(trend)-30; i++ {
			sum += trend[i].TotalMonthlyCost
			count++
		}
		if count > 0 {
			previousTotal = sum / float64(count)
		}
	}

	// Build namespace comparison using mock-matching field names
	type nsComparison struct {
		Namespace    string  `json:"namespace"`
		CurrentCost  float64 `json:"currentCost"`
		PreviousCost float64 `json:"previousCost"`
		Change       float64 `json:"change"`
	}
	allNS := make(map[string]bool)
	for ns := range currentByNS {
		allNS[ns] = true
	}
	for ns := range previousByNS {
		allNS[ns] = true
	}

	// If no store data, derive current namespace costs from live state
	if len(currentByNS) == 0 {
		currentByNS = make(map[string]float64)
		for _, n := range nodes {
			if n.CPUCapacity == 0 && n.MemoryCapacity == 0 {
				continue
			}
			nodeCost := n.HourlyCostUSD * cost.HoursPerMonth
			if nodeCost == 0 {
				continue
			}
			weights := make([]float64, len(n.Pods))
			totalW := 0.0
			for i, pod := range n.Pods {
				w := podWeight(pod.Spec.Containers, n.CPUCapacity, n.MemoryCapacity)
				weights[i] = w
				totalW += w
			}
			if totalW == 0 {
				continue
			}
			for i, pod := range n.Pods {
				currentByNS[pod.Namespace] += nodeCost * weights[i] / totalW
				allNS[pod.Namespace] = true
			}
		}
	}

	var byNamespace []nsComparison
	for ns := range allNS {
		cur := currentByNS[ns]
		prev := previousByNS[ns]
		changePct := 0.0
		if prev > 0 {
			changePct = (cur - prev) / prev * 100
		}
		byNamespace = append(byNamespace, nsComparison{
			Namespace:    ns,
			CurrentCost:  cur,
			PreviousCost: prev,
			Change:       changePct,
		})
	}
	sort.Slice(byNamespace, func(i, j int) bool {
		return byNamespace[i].CurrentCost > byNamespace[j].CurrentCost
	})

	// Estimate cost breakdown: compute ~75%, storage ~15%, network ~4%, other ~6%
	// These ratios are typical for Kubernetes clusters
	computeCurrent := currentTotal * 0.75
	storageCurrent := currentTotal * 0.15
	networkCurrent := currentTotal * 0.04
	otherCurrent := currentTotal - computeCurrent - storageCurrent - networkCurrent

	computePrevious := previousTotal * 0.75
	storagePrevious := previousTotal * 0.15
	networkPrevious := previousTotal * 0.04
	otherPrevious := previousTotal - computePrevious - storagePrevious - networkPrevious

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"currentPeriod":  currentPeriod,
		"previousPeriod": previousPeriod,
		"current": map[string]interface{}{
			"totalCost":   currentTotal,
			"computeCost": computeCurrent,
			"storageCost": storageCurrent,
			"networkCost": networkCurrent,
			"otherCost":   otherCurrent,
		},
		"previous": map[string]interface{}{
			"totalCost":   previousTotal,
			"computeCost": computePrevious,
			"storageCost": storagePrevious,
			"networkCost": networkPrevious,
			"otherCost":   otherPrevious,
		},
		"byNamespace": byNamespace,
	})
}

// GetImpact returns historical savings from executed recommendations.
// Computes real savings from Recommendation CRDs (executed state) and audit events.
func (h *CostHandler) GetImpact(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	// Fetch all recommendations to find executed ones with real savings
	var recList koptv1alpha1.RecommendationList
	if err := h.client.List(ctx, &recList, client.InNamespace("koptimizer-system")); err != nil {
		slog.Warn("Failed to list Recommendations for impact", "error", err)
	}

	// Build savings from executed/approved recommendations with real EstimatedSaving
	type monthlySaving struct {
		Month             string  `json:"month"`
		SavingsUSD        float64 `json:"savingsUSD"`
		CumulativeSavings float64 `json:"cumulativeSavings"`
		ActionsApplied    int     `json:"actionsApplied"`
	}

	type categoryInfo struct {
		Category       string  `json:"category"`
		SavingsUSD     float64 `json:"savingsUSD"`
		ActionsApplied int     `json:"actionsApplied"`
	}

	monthMap := make(map[string]*monthlySaving)
	catMap := make(map[string]*categoryInfo)
	var recentActions []map[string]interface{}
	totalSavings := 0.0
	totalActions := 0

	for _, rec := range recList.Items {
		// Only count executed or approved recommendations as realized savings
		if rec.Status.State != "executed" && rec.Status.State != "approved" {
			continue
		}

		saving := rec.Spec.EstimatedSaving.MonthlySavingsUSD
		if saving == 0 {
			continue
		}

		// Determine month from execution or creation time
		var ts time.Time
		if !rec.Status.ExecutedAt.IsZero() {
			ts = rec.Status.ExecutedAt.Time
		} else {
			ts = rec.CreationTimestamp.Time
		}
		month := ts.Format("2006-01")

		ms, ok := monthMap[month]
		if !ok {
			ms = &monthlySaving{Month: month}
			monthMap[month] = ms
		}
		ms.SavingsUSD += saving
		ms.ActionsApplied++

		// Category from rec type
		cat := rec.Spec.Type
		ci, ok := catMap[cat]
		if !ok {
			ci = &categoryInfo{Category: cat}
			catMap[cat] = ci
		}
		ci.SavingsUSD += saving
		ci.ActionsApplied++

		totalSavings += saving
		totalActions++

		// Collect as recent action
		recentActions = append(recentActions, map[string]interface{}{
			"timestamp":  ts.Format(time.RFC3339),
			"action":     rec.Spec.Summary,
			"savingsUSD": saving,
			"category":   cat,
		})
	}

	// Also check audit log for actions not captured in CRDs
	events := h.state.AuditLog.GetAll()

	// Build a set of targets already counted from CRDs to avoid double-counting
	countedTargets := make(map[string]bool)
	for _, rec := range recList.Items {
		if rec.Status.State == "executed" || rec.Status.State == "approved" {
			countedTargets[rec.Spec.TargetNamespace+"/"+rec.Spec.TargetName] = true
		}
	}

	// For audit events not covered by CRDs, estimate savings from actual node costs
	// These are real actions (scale-down, drain) where we can use actual node pricing
	nodeMap := make(map[string]float64) // node name -> monthly cost
	for _, n := range h.state.GetAllNodes() {
		nodeMap[n.Node.Name] = n.HourlyCostUSD * cost.HoursPerMonth
	}

	for _, e := range events {
		if countedTargets[e.Target] {
			continue
		}

		saving := 0.0
		category := ""

		switch {
		case containsAction(e.Action, "scale-down", "drain", "consolidate"):
			// Use actual node cost if target is a known node
			if nodeCost, ok := nodeMap[e.Target]; ok {
				saving = nodeCost
			}
			category = "Consolidation"
		case containsAction(e.Action, "spot-convert"):
			// nodeMap values are monthly costs at spot pricing (OD * 0.35).
			// On-demand equivalent = spotCost / 0.35; savings = OD - spot.
			if nodeCost, ok := nodeMap[e.Target]; ok {
				saving = nodeCost/0.35 - nodeCost
			}
			category = "Spot Migration"
		default:
			// Skip actions we can't reliably estimate
			continue
		}

		if saving == 0 {
			continue
		}

		month := e.Timestamp.Format("2006-01")
		ms, ok := monthMap[month]
		if !ok {
			ms = &monthlySaving{Month: month}
			monthMap[month] = ms
		}
		ms.SavingsUSD += saving
		ms.ActionsApplied++

		ci, ok := catMap[category]
		if !ok {
			ci = &categoryInfo{Category: category}
			catMap[category] = ci
		}
		ci.SavingsUSD += saving
		ci.ActionsApplied++

		totalSavings += saving
		totalActions++
	}

	// Sort recent actions by timestamp descending, limit to 20
	sort.Slice(recentActions, func(i, j int) bool {
		tsi, _ := recentActions[i]["timestamp"].(string)
		tsj, _ := recentActions[j]["timestamp"].(string)
		return tsi > tsj
	})
	if len(recentActions) > 20 {
		recentActions = recentActions[:20]
	}

	// Convert months to sorted slice with cumulative totals
	months := make([]*monthlySaving, 0, len(monthMap))
	for _, ms := range monthMap {
		months = append(months, ms)
	}
	sort.Slice(months, func(i, j int) bool {
		return months[i].Month < months[j].Month
	})
	cumulative := 0.0
	for _, ms := range months {
		cumulative += ms.SavingsUSD
		ms.CumulativeSavings = cumulative
	}

	// Convert categories to slice
	categories := make([]*categoryInfo, 0, len(catMap))
	for _, ci := range catMap {
		categories = append(categories, ci)
	}
	sort.Slice(categories, func(i, j int) bool {
		return categories[i].SavingsUSD > categories[j].SavingsUSD
	})

	avgMonthlySavings := 0.0
	if len(months) > 0 {
		avgMonthlySavings = totalSavings / float64(len(months))
	}

	// Compute savings vs identified ratio from total recommendations
	identifiedTotal := 0.0
	for _, rec := range recList.Items {
		identifiedTotal += rec.Spec.EstimatedSaving.MonthlySavingsUSD
	}
	savingsVsIdentified := 0.0
	if identifiedTotal > 0 {
		savingsVsIdentified = totalSavings / identifiedTotal * 100
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"summary": map[string]interface{}{
			"totalSavingsUSD":        totalSavings,
			"avgMonthlySavingsUSD":   avgMonthlySavings,
			"totalActionsApplied":    totalActions,
			"savingsVsIdentifiedPct": savingsVsIdentified,
		},
		"monthly":       months,
		"byCategory":    categories,
		"recentActions": recentActions,
	})
}

// podWeight computes a blended CPU+memory weight for a pod relative to node capacity.
func podWeight(containers []corev1.Container, cpuCap, memCap int64) float64 {
	cpuReq := int64(0)
	memReq := int64(0)
	for _, c := range containers {
		if cpu, ok := c.Resources.Requests["cpu"]; ok {
			cpuReq += cpu.MilliValue()
		}
		if mem, ok := c.Resources.Requests["memory"]; ok {
			memReq += mem.Value()
		}
	}
	w := 0.0
	if cpuCap > 0 {
		w += 0.5 * float64(cpuReq) / float64(cpuCap)
	}
	if memCap > 0 {
		w += 0.5 * float64(memReq) / float64(memCap)
	}
	return w
}

// containsAction checks if the action string matches any of the given prefixes.
func containsAction(action string, prefixes ...string) bool {
	for _, p := range prefixes {
		if len(action) >= len(p) && action[:len(p)] == p {
			return true
		}
	}
	return false
}

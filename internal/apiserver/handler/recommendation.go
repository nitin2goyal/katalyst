package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	koptv1alpha1 "github.com/koptimizer/koptimizer/api/v1alpha1"
	"github.com/koptimizer/koptimizer/internal/state"
)

type RecommendationHandler struct {
	state  *state.ClusterState
	client client.Client
}

func NewRecommendationHandler(st *state.ClusterState, c client.Client) *RecommendationHandler {
	return &RecommendationHandler{state: st, client: c}
}

func (h *RecommendationHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	var recList koptv1alpha1.RecommendationList
	crdErr := h.client.List(ctx, &recList, client.InNamespace("koptimizer-system"))
	if crdErr != nil && !meta.IsNoMatchError(crdErr) {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": crdErr.Error()})
		return
	}

	// Fallback: compute recommendations on-the-fly when no CRDs exist
	if crdErr != nil || len(recList.Items) == 0 {
		computed := ComputeRecommendations(h.state)
		writeJSON(w, http.StatusOK, computed)
		return
	}

	// Transform CRD objects to API-friendly format matching expected shape
	result := make([]map[string]interface{}, 0, len(recList.Items))
	for _, rec := range recList.Items {
		target := rec.Spec.TargetName
		if rec.Spec.TargetNamespace != "" {
			target = rec.Spec.TargetNamespace + "/" + rec.Spec.TargetName
		}
		status := rec.Status.State
		if status == "" {
			status = "pending"
		}
		result = append(result, map[string]interface{}{
			"id":               rec.Name,
			"type":             rec.Spec.Type,
			"target":           target,
			"description":      rec.Spec.Summary,
			"estimatedSavings": rec.Spec.EstimatedSaving.MonthlySavingsUSD,
			"status":           status,
			"priority":         rec.Spec.Priority,
			"createdAt":        rec.CreationTimestamp.Format(time.RFC3339),
			"confidence":       0.90,
		})
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *RecommendationHandler) Get(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	id := chi.URLParam(r, "id")
	var rec koptv1alpha1.Recommendation
	if err := h.client.Get(ctx, types.NamespacedName{
		Namespace: "koptimizer-system",
		Name:      id,
	}, &rec); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "recommendation not found", "id": id})
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

func (h *RecommendationHandler) Approve(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	id := chi.URLParam(r, "id")
	// Computed recommendations (from the engine) cannot be approved via CRD update.
	if len(id) > 9 && id[:9] == "computed-" {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"id": id, "status": "acknowledged",
			"message": "Computed recommendation noted. Switch to OPTIMIZE mode to enable automatic execution.",
		})
		return
	}
	var rec koptv1alpha1.Recommendation
	if err := h.client.Get(ctx, types.NamespacedName{
		Namespace: "koptimizer-system",
		Name:      id,
	}, &rec); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "recommendation not found", "id": id})
		return
	}
	rec.Status.State = "approved"
	if err := h.client.Status().Update(ctx, &rec); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

func (h *RecommendationHandler) Dismiss(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	id := chi.URLParam(r, "id")
	// Computed recommendations cannot be dismissed via CRD update.
	if len(id) > 9 && id[:9] == "computed-" {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"id": id, "status": "dismissed",
			"message": "Computed recommendation dismissed.",
		})
		return
	}
	var rec koptv1alpha1.Recommendation
	if err := h.client.Get(ctx, types.NamespacedName{
		Namespace: "koptimizer-system",
		Name:      id,
	}, &rec); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "recommendation not found", "id": id})
		return
	}
	rec.Status.State = "dismissed"
	if err := h.client.Status().Update(ctx, &rec); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

// Debug returns raw metrics availability info for data validation.
func (h *RecommendationHandler) Debug(w http.ResponseWriter, r *http.Request) {
	nodes := h.state.GetAllNodes()
	pods := h.state.GetAllPods()

	nodesWithUsage := 0
	totalNodeCPUCap := int64(0)
	totalNodeCPUUsed := int64(0)
	totalNodeMemCap := int64(0)
	totalNodeMemUsed := int64(0)
	totalNodeHourly := 0.0
	emptyNodes := 0
	spotNodes := 0

	for _, n := range nodes {
		totalNodeCPUCap += n.CPUCapacity
		totalNodeCPUUsed += n.CPUUsed
		totalNodeMemCap += n.MemoryCapacity
		totalNodeMemUsed += n.MemoryUsed
		totalNodeHourly += n.HourlyCostUSD
		if n.CPUUsed > 0 || n.MemoryUsed > 0 {
			nodesWithUsage++
		}
		if n.IsEmpty() {
			emptyNodes++
		}
		if n.IsSpot {
			spotNodes++
		}
	}

	podsWithUsage := 0
	totalPodCPUReq := int64(0)
	totalPodCPUUsage := int64(0)
	totalPodMemReq := int64(0)
	totalPodMemUsage := int64(0)
	for _, p := range pods {
		totalPodCPUReq += p.CPURequest
		totalPodCPUUsage += p.CPUUsage
		totalPodMemReq += p.MemoryRequest
		totalPodMemUsage += p.MemoryUsage
		if p.CPUUsage > 0 || p.MemoryUsage > 0 {
			podsWithUsage++
		}
	}

	computed := ComputeRecommendations(h.state)
	recsByType := map[string]int{}
	savingsByType := map[string]float64{}
	for _, r := range computed {
		recsByType[r.Type]++
		savingsByType[r.Type] += r.EstimatedSavings
	}

	cpuUtilPct := 0.0
	if totalNodeCPUCap > 0 {
		cpuUtilPct = float64(totalNodeCPUUsed) / float64(totalNodeCPUCap) * 100
	}
	memUtilPct := 0.0
	if totalNodeMemCap > 0 {
		memUtilPct = float64(totalNodeMemUsed) / float64(totalNodeMemCap) * 100
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"nodes": map[string]interface{}{
			"total":         len(nodes),
			"withUsageData": nodesWithUsage,
			"empty":         emptyNodes,
			"spot":          spotNodes,
			"totalCPUCap":   totalNodeCPUCap,
			"totalCPUUsed":  totalNodeCPUUsed,
			"cpuUtilPct":    cpuUtilPct,
			"totalMemCap":   totalNodeMemCap,
			"totalMemUsed":  totalNodeMemUsed,
			"memUtilPct":    memUtilPct,
			"totalHourlyCost": totalNodeHourly,
			"totalMonthlyCost": totalNodeHourly * 730.5,
		},
		"pods": map[string]interface{}{
			"total":          len(pods),
			"withUsageData":  podsWithUsage,
			"totalCPUReq":    totalPodCPUReq,
			"totalCPUUsage":  totalPodCPUUsage,
			"totalMemReq":    totalPodMemReq,
			"totalMemUsage":  totalPodMemUsage,
			"hasMetrics":     podsWithUsage > len(pods)/10,
		},
		"recommendations": map[string]interface{}{
			"total":        len(computed),
			"byType":       recsByType,
			"savingsByType": savingsByType,
			"totalSavings": ComputeTotalPotentialSavings(computed),
		},
	})
}

func (h *RecommendationHandler) GetSummary(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	var recList koptv1alpha1.RecommendationList
	crdErr := h.client.List(ctx, &recList, client.InNamespace("koptimizer-system"))
	if crdErr != nil && !meta.IsNoMatchError(crdErr) {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": crdErr.Error()})
		return
	}

	// Fallback: compute summary from engine when no CRDs exist
	if crdErr != nil || len(recList.Items) == 0 {
		computed := ComputeRecommendations(h.state)
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"total":                 len(computed),
			"pending":               len(computed),
			"approved":              0,
			"dismissed":             0,
			"totalEstimatedSavings": ComputeTotalPotentialSavings(computed),
		})
		return
	}

	statusCounts := map[string]int{
		"pending": 0, "approved": 0, "dismissed": 0,
	}
	totalSavings := 0.0

	for _, rec := range recList.Items {
		st := rec.Status.State
		if st == "" {
			st = "pending"
		}
		statusCounts[st]++
		totalSavings += rec.Spec.EstimatedSaving.MonthlySavingsUSD
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"total":                 len(recList.Items),
		"pending":               statusCounts["pending"],
		"approved":              statusCounts["approved"],
		"dismissed":             statusCounts["dismissed"],
		"totalEstimatedSavings": totalSavings,
	})
}

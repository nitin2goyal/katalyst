package handler

import (
	"encoding/json"
	"net/http"

	"github.com/koptimizer/koptimizer/internal/helmdrift"
)

type HelmDriftHandler struct {
	service *helmdrift.Service
}

func NewHelmDriftHandler(svc *helmdrift.Service) *HelmDriftHandler {
	return &HelmDriftHandler{service: svc}
}

func (h *HelmDriftHandler) Get(w http.ResponseWriter, r *http.Request) {
	forceRefresh := r.URL.Query().Get("refresh") == "true"
	result, err := h.service.GetDrift(forceRefresh)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

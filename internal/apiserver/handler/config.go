package handler

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/internal/store"
)

type ConfigHandler struct {
	config   *config.Config
	settings *store.SettingsStore
}

func NewConfigHandler(cfg *config.Config, settings *store.SettingsStore) *ConfigHandler {
	return &ConfigHandler{config: cfg, settings: settings}
}

func (h *ConfigHandler) Get(w http.ResponseWriter, r *http.Request) {
	h.config.Mu.RLock()
	defer h.config.Mu.RUnlock()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"mode":          h.config.Mode,
		"cloudProvider": h.config.CloudProvider,
		"region":        h.config.Region,
		"clusterName":   h.config.ClusterName,
		"controllers": map[string]bool{
			"costMonitor":    h.config.CostMonitor.Enabled,
			"nodegroupMgr":   h.config.NodeGroupMgr.Enabled,
			"rightsizer":     h.config.Rightsizer.Enabled,
			"workloadScaler": h.config.WorkloadScaler.Enabled,
			"gpu":            h.config.GPU.Enabled,
			"gpuReclaim":     h.config.GPU.ReclaimEnabled,
			"commitments":    h.config.Commitments.Enabled,
			"aiGate":         h.config.AIGate.Enabled,
			"podPurger":      h.config.PodPurger.Enabled,
		},
		"autoApprove": map[string]bool{
			"rightsizer": h.config.Rightsizer.AutoApprove,
		},
	})
}

func (h *ConfigHandler) SetMode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode string `json:"mode"`
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	switch req.Mode {
	case "monitor", "recommend", "active":
		h.config.SetMode(req.Mode)
		h.settings.SaveMode(req.Mode)
		writeJSON(w, http.StatusOK, map[string]string{"mode": req.Mode})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid mode, must be monitor, recommend, or active"})
	}
}

func (h *ConfigHandler) SetPodPurger(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	h.config.SetControllerEnabled("podPurger", req.Enabled)
	h.settings.SavePodPurgerEnabled(req.Enabled)
	writeJSON(w, http.StatusOK, map[string]interface{}{"enabled": req.Enabled})
}

func (h *ConfigHandler) SetController(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	var req struct {
		Enabled bool `json:"enabled"`
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	if !h.config.SetControllerEnabled(name, req.Enabled) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown controller: " + name})
		return
	}

	h.settings.SaveControllerEnabled(name, req.Enabled)
	writeJSON(w, http.StatusOK, map[string]interface{}{"controller": name, "enabled": req.Enabled})
}

func (h *ConfigHandler) SetAutoApprove(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	var req struct {
		AutoApprove bool `json:"autoApprove"`
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	if !h.config.SetAutoApprove(name, req.AutoApprove) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "controller does not support autoApprove: " + name})
		return
	}

	h.settings.SaveAutoApprove(name, req.AutoApprove)
	writeJSON(w, http.StatusOK, map[string]interface{}{"controller": name, "autoApprove": req.AutoApprove})
}

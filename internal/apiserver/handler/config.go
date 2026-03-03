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
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"mode":          h.config.Mode,
		"cloudProvider": h.config.CloudProvider,
		"region":        h.config.Region,
		"clusterName":   h.config.ClusterName,
		"controllers": map[string]bool{
			"costMonitor":    h.config.CostMonitor.Enabled,
			"nodeAutoscaler": h.config.NodeAutoscaler.Enabled,
			"nodegroupMgr":   h.config.NodeGroupMgr.Enabled,
			"rightsizer":     h.config.Rightsizer.Enabled,
			"workloadScaler": h.config.WorkloadScaler.Enabled,
			"evictor":        h.config.Evictor.Enabled,
			"rebalancer":     h.config.Rebalancer.Enabled,
			"gpu":            h.config.GPU.Enabled,
			"gpuReclaim":     h.config.GPU.ReclaimEnabled,
			"commitments":    h.config.Commitments.Enabled,
			"aiGate":         h.config.AIGate.Enabled,
			"podPurger":      h.config.PodPurger.Enabled,
		},
		"dryRun": map[string]bool{
			"nodeAutoscaler": h.config.NodeAutoscaler.DryRun,
			"evictor":        h.config.Evictor.DryRun,
			"rebalancer":     h.config.Rebalancer.DryRun,
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
		h.config.Mode = req.Mode
		h.settings.SaveMode(req.Mode)
		writeJSON(w, http.StatusOK, map[string]string{"mode": h.config.Mode})
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

	h.config.PodPurger.Enabled = req.Enabled
	h.settings.SavePodPurgerEnabled(req.Enabled)
	writeJSON(w, http.StatusOK, map[string]interface{}{"enabled": h.config.PodPurger.Enabled})
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

	switch name {
	case "costMonitor":
		h.config.CostMonitor.Enabled = req.Enabled
	case "nodeAutoscaler":
		h.config.NodeAutoscaler.Enabled = req.Enabled
	case "nodegroupMgr":
		h.config.NodeGroupMgr.Enabled = req.Enabled
	case "rightsizer":
		h.config.Rightsizer.Enabled = req.Enabled
	case "workloadScaler":
		h.config.WorkloadScaler.Enabled = req.Enabled
	case "evictor":
		h.config.Evictor.Enabled = req.Enabled
	case "rebalancer":
		h.config.Rebalancer.Enabled = req.Enabled
	case "gpu":
		h.config.GPU.Enabled = req.Enabled
	case "gpuReclaim":
		h.config.GPU.ReclaimEnabled = req.Enabled
	case "commitments":
		h.config.Commitments.Enabled = req.Enabled
	case "aiGate":
		h.config.AIGate.Enabled = req.Enabled
	case "podPurger":
		h.config.PodPurger.Enabled = req.Enabled
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown controller: " + name})
		return
	}

	h.settings.SaveControllerEnabled(name, req.Enabled)
	writeJSON(w, http.StatusOK, map[string]interface{}{"controller": name, "enabled": req.Enabled})
}

func (h *ConfigHandler) SetControllerDryRun(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	var req struct {
		DryRun bool `json:"dryRun"`
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

	switch name {
	case "nodeAutoscaler":
		h.config.NodeAutoscaler.DryRun = req.DryRun
	case "evictor":
		h.config.Evictor.DryRun = req.DryRun
	case "rebalancer":
		h.config.Rebalancer.DryRun = req.DryRun
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "controller does not support dryRun: " + name})
		return
	}

	h.settings.SaveControllerDryRun(name, req.DryRun)
	writeJSON(w, http.StatusOK, map[string]interface{}{"controller": name, "dryRun": req.DryRun})
}

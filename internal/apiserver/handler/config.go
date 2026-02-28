package handler

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/koptimizer/koptimizer/internal/config"
)

type ConfigHandler struct {
	config *config.Config
}

func NewConfigHandler(cfg *config.Config) *ConfigHandler {
	return &ConfigHandler{config: cfg}
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
			"commitments":    h.config.Commitments.Enabled,
			"aiGate":         h.config.AIGate.Enabled,
			"podPurger":      h.config.PodPurger.Enabled,
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
	writeJSON(w, http.StatusOK, map[string]interface{}{"enabled": h.config.PodPurger.Enabled})
}

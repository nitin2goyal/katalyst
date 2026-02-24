package handler

import (
	"net/http"

	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/internal/state"
)

// PolicyHandler handles policy and node template queries.
type PolicyHandler struct {
	state *state.ClusterState
	cfg   *config.Config
}

// NewPolicyHandler creates a new PolicyHandler.
func NewPolicyHandler(st *state.ClusterState, cfg *config.Config) *PolicyHandler {
	return &PolicyHandler{state: st, cfg: cfg}
}

// Get returns scheduling policies and node templates derived from config and state.
func (h *PolicyHandler) Get(w http.ResponseWriter, r *http.Request) {
	// Node templates from discovered node groups
	type nodeTemplate struct {
		Name         string            `json:"name"`
		InstanceType string            `json:"instanceType,omitempty"`
		CapacityType string            `json:"capacityType"`
		MinNodes     int               `json:"minNodes"`
		MaxNodes     int               `json:"maxNodes"`
		Zones        []string          `json:"zones"`
		Labels       map[string]string `json:"labels,omitempty"`
	}

	groups := h.state.GetNodeGroups().GetAll()
	var templates []nodeTemplate
	for _, g := range groups {
		capacityType := g.Lifecycle
		if capacityType == "" {
			capacityType = "on-demand"
		}
		zones := []string{}
		if g.Zone != "" {
			zones = []string{g.Zone}
		}
		labels := g.Labels
		if labels == nil {
			labels = map[string]string{}
		}
		templates = append(templates, nodeTemplate{
			Name:         g.Name,
			InstanceType: g.InstanceType,
			CapacityType: capacityType,
			MinNodes:     g.MinCount,
			MaxNodes:     g.MaxCount,
			Zones:        zones,
			Labels:       labels,
		})
	}

	// Scheduling policies from config
	type policy struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Type        string `json:"type"`
		Target      string `json:"target"`
		Enabled     bool   `json:"enabled"`
	}

	var policies []policy

	if h.cfg.Rebalancer.Enabled {
		policies = append(policies, policy{
			Name:        "High Availability",
			Description: "Spread workloads across AZs via rebalancer",
			Type:        "topology-spread",
			Target:      "*/*",
			Enabled:     true,
		})
	}
	if h.cfg.GPU.Enabled {
		policies = append(policies, policy{
			Name:        "GPU Affinity",
			Description: "Schedule GPU workloads only on GPU nodes",
			Type:        "node-affinity",
			Target:      "gpu/*",
			Enabled:     true,
		})
	}
	if h.cfg.Spot.Enabled {
		policies = append(policies, policy{
			Name:        "Spot Tolerance",
			Description: "Allow non-critical workloads on spot instances",
			Type:        "toleration",
			Target:      "*/*",
			Enabled:     true,
		})
	}
	if h.cfg.Evictor.Enabled {
		policies = append(policies, policy{
			Name:        "Node Consolidation",
			Description: "Evict pods from underutilized nodes for bin-packing",
			Type:        "bin-packing",
			Target:      "*/*",
			Enabled:     true,
		})
	}
	// Always report cost-aware scheduling as it's a core feature
	policies = append(policies, policy{
		Name:        "Cost-aware Scheduling",
		Description: "Prefer cheaper nodes when resource requirements are flexible",
		Type:        "cost-aware",
		Target:      "*/*",
		Enabled:     h.cfg.CostMonitor.Enabled,
	})

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"nodeTemplates":      templates,
		"schedulingPolicies": policies,
	})
}

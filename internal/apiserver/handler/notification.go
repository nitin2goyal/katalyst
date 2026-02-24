package handler

import (
	"net/http"

	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/internal/state"
)

// NotificationHandler handles notification/alert queries.
type NotificationHandler struct {
	auditLog *state.AuditLog
	cfg      *config.Config
}

// NewNotificationHandler creates a new NotificationHandler.
func NewNotificationHandler(auditLog *state.AuditLog, cfg *config.Config) *NotificationHandler {
	return &NotificationHandler{auditLog: auditLog, cfg: cfg}
}

// severityCategory maps known action prefixes to their severity and category.
// Using a slice for deterministic iteration order (longest prefix first).
var actionClassification = []struct {
	prefix   string
	severity string
	category string
}{
	{"cost-anomaly", "critical", "cost"},
	{"oom", "critical", "reliability"},
	{"error", "critical", "system"},
	{"spot-convert", "info", "cost"},
	{"scale-down", "info", "autoscaling"},
	{"scale-up", "info", "autoscaling"},
	{"drain", "warning", "node-management"},
	{"evict", "warning", "node-management"},
	{"rightsize", "info", "optimization"},
	{"consolidate", "info", "optimization"},
	{"hibernate", "info", "scheduling"},
}

// classifyAction returns severity and category for a given action string.
func classifyAction(action string) (string, string) {
	for _, ac := range actionClassification {
		if len(action) >= len(ac.prefix) && action[:len(ac.prefix)] == ac.prefix {
			return ac.severity, ac.category
		}
	}
	return "info", "general"
}

// Get returns current alerts and configured notification channels.
func (h *NotificationHandler) Get(w http.ResponseWriter, r *http.Request) {
	events := h.auditLog.GetRecent(50)

	type alert struct {
		Timestamp string `json:"timestamp"`
		Severity  string `json:"severity"`
		Category  string `json:"category"`
		Message   string `json:"message"`
		Target    string `json:"target"`
		Status    string `json:"status"`
	}

	var alerts []alert
	for _, e := range events {
		severity, category := classifyAction(e.Action)
		alerts = append(alerts, alert{
			Timestamp: e.Timestamp.Format("2006-01-02T15:04:05Z"),
			Severity:  severity,
			Category:  category,
			Message:   e.Details,
			Target:    e.Target,
			Status:    "active",
		})
	}

	// Build channels from config
	type channel struct {
		Type    string `json:"type"`
		Name    string `json:"name"`
		Target  string `json:"target"`
		Enabled bool   `json:"enabled"`
	}
	var channels []channel

	if h.cfg.Alerts.SlackWebhookURL != "" {
		channels = append(channels, channel{
			Type:    "slack",
			Name:    "Slack",
			Target:  h.cfg.Alerts.SlackWebhookURL,
			Enabled: h.cfg.Alerts.Enabled,
		})
	}
	for _, email := range h.cfg.Alerts.EmailRecipients {
		channels = append(channels, channel{
			Type:    "email",
			Name:    "Email " + email,
			Target:  email,
			Enabled: h.cfg.Alerts.Enabled,
		})
	}
	for _, wh := range h.cfg.Alerts.Webhooks {
		channels = append(channels, channel{
			Type:    "webhook",
			Name:    "Webhook",
			Target:  wh,
			Enabled: h.cfg.Alerts.Enabled,
		})
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"alerts":   alerts,
		"channels": channels,
	})
}

package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

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
		ID      int    `json:"id"`
		Type    string `json:"type"`
		Name    string `json:"name"`
		Target  string `json:"target"`
		Enabled bool   `json:"enabled"`
		Static  bool   `json:"static"`
	}
	var channels []channel

	if h.cfg.Alerts.SlackWebhookURL != "" {
		channels = append(channels, channel{
			ID:      -1,
			Type:    "slack",
			Name:    "Slack",
			Target:  h.cfg.Alerts.SlackWebhookURL,
			Enabled: h.cfg.Alerts.Enabled,
			Static:  true,
		})
	}
	for _, email := range h.cfg.Alerts.EmailRecipients {
		channels = append(channels, channel{
			ID:      -1,
			Type:    "email",
			Name:    "Email " + email,
			Target:  email,
			Enabled: h.cfg.Alerts.Enabled,
			Static:  true,
		})
	}
	for _, wh := range h.cfg.Alerts.Webhooks {
		channels = append(channels, channel{
			ID:      -1,
			Type:    "webhook",
			Name:    "Webhook",
			Target:  wh,
			Enabled: h.cfg.Alerts.Enabled,
			Static:  true,
		})
	}

	// Append dynamically-added channels
	for i, ch := range h.cfg.Alerts.Channels {
		channels = append(channels, channel{
			ID:      i,
			Type:    ch.Type,
			Name:    ch.Name,
			Target:  ch.URL,
			Enabled: ch.Enabled,
			Static:  false,
		})
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"alerts":   alerts,
		"channels": channels,
	})
}

// AddChannel adds a new notification channel.
// POST /notifications/channels
func (h *NotificationHandler) AddChannel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Type string `json:"type"`
		Name string `json:"name"`
		URL  string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	req.Type = strings.TrimSpace(strings.ToLower(req.Type))
	req.Name = strings.TrimSpace(req.Name)
	req.URL = strings.TrimSpace(req.URL)

	if req.Type != "slack" && req.Type != "teams" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "type must be 'slack' or 'teams'"})
		return
	}
	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	if req.URL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url is required"})
		return
	}

	ch := config.NotificationChannel{
		Type:    req.Type,
		Name:    req.Name,
		URL:     req.URL,
		Enabled: true,
	}
	h.cfg.Alerts.Channels = append(h.cfg.Alerts.Channels, ch)
	idx := len(h.cfg.Alerts.Channels) - 1

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id":      idx,
		"type":    ch.Type,
		"name":    ch.Name,
		"url":     ch.URL,
		"enabled": ch.Enabled,
	})
}

// DeleteChannel removes a notification channel by index.
// DELETE /notifications/channels/{idx}
func (h *NotificationHandler) DeleteChannel(w http.ResponseWriter, r *http.Request) {
	idx, err := strconv.Atoi(chi.URLParam(r, "idx"))
	if err != nil || idx < 0 || idx >= len(h.cfg.Alerts.Channels) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "channel not found"})
		return
	}

	h.cfg.Alerts.Channels = append(h.cfg.Alerts.Channels[:idx], h.cfg.Alerts.Channels[idx+1:]...)

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ToggleChannel enables or disables a notification channel by index.
// PUT /notifications/channels/{idx}
func (h *NotificationHandler) ToggleChannel(w http.ResponseWriter, r *http.Request) {
	idx, err := strconv.Atoi(chi.URLParam(r, "idx"))
	if err != nil || idx < 0 || idx >= len(h.cfg.Alerts.Channels) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "channel not found"})
		return
	}

	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	h.cfg.Alerts.Channels[idx].Enabled = req.Enabled

	ch := h.cfg.Alerts.Channels[idx]
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id":      idx,
		"type":    ch.Type,
		"name":    ch.Name,
		"url":     ch.URL,
		"enabled": ch.Enabled,
	})
}

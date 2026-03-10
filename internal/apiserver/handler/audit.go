package handler

import (
	"encoding/json"
	"net/http"

	"github.com/koptimizer/koptimizer/internal/state"
)

// AuditHandler handles audit log API requests.
type AuditHandler struct {
	auditLog *state.AuditLog
}

// NewAuditHandler creates a new AuditHandler.
func NewAuditHandler(auditLog *state.AuditLog) *AuditHandler {
	return &AuditHandler{auditLog: auditLog}
}

// List returns all audit events in reverse chronological order.
func (h *AuditHandler) List(w http.ResponseWriter, r *http.Request) {
	events := h.auditLog.GetAll()
	if events == nil {
		events = []state.AuditEvent{}
	}
	page, pageSize := parsePagination(r)
	start, end, resp := paginateSlice(len(events), page, pageSize)
	resp.Data = events[start:end]
	writeJSON(w, http.StatusOK, resp)
}

// Record creates a new audit event from a dashboard user action.
func (h *AuditHandler) Record(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action  string `json:"action"`
		Target  string `json:"target"`
		User    string `json:"user"`
		Details string `json:"details"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid JSON body"})
		return
	}
	if req.Action == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "action is required"})
		return
	}
	if req.User == "" {
		req.User = "dashboard"
	}
	h.auditLog.Record(req.Action, req.Target, req.User, req.Details)
	writeJSON(w, http.StatusCreated, map[string]string{"status": "recorded"})
}

// ListEvents returns audit events wrapped in an object matching the dashboard format.
func (h *AuditHandler) ListEvents(w http.ResponseWriter, r *http.Request) {
	events := h.auditLog.GetAll()
	if events == nil {
		events = []state.AuditEvent{}
	}
	page, pageSize := parsePagination(r)
	start, end, resp := paginateSlice(len(events), page, pageSize)
	resp.Data = map[string]interface{}{
		"events": events[start:end],
	}
	writeJSON(w, http.StatusOK, resp)
}

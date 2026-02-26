package handler

import (
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

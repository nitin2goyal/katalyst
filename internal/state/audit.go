package state

import (
	"database/sql"
	"log/slog"
	"sync"
	"time"

	"github.com/koptimizer/koptimizer/internal/store"
)

// AuditEvent represents a single audit log entry.
type AuditEvent struct {
	Timestamp time.Time `json:"timestamp"`
	Action    string    `json:"action"`
	Target    string    `json:"target"`
	User      string    `json:"user"`
	Details   string    `json:"details"`
}

// AuditLog is a thread-safe ring buffer for audit events with optional SQLite
// persistence.
type AuditLog struct {
	mu     sync.RWMutex
	events []AuditEvent
	max    int
	db     *sql.DB
	writer *store.Writer
}

// NewAuditLog creates an audit log with the given max capacity (in-memory only).
func NewAuditLog(maxEvents int) *AuditLog {
	return &AuditLog{
		events: make([]AuditEvent, 0, maxEvents),
		max:    maxEvents,
	}
}

// NewAuditLogWithDB creates an audit log backed by SQLite. If db or writer is
// nil, it behaves identically to NewAuditLog.
func NewAuditLogWithDB(maxEvents int, db *sql.DB, writer *store.Writer) *AuditLog {
	return &AuditLog{
		events: make([]AuditEvent, 0, maxEvents),
		max:    maxEvents,
		db:     db,
		writer: writer,
	}
}

// Record adds a new audit event.
func (a *AuditLog) Record(action, target, user, details string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	event := AuditEvent{
		Timestamp: time.Now(),
		Action:    action,
		Target:    target,
		User:      user,
		Details:   details,
	}

	if len(a.events) >= a.max {
		// Shift left to make room
		copy(a.events, a.events[1:])
		a.events[len(a.events)-1] = event
	} else {
		a.events = append(a.events, event)
	}

	// Persist to SQLite via async writer
	if a.writer != nil {
		ts := event.Timestamp.Format(time.RFC3339)
		act, tgt, usr, det := event.Action, event.Target, event.User, event.Details
		a.writer.Enqueue(func(db *sql.DB) {
			if _, err := db.Exec(
				"INSERT INTO audit_events (timestamp, action, target, user, details) VALUES (?, ?, ?, ?, ?)",
				ts, act, tgt, usr, det,
			); err != nil {
				slog.Error("audit: insert event", "action", act, "error", err)
			}
		})
	}
}

// GetRecent returns the most recent n events in reverse chronological order.
// Always reads from in-memory for consistency (SQLite writes are async).
func (a *AuditLog) GetRecent(n int) []AuditEvent {
	a.mu.RLock()
	defer a.mu.RUnlock()

	count := len(a.events)
	if n > count {
		n = count
	}

	result := make([]AuditEvent, n)
	for i := 0; i < n; i++ {
		result[i] = a.events[count-1-i]
	}
	return result
}

// Flush ensures all pending audit events are written to SQLite before shutdown.
// This is a no-op if no async writer is configured.
func (a *AuditLog) Flush() {
	if a.writer != nil {
		a.writer.Drain()
	}
}

// GetAll returns all events in reverse chronological order.
// When backed by SQLite, it returns the full persisted history (for queries
// that need more than the in-memory ring buffer). Falls back to in-memory.
func (a *AuditLog) GetAll() []AuditEvent {
	if a.db != nil {
		if events := a.queryAll(); events != nil {
			return events
		}
	}

	a.mu.RLock()
	count := len(a.events)
	a.mu.RUnlock()

	return a.GetRecent(count)
}

func (a *AuditLog) queryAll() []AuditEvent {
	rows, err := a.db.Query(
		"SELECT timestamp, action, target, user, details FROM audit_events ORDER BY timestamp DESC LIMIT 10000",
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	return scanAuditRows(rows)
}

func scanAuditRows(rows *sql.Rows) []AuditEvent {
	var result []AuditEvent
	for rows.Next() {
		var e AuditEvent
		var ts string
		if err := rows.Scan(&ts, &e.Action, &e.Target, &e.User, &e.Details); err != nil {
			slog.Warn("audit: scan row", "error", err)
			continue
		}
		var err error
		e.Timestamp, err = time.Parse(time.RFC3339, ts)
		if err != nil {
			slog.Warn("audit: parse timestamp", "ts", ts, "error", err)
			continue
		}
		result = append(result, e)
	}
	return result
}

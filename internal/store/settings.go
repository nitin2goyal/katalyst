package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/koptimizer/koptimizer/internal/config"
)

// SettingsStore persists runtime settings (mode, pod purger, notification
// channels) to a SQLite key-value table so they survive pod restarts.
// All methods are nil-safe: if the underlying *sql.DB is nil, writes are
// silently dropped and reads return zero values.
type SettingsStore struct {
	db *sql.DB
}

// NewSettingsStore creates a SettingsStore backed by the given database.
// If db is nil the store operates as a no-op (all reads return defaults).
func NewSettingsStore(db *sql.DB) *SettingsStore {
	s := &SettingsStore{db: db}
	if db != nil {
		s.ensureTable()
	}
	return s
}

func (s *SettingsStore) ensureTable() {
	stmt := `CREATE TABLE IF NOT EXISTS settings (
		key        TEXT PRIMARY KEY,
		value      TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`
	if _, err := s.db.Exec(stmt); err != nil {
		fmt.Fprintf(os.Stderr, "settings: table init failed: %v\n", err)
	}
}

// ── private helpers ─────────────────────────────────────────────────────

func (s *SettingsStore) get(key string) (string, bool) {
	if s.db == nil {
		return "", false
	}
	var val string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&val)
	if err != nil {
		return "", false
	}
	return val, true
}

func (s *SettingsStore) set(key, value string) {
	if s.db == nil {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(
		`INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, now,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "settings: write failed for key %q: %v\n", key, err)
	}
}

// ── Mode ────────────────────────────────────────────────────────────────

const keyMode = "mode"

// LoadMode returns the persisted operating mode, or "" if none was saved.
func (s *SettingsStore) LoadMode() string {
	val, _ := s.get(keyMode)
	return val
}

// SaveMode persists the operating mode.
func (s *SettingsStore) SaveMode(mode string) {
	s.set(keyMode, mode)
}

// ── Pod Purger ──────────────────────────────────────────────────────────

const keyPodPurger = "pod_purger_enabled"

// LoadPodPurgerEnabled returns the persisted pod purger state.
// The second return value indicates whether a value was actually saved.
func (s *SettingsStore) LoadPodPurgerEnabled() (enabled bool, found bool) {
	val, ok := s.get(keyPodPurger)
	if !ok {
		return false, false
	}
	return val == "true", true
}

// SavePodPurgerEnabled persists the pod purger enabled state.
func (s *SettingsStore) SavePodPurgerEnabled(enabled bool) {
	v := "false"
	if enabled {
		v = "true"
	}
	s.set(keyPodPurger, v)
}

// ── Notification Channels ───────────────────────────────────────────────

const keyChannels = "notification_channels"

// LoadChannels returns the persisted notification channels, or nil if none
// were saved (or on any JSON decode error).
func (s *SettingsStore) LoadChannels() []config.NotificationChannel {
	val, ok := s.get(keyChannels)
	if !ok {
		return nil
	}
	var channels []config.NotificationChannel
	if err := json.Unmarshal([]byte(val), &channels); err != nil {
		fmt.Fprintf(os.Stderr, "settings: failed to decode channels: %v\n", err)
		return nil
	}
	return channels
}

// SaveChannels persists the notification channels as JSON.
func (s *SettingsStore) SaveChannels(channels []config.NotificationChannel) {
	data, err := json.Marshal(channels)
	if err != nil {
		fmt.Fprintf(os.Stderr, "settings: failed to encode channels: %v\n", err)
		return
	}
	s.set(keyChannels, string(data))
}

// ── Controller Enabled States ────────────────────────────────────────

const keyControllerPrefix = "controller_enabled:"

// ── Controller DryRun States ─────────────────────────────────────────

const keyDryRunPrefix = "controller_dryrun:"

// SaveControllerDryRun persists a controller's dry-run state.
func (s *SettingsStore) SaveControllerDryRun(name string, dryRun bool) {
	v := "false"
	if dryRun {
		v = "true"
	}
	s.set(keyDryRunPrefix+name, v)
}

// LoadControllerDryRunStates returns all persisted controller dry-run states.
func (s *SettingsStore) LoadControllerDryRunStates() map[string]bool {
	if s.db == nil {
		return nil
	}
	rows, err := s.db.Query(`SELECT key, value FROM settings WHERE key LIKE ?`, keyDryRunPrefix+"%")
	if err != nil {
		return nil
	}
	defer rows.Close()

	states := map[string]bool{}
	for rows.Next() {
		var key, val string
		if err := rows.Scan(&key, &val); err != nil {
			continue
		}
		name := key[len(keyDryRunPrefix):]
		states[name] = val == "true"
	}
	if len(states) == 0 {
		return nil
	}
	return states
}

// SaveControllerEnabled persists a single controller's enabled state.
func (s *SettingsStore) SaveControllerEnabled(name string, enabled bool) {
	v := "false"
	if enabled {
		v = "true"
	}
	s.set(keyControllerPrefix+name, v)
}

// LoadControllerStates returns all persisted controller enabled states.
// Only controllers that were explicitly saved are returned.
func (s *SettingsStore) LoadControllerStates() map[string]bool {
	if s.db == nil {
		return nil
	}
	rows, err := s.db.Query(`SELECT key, value FROM settings WHERE key LIKE ?`, keyControllerPrefix+"%")
	if err != nil {
		return nil
	}
	defer rows.Close()

	states := map[string]bool{}
	for rows.Next() {
		var key, val string
		if err := rows.Scan(&key, &val); err != nil {
			continue
		}
		name := key[len(keyControllerPrefix):]
		states[name] = val == "true"
	}
	if len(states) == 0 {
		return nil
	}
	return states
}

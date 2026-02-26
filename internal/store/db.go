package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Config holds database configuration.
type Config struct {
	Path          string
	RetentionDays int
}

// DB wraps a sql.DB with retention settings.
type DB struct {
	db            *sql.DB
	retentionDays int
}

// RawDB returns the underlying *sql.DB for components that need direct access.
func (d *DB) RawDB() *sql.DB {
	return d.db
}

// Close closes the underlying database connection.
func (d *DB) Close() error {
	return d.db.Close()
}

// Open creates the directory, opens the SQLite database, sets WAL mode and
// pragmas, and ensures all tables exist.
func Open(cfg Config) (*DB, error) {
	if cfg.Path == "" {
		return nil, fmt.Errorf("database path is empty")
	}

	dir := filepath.Dir(cfg.Path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("creating database directory: %w", err)
	}

	sqlDB, err := sql.Open("sqlite", cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// In WAL mode SQLite supports concurrent readers with a single writer.
	// Allow multiple connections so reads don't block behind writes.
	sqlDB.SetMaxOpenConns(4)
	sqlDB.SetMaxIdleConns(2)

	// Set pragmas for performance and concurrency.
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA synchronous=NORMAL",
	}
	for _, p := range pragmas {
		if _, err := sqlDB.Exec(p); err != nil {
			sqlDB.Close()
			return nil, fmt.Errorf("setting pragma %q: %w", p, err)
		}
	}

	if err := createTables(sqlDB); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("creating tables: %w", err)
	}

	retDays := cfg.RetentionDays
	if retDays <= 0 {
		retDays = 90
	}

	d := &DB{db: sqlDB, retentionDays: retDays}

	// Run cleanup at startup so old data is purged even if the pod never
	// lives long enough for the periodic ticker to fire.
	if err := d.Cleanup(); err != nil {
		fmt.Fprintf(os.Stderr, "store: startup cleanup failed (non-fatal): %v\n", err)
	}

	return d, nil
}

func createTables(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS audit_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp TEXT NOT NULL,
			action TEXT NOT NULL,
			target TEXT NOT NULL,
			user TEXT NOT NULL,
			details TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_timestamp ON audit_events(timestamp)`,

		`CREATE TABLE IF NOT EXISTS cost_snapshots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			date TEXT NOT NULL UNIQUE,
			total_monthly_cost_usd REAL NOT NULL
		)`,

		`CREATE TABLE IF NOT EXISTS cost_by_namespace (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			date TEXT NOT NULL,
			namespace TEXT NOT NULL,
			cost_usd REAL NOT NULL,
			UNIQUE(date, namespace)
		)`,

		`CREATE TABLE IF NOT EXISTS cost_by_nodegroup (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			date TEXT NOT NULL,
			nodegroup TEXT NOT NULL,
			cost_usd REAL NOT NULL,
			UNIQUE(date, nodegroup)
		)`,

		`CREATE TABLE IF NOT EXISTS node_metrics (
			id INTEGER PRIMARY KEY,
			timestamp INTEGER NOT NULL,
			node_name TEXT NOT NULL,
			cpu_usage INTEGER NOT NULL,
			memory_usage INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_node_metrics_name_ts ON node_metrics(node_name, timestamp)`,

		`CREATE TABLE IF NOT EXISTS pod_metrics (
			id INTEGER PRIMARY KEY,
			timestamp INTEGER NOT NULL,
			namespace TEXT NOT NULL,
			pod_name TEXT NOT NULL,
			container TEXT NOT NULL,
			cpu_usage INTEGER NOT NULL,
			memory_usage INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_pod_metrics_key_ts ON pod_metrics(namespace, pod_name, container, timestamp)`,

		`CREATE TABLE IF NOT EXISTS cost_snapshots_hourly (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			datetime_hour TEXT NOT NULL UNIQUE,
			total_monthly_cost_usd REAL NOT NULL
		)`,

		`CREATE TABLE IF NOT EXISTS cluster_snapshots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp INTEGER NOT NULL,
			node_count INTEGER NOT NULL,
			pod_count INTEGER NOT NULL,
			total_cpu_capacity INTEGER NOT NULL,
			total_cpu_used INTEGER NOT NULL,
			total_memory_capacity INTEGER NOT NULL,
			total_memory_used INTEGER NOT NULL,
			total_monthly_cost REAL NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_cluster_snapshots_ts ON cluster_snapshots(timestamp)`,
	}

	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("executing %q: %w", stmt[:40], err)
		}
	}
	return nil
}

// Cleanup deletes audit/cost records older than retentionDays and metrics
// older than 7 days.
func (d *DB) Cleanup() error {
	retentionCutoff := time.Now().AddDate(0, 0, -d.retentionDays).Format(time.RFC3339)
	metricsCutoff := time.Now().Add(-7 * 24 * time.Hour).Unix()
	dateCutoff := time.Now().AddDate(0, 0, -d.retentionDays).Format("2006-01-02")

	stmts := []struct {
		sql    string
		cutoff any
	}{
		{"DELETE FROM audit_events WHERE timestamp < ?", retentionCutoff},
		{"DELETE FROM cost_snapshots WHERE date < ?", dateCutoff},
		{"DELETE FROM cost_by_namespace WHERE date < ?", dateCutoff},
		{"DELETE FROM cost_by_nodegroup WHERE date < ?", dateCutoff},
		{"DELETE FROM node_metrics WHERE timestamp < ?", metricsCutoff},
		{"DELETE FROM pod_metrics WHERE timestamp < ?", metricsCutoff},
		{"DELETE FROM cost_snapshots_hourly WHERE datetime_hour < ?", time.Now().AddDate(0, 0, -d.retentionDays).Format("2006-01-02T15")},
		{"DELETE FROM cluster_snapshots WHERE timestamp < ?", metricsCutoff},
	}

	for _, s := range stmts {
		if _, err := d.db.Exec(s.sql, s.cutoff); err != nil {
			return fmt.Errorf("cleanup %q: %w", s.sql[:30], err)
		}
	}
	return nil
}

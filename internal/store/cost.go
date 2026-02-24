package store

import (
	"context"
	"database/sql"
	"log/slog"
	"time"
)

// CostSnapshot represents a daily cost data point.
type CostSnapshot struct {
	Date             string  `json:"date"`
	TotalMonthlyCost float64 `json:"totalMonthlyCostUSD"`
}

// CostStore persists daily cost snapshots to SQLite.
type CostStore struct {
	db *sql.DB
}

// NewCostStore creates a CostStore. db may be nil (all ops become no-ops).
func NewCostStore(db *sql.DB) *CostStore {
	return &CostStore{db: db}
}

// RecordDailySnapshot upserts today's cost totals and per-namespace/nodegroup
// breakdowns atomically within a single transaction.
func (s *CostStore) RecordDailySnapshot(total float64, costByNS map[string]float64, costByNG map[string]float64) {
	if s.db == nil {
		return
	}

	today := time.Now().Format("2006-01-02")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		slog.Error("cost snapshot: begin tx", "error", err)
		return
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	// Upsert total
	if _, err := tx.Exec(
		"INSERT INTO cost_snapshots (date, total_monthly_cost_usd) VALUES (?, ?) ON CONFLICT(date) DO UPDATE SET total_monthly_cost_usd = excluded.total_monthly_cost_usd",
		today, total,
	); err != nil {
		slog.Error("cost snapshot: upsert total", "error", err)
		return
	}

	// Upsert per-namespace
	for ns, cost := range costByNS {
		if _, err := tx.Exec(
			"INSERT INTO cost_by_namespace (date, namespace, cost_usd) VALUES (?, ?, ?) ON CONFLICT(date, namespace) DO UPDATE SET cost_usd = excluded.cost_usd",
			today, ns, cost,
		); err != nil {
			slog.Error("cost snapshot: upsert namespace", "namespace", ns, "error", err)
			return
		}
	}

	// Upsert per-nodegroup
	for ng, cost := range costByNG {
		if _, err := tx.Exec(
			"INSERT INTO cost_by_nodegroup (date, nodegroup, cost_usd) VALUES (?, ?, ?) ON CONFLICT(date, nodegroup) DO UPDATE SET cost_usd = excluded.cost_usd",
			today, ng, cost,
		); err != nil {
			slog.Error("cost snapshot: upsert nodegroup", "nodegroup", ng, "error", err)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		slog.Error("cost snapshot: commit tx", "error", err)
	}
}

// GetByNamespaceForPeriod returns average cost per namespace for the given date range.
func (s *CostStore) GetByNamespaceForPeriod(start, end time.Time) map[string]float64 {
	if s.db == nil {
		return nil
	}

	startStr := start.Format("2006-01-02")
	endStr := end.Format("2006-01-02")

	rows, err := s.db.Query(
		"SELECT namespace, AVG(cost_usd) FROM cost_by_namespace WHERE date >= ? AND date < ? GROUP BY namespace",
		startStr, endStr,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()

	result := make(map[string]float64)
	for rows.Next() {
		var ns string
		var avg float64
		if err := rows.Scan(&ns, &avg); err != nil {
			continue
		}
		result[ns] = avg
	}
	return result
}

// GetTrend returns cost snapshots for the last N days, ordered by date ascending.
func (s *CostStore) GetTrend(days int) []CostSnapshot {
	if s.db == nil {
		return nil
	}

	cutoff := time.Now().AddDate(0, 0, -days).Format("2006-01-02")
	rows, err := s.db.Query(
		"SELECT date, total_monthly_cost_usd FROM cost_snapshots WHERE date >= ? ORDER BY date ASC",
		cutoff,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var result []CostSnapshot
	for rows.Next() {
		var cs CostSnapshot
		if err := rows.Scan(&cs.Date, &cs.TotalMonthlyCost); err != nil {
			continue
		}
		result = append(result, cs)
	}
	return result
}

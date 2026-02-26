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

// CostSnapshotHourly represents an hourly cost data point.
type CostSnapshotHourly struct {
	DatetimeHour     string  `json:"datetimeHour"`
	TotalMonthlyCost float64 `json:"totalMonthlyCostUSD"`
}

// ClusterSnapshot represents a point-in-time cluster resource snapshot.
type ClusterSnapshot struct {
	Timestamp         int64   `json:"timestamp"`
	NodeCount         int     `json:"nodeCount"`
	PodCount          int     `json:"podCount"`
	TotalCPUCapacity  int64   `json:"totalCPUCapacity"`
	TotalCPUUsed      int64   `json:"totalCPUUsed"`
	TotalMemCapacity  int64   `json:"totalMemoryCapacity"`
	TotalMemUsed      int64   `json:"totalMemoryUsed"`
	TotalMonthlyCost  float64 `json:"totalMonthlyCost"`
}

// RecordHourlySnapshot upserts the current hour's cost snapshot.
func (s *CostStore) RecordHourlySnapshot(totalMonthlyCost float64) {
	if s.db == nil {
		return
	}
	hour := time.Now().Format("2006-01-02T15")
	if _, err := s.db.Exec(
		"INSERT INTO cost_snapshots_hourly (datetime_hour, total_monthly_cost_usd) VALUES (?, ?) ON CONFLICT(datetime_hour) DO UPDATE SET total_monthly_cost_usd = excluded.total_monthly_cost_usd",
		hour, totalMonthlyCost,
	); err != nil {
		slog.Error("cost hourly snapshot: upsert", "error", err)
	}
}

// RecordClusterSnapshot inserts a cluster resource snapshot.
func (s *CostStore) RecordClusterSnapshot(nodeCount, podCount int, cpuCap, cpuUsed, memCap, memUsed int64, monthlyCost float64) {
	if s.db == nil {
		return
	}
	ts := time.Now().Unix()
	if _, err := s.db.Exec(
		"INSERT INTO cluster_snapshots (timestamp, node_count, pod_count, total_cpu_capacity, total_cpu_used, total_memory_capacity, total_memory_used, total_monthly_cost) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		ts, nodeCount, podCount, cpuCap, cpuUsed, memCap, memUsed, monthlyCost,
	); err != nil {
		slog.Error("cluster snapshot: insert", "error", err)
	}
}

// GetHourlyTrend returns cost snapshots for the last N hours, ordered ascending.
func (s *CostStore) GetHourlyTrend(hours int) []CostSnapshotHourly {
	if s.db == nil {
		return nil
	}
	cutoff := time.Now().Add(-time.Duration(hours) * time.Hour).Format("2006-01-02T15")
	rows, err := s.db.Query(
		"SELECT datetime_hour, total_monthly_cost_usd FROM cost_snapshots_hourly WHERE datetime_hour >= ? ORDER BY datetime_hour ASC",
		cutoff,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var result []CostSnapshotHourly
	for rows.Next() {
		var cs CostSnapshotHourly
		if err := rows.Scan(&cs.DatetimeHour, &cs.TotalMonthlyCost); err != nil {
			continue
		}
		result = append(result, cs)
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

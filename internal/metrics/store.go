package metrics

import (
	"database/sql"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/koptimizer/koptimizer/internal/store"
	pkgmetrics "github.com/koptimizer/koptimizer/pkg/metrics"
)

// maxPodSeriesKeys caps the number of unique pod series to prevent unbounded
// memory growth from churned pods. When exceeded, Cleanup() prunes the oldest.
const maxPodSeriesKeys = 100_000

// Store is an in-memory time-series store for metrics with optional SQLite
// persistence.
type Store struct {
	mu         sync.RWMutex
	nodeSeries map[string][]dataPoint // nodeName -> time series
	podSeries  map[string][]dataPoint // namespace/name/container -> time series
	retention  time.Duration
	db         *sql.DB
	writer     *store.Writer
}

type dataPoint struct {
	Timestamp   time.Time
	CPUUsage    int64
	MemoryUsage int64
}

// NewStore creates a new metrics Store (in-memory only).
func NewStore(retention time.Duration) *Store {
	return &Store{
		nodeSeries: make(map[string][]dataPoint),
		podSeries:  make(map[string][]dataPoint),
		retention:  retention,
	}
}

// NewStoreWithDB creates a metrics Store backed by SQLite. On startup it
// hydrates in-memory maps from the database. If db is nil, it behaves
// identically to NewStore.
func NewStoreWithDB(retention time.Duration, db *sql.DB, writer *store.Writer) *Store {
	s := &Store{
		nodeSeries: make(map[string][]dataPoint),
		podSeries:  make(map[string][]dataPoint),
		retention:  retention,
		db:         db,
		writer:     writer,
	}
	if db != nil {
		s.loadFromDB()
	}
	return s
}

// loadFromDB hydrates in-memory maps from SQLite within the retention window.
func (s *Store) loadFromDB() {
	cutoff := time.Now().Add(-s.retention).Unix()

	// Load node metrics
	s.loadNodeMetrics(cutoff)

	// Load pod metrics
	s.loadPodMetrics(cutoff)
}

func (s *Store) loadNodeMetrics(cutoff int64) {
	rows, err := s.db.Query(
		"SELECT timestamp, node_name, cpu_usage, memory_usage FROM node_metrics WHERE timestamp >= ? ORDER BY timestamp ASC",
		cutoff,
	)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var tsUnix int64
		var name string
		var dp dataPoint
		if err := rows.Scan(&tsUnix, &name, &dp.CPUUsage, &dp.MemoryUsage); err != nil {
			slog.Warn("metrics: scan node_metrics row", "error", err)
			continue
		}
		dp.Timestamp = time.Unix(tsUnix, 0)
		s.nodeSeries[name] = append(s.nodeSeries[name], dp)
	}
}

func (s *Store) loadPodMetrics(cutoff int64) {
	rows, err := s.db.Query(
		"SELECT timestamp, namespace, pod_name, container, cpu_usage, memory_usage FROM pod_metrics WHERE timestamp >= ? ORDER BY timestamp ASC",
		cutoff,
	)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var tsUnix int64
		var ns, pod, container string
		var dp dataPoint
		if err := rows.Scan(&tsUnix, &ns, &pod, &container, &dp.CPUUsage, &dp.MemoryUsage); err != nil {
			slog.Warn("metrics: scan pod_metrics row", "error", err)
			continue
		}
		dp.Timestamp = time.Unix(tsUnix, 0)
		key := ns + "/" + pod + "/" + container
		s.podSeries[key] = append(s.podSeries[key], dp)
	}
}

// RecordNodeMetrics stores a node metrics data point.
func (s *Store) RecordNodeMetrics(m pkgmetrics.NodeMetrics) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodeSeries[m.Name] = append(s.nodeSeries[m.Name], dataPoint{
		Timestamp:   m.Timestamp,
		CPUUsage:    m.CPUUsage,
		MemoryUsage: m.MemoryUsage,
	})
	s.evict(s.nodeSeries, m.Name)

	if s.writer != nil {
		ts := m.Timestamp.Unix()
		name, cpu, mem := m.Name, m.CPUUsage, m.MemoryUsage
		s.writer.Enqueue(func(db *sql.DB) {
			if _, err := db.Exec(
				"INSERT INTO node_metrics (timestamp, node_name, cpu_usage, memory_usage) VALUES (?, ?, ?, ?)",
				ts, name, cpu, mem,
			); err != nil {
				slog.Error("metrics: insert node_metrics", "node", name, "error", err)
			}
		})
	}
}

// RecordPodMetrics stores a pod metrics data point.
func (s *Store) RecordPodMetrics(m pkgmetrics.PodMetrics) {
	s.mu.Lock()
	defer s.mu.Unlock()

	type containerRow struct {
		ts        int64
		ns, pod   string
		container string
		cpu, mem  int64
	}
	var rows []containerRow

	for _, c := range m.Containers {
		key := m.Namespace + "/" + m.Name + "/" + c.Name
		s.podSeries[key] = append(s.podSeries[key], dataPoint{
			Timestamp:   m.Timestamp,
			CPUUsage:    c.CPUUsage,
			MemoryUsage: c.MemoryUsage,
		})
		s.evict(s.podSeries, key)

		if s.writer != nil {
			rows = append(rows, containerRow{
				ts:        m.Timestamp.Unix(),
				ns:        m.Namespace,
				pod:       m.Name,
				container: c.Name,
				cpu:       c.CPUUsage,
				mem:       c.MemoryUsage,
			})
		}
	}

	// Single enqueue for all containers in this pod
	if s.writer != nil && len(rows) > 0 {
		s.writer.Enqueue(func(db *sql.DB) {
			for _, r := range rows {
				if _, err := db.Exec(
					"INSERT INTO pod_metrics (timestamp, namespace, pod_name, container, cpu_usage, memory_usage) VALUES (?, ?, ?, ?, ?, ?)",
					r.ts, r.ns, r.pod, r.container, r.cpu, r.mem,
				); err != nil {
					slog.Error("metrics: insert pod_metrics", "pod", r.ns+"/"+r.pod, "error", err)
				}
			}
		})
	}
}

// GetNodeWindow returns the metrics window for a node over the given duration.
func (s *Store) GetNodeWindow(name string, duration time.Duration) *pkgmetrics.MetricsWindow {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.computeWindow(s.nodeSeries[name], duration)
}

// GetPodContainerWindow returns the metrics window for a pod container.
func (s *Store) GetPodContainerWindow(namespace, pod, container string, duration time.Duration) *pkgmetrics.MetricsWindow {
	s.mu.RLock()
	defer s.mu.RUnlock()
	key := namespace + "/" + pod + "/" + container
	return s.computeWindow(s.podSeries[key], duration)
}

func (s *Store) computeWindow(points []dataPoint, duration time.Duration) *pkgmetrics.MetricsWindow {
	if len(points) == 0 {
		return nil
	}

	cutoff := time.Now().Add(-duration)
	var filtered []dataPoint
	for _, p := range points {
		if p.Timestamp.After(cutoff) {
			filtered = append(filtered, p)
		}
	}

	if len(filtered) == 0 {
		return nil
	}

	cpuValues := make([]int64, len(filtered))
	memValues := make([]int64, len(filtered))
	for i, p := range filtered {
		cpuValues[i] = p.CPUUsage
		memValues[i] = p.MemoryUsage
	}

	return &pkgmetrics.MetricsWindow{
		Start:      filtered[0].Timestamp,
		End:        filtered[len(filtered)-1].Timestamp,
		DataPoints: len(filtered),
		P50CPU:     percentile(cpuValues, 50),
		P95CPU:     percentile(cpuValues, 95),
		P99CPU:     percentile(cpuValues, 99),
		MaxCPU:     maxVal(cpuValues),
		P50Memory:  percentile(memValues, 50),
		P95Memory:  percentile(memValues, 95),
		P99Memory:  percentile(memValues, 99),
		MaxMemory:  maxVal(memValues),
	}
}

func (s *Store) evict(series map[string][]dataPoint, key string) {
	cutoff := time.Now().Add(-s.retention)
	points := series[key]
	i := 0
	for i < len(points) && points[i].Timestamp.Before(cutoff) {
		i++
	}
	if i > 0 {
		remaining := points[i:]
		if len(remaining) == 0 {
			delete(series, key) // Clean up empty entries immediately
		} else {
			series[key] = remaining
		}
	}
}

// Cleanup removes series keys that have no data points within the retention
// window, and enforces the maxPodSeriesKeys cap to prevent unbounded memory
// growth from churned pods. Call this periodically (e.g. hourly).
func (s *Store) Cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := time.Now().Add(-s.retention)
	for key, points := range s.nodeSeries {
		if len(points) == 0 || points[len(points)-1].Timestamp.Before(cutoff) {
			delete(s.nodeSeries, key)
		}
	}
	for key, points := range s.podSeries {
		if len(points) == 0 || points[len(points)-1].Timestamp.Before(cutoff) {
			delete(s.podSeries, key)
		}
	}

	// Safety cap: if pod series keys still exceed the maximum after
	// retention-based cleanup, evict the oldest entries first.
	if len(s.podSeries) > maxPodSeriesKeys {
		type keyAge struct {
			key string
			ts  time.Time
		}
		entries := make([]keyAge, 0, len(s.podSeries))
		for k, pts := range s.podSeries {
			if len(pts) > 0 {
				entries = append(entries, keyAge{k, pts[len(pts)-1].Timestamp})
			} else {
				delete(s.podSeries, k)
			}
		}
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].ts.Before(entries[j].ts)
		})
		toRemove := len(entries) - maxPodSeriesKeys
		for i := 0; i < toRemove; i++ {
			delete(s.podSeries, entries[i].key)
		}
		slog.Info("metrics: evicted stale pod series to enforce cap",
			"removed", toRemove, "remaining", len(s.podSeries))
	}
}

func percentile(values []int64, pct int) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := make([]int64, len(values))
	copy(sorted, values)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := (pct * len(sorted)) / 100
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func maxVal(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	m := values[0]
	for _, v := range values[1:] {
		if v > m {
			m = v
		}
	}
	return m
}

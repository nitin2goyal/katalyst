package alerts

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/smtp"
	"os"
	"sync"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/koptimizer/koptimizer/internal/config"
	intmetrics "github.com/koptimizer/koptimizer/internal/metrics"
	"github.com/koptimizer/koptimizer/internal/state"
	"github.com/koptimizer/koptimizer/internal/store"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// Severity levels for alerts.
const (
	SeverityCritical = "critical"
	SeverityWarning  = "warning"
	SeverityInfo     = "info"
)

// Alert represents a fired alert.
type Alert struct {
	Type      string    `json:"type"`
	Severity  string    `json:"severity"`
	Title     string    `json:"title"`
	Message   string    `json:"message"`
	Timestamp time.Time `json:"timestamp"`
	Value     float64   `json:"value,omitempty"`
	Threshold float64   `json:"threshold,omitempty"`
}

// Controller monitors cost metrics for anomalies and fires alerts via
// configured channels (Slack, email).
type Controller struct {
	config *config.Config
	state  *state.ClusterState
	db     *sql.DB // may be nil
	writer *store.Writer // async writer to avoid direct SQLite contention

	mu             sync.Mutex
	costHistory    []float64  // rolling window of daily cost samples
	lastAlertTime  map[string]time.Time // alert type -> last fire time
	httpClient     *http.Client
}

func NewController(mgr ctrl.Manager, st *state.ClusterState, cfg *config.Config, db *sql.DB, writer ...*store.Writer) *Controller {
	c := &Controller{
		config:        cfg,
		state:         st,
		db:            db,
		costHistory:   make([]float64, 0, 90),
		lastAlertTime: make(map[string]time.Time),
		httpClient:    &http.Client{Timeout: 10 * time.Second},
	}
	if len(writer) > 0 {
		c.writer = writer[0]
	}
	if db != nil {
		c.initCostHistoryTable()
		c.loadCostHistory()
	}
	return c
}

func (c *Controller) SetupWithManager(mgr ctrl.Manager) error {
	return mgr.Add(c)
}

// Start implements manager.Runnable.
func (c *Controller) Start(ctx context.Context) error {
	c.run(ctx)
	return nil
}

func (c *Controller) Name() string { return "alerts" }

func (c *Controller) Analyze(ctx context.Context, snapshot *optimizer.ClusterSnapshot) ([]optimizer.Recommendation, error) {
	var recs []optimizer.Recommendation

	// Calculate current hourly cost
	var currentHourlyCost float64
	for _, node := range snapshot.Nodes {
		currentHourlyCost += node.HourlyCostUSD
	}
	dailyCost := currentHourlyCost * 24

	c.mu.Lock()
	defer c.mu.Unlock()

	// Add to cost history
	c.costHistory = append(c.costHistory, dailyCost)
	if len(c.costHistory) > 90 {
		c.costHistory = c.costHistory[1:]
	}
	c.persistCostSample(dailyCost)

	// Need at least 7 data points for anomaly detection
	if len(c.costHistory) < 7 {
		return nil, nil
	}

	// Detect cost anomaly using Z-score
	mean, stddev := meanStdDev(c.costHistory[:len(c.costHistory)-1])
	if stddev > 0 {
		zscore := (dailyCost - mean) / stddev
		threshold := c.config.Alerts.CostAnomalyStdDev

		if zscore > threshold {
			alertKey := "cost-spike"
			if c.canFireAlert(alertKey) {
				alert := Alert{
					Type:      "cost-anomaly",
					Severity:  SeverityWarning,
					Title:     "Cost Anomaly Detected",
					Message:   fmt.Sprintf("Daily cost $%.2f is %.1f standard deviations above the mean ($%.2f). This is a %.0f%% increase.", dailyCost, zscore, mean, (dailyCost-mean)/mean*100),
					Timestamp: time.Now(),
					Value:     dailyCost,
					Threshold: mean + threshold*stddev,
				}
				c.fireAlert(ctx, alert)
				c.lastAlertTime[alertKey] = time.Now()

				recs = append(recs, optimizer.Recommendation{
					ID:             fmt.Sprintf("cost-anomaly-%d", time.Now().Unix()),
					Type:           optimizer.RecommendationCostAnomaly,
					Priority:       optimizer.PriorityHigh,
					AutoExecutable: false,
					TargetKind:     "Cluster",
					TargetName:     c.config.ClusterName,
					Summary:        alert.Message,
					ActionSteps: []string{
						"Review recent scaling events and new workloads",
						"Check for runaway deployments or resource leaks",
						"Verify spot instance pricing hasn't spiked",
					},
					Details: map[string]string{
						"action":    "investigate-cost-anomaly",
						"dailyCost": fmt.Sprintf("%.2f", dailyCost),
						"meanCost":  fmt.Sprintf("%.2f", mean),
						"zscore":    fmt.Sprintf("%.2f", zscore),
					},
				})
			}
		}

		if zscore < -threshold {
			alertKey := "cost-drop"
			if c.canFireAlert(alertKey) {
				alert := Alert{
					Type:      "cost-anomaly",
					Severity:  SeverityInfo,
					Title:     "Cost Drop Detected",
					Message:   fmt.Sprintf("Daily cost $%.2f is %.1f standard deviations below the mean ($%.2f). Verify this is expected.", dailyCost, -zscore, mean),
					Timestamp: time.Now(),
					Value:     dailyCost,
					Threshold: mean - threshold*stddev,
				}
				c.fireAlert(ctx, alert)
				c.lastAlertTime[alertKey] = time.Now()
			}
		}
	}

	// Check for nodes with very high utilization (potential capacity alert)
	highUtilNodes := 0
	for _, node := range snapshot.Nodes {
		if node.CPUCapacity > 0 {
			util := float64(node.CPUUsed) / float64(node.CPUCapacity) * 100
			if util > 95 {
				highUtilNodes++
			}
		}
	}
	if highUtilNodes > 0 && float64(highUtilNodes)/float64(len(snapshot.Nodes)) > 0.5 {
		alertKey := "capacity-pressure"
		if c.canFireAlert(alertKey) {
			alert := Alert{
				Type:     "capacity",
				Severity: SeverityCritical,
				Title:    "Cluster Capacity Pressure",
				Message:  fmt.Sprintf("%d of %d nodes are above 95%% CPU utilization", highUtilNodes, len(snapshot.Nodes)),
			}
			c.fireAlert(ctx, alert)
			c.lastAlertTime[alertKey] = time.Now()
		}
	}

	return recs, nil
}

func (c *Controller) Execute(ctx context.Context, rec optimizer.Recommendation) error {
	return nil
}

func (c *Controller) canFireAlert(key string) bool {
	last, ok := c.lastAlertTime[key]
	if !ok {
		return true
	}
	cooldown := time.Duration(c.config.Alerts.CooldownMinutes) * time.Minute
	return time.Since(last) > cooldown
}

func (c *Controller) fireAlert(ctx context.Context, alert Alert) {
	logger := log.FromContext(ctx).WithName("alerts")

	intmetrics.AlertsFired.WithLabelValues(alert.Type, alert.Severity).Inc()

	// Send to Slack
	if c.config.Alerts.SlackWebhookURL != "" {
		if err := c.sendSlack(ctx, alert); err != nil {
			logger.Error(err, "Failed to send Slack alert")
		}
	}

	// Send email alerts
	if len(c.config.Alerts.EmailRecipients) > 0 {
		if err := c.sendEmail(ctx, alert); err != nil {
			logger.Error(err, "Failed to send email alert")
		}
	}

	logger.Info("Alert fired",
		"type", alert.Type,
		"severity", alert.Severity,
		"title", alert.Title,
		"message", alert.Message,
	)
}

func (c *Controller) sendSlack(ctx context.Context, alert Alert) error {
	// Map severity to Slack color
	color := "#36a64f" // green
	switch alert.Severity {
	case SeverityCritical:
		color = "#ff0000"
	case SeverityWarning:
		color = "#ffcc00"
	}

	payload := map[string]interface{}{
		"attachments": []map[string]interface{}{
			{
				"color":  color,
				"title":  fmt.Sprintf("[KOptimizer] %s", alert.Title),
				"text":   alert.Message,
				"footer": "KOptimizer Cost Alerts",
				"ts":     alert.Timestamp.Unix(),
			},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshaling Slack payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.Alerts.SlackWebhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating Slack request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending Slack alert: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("Slack returned status %d", resp.StatusCode)
	}

	return nil
}

func (c *Controller) sendEmail(ctx context.Context, alert Alert) error {
	smtpHost := os.Getenv("KOPTIMIZER_SMTP_HOST")
	smtpPort := os.Getenv("KOPTIMIZER_SMTP_PORT")
	smtpFrom := os.Getenv("KOPTIMIZER_SMTP_FROM")

	if smtpHost == "" || smtpFrom == "" {
		return fmt.Errorf("KOPTIMIZER_SMTP_HOST and KOPTIMIZER_SMTP_FROM must be set for email alerts")
	}
	if smtpPort == "" {
		smtpPort = "587"
	}

	subject := fmt.Sprintf("[KOptimizer] %s: %s", alert.Severity, alert.Title)
	body := fmt.Sprintf("Severity: %s\nType: %s\nTime: %s\n\n%s",
		alert.Severity, alert.Type, alert.Timestamp.Format(time.RFC3339), alert.Message)

	if alert.Value > 0 {
		body += fmt.Sprintf("\n\nCurrent Value: $%.2f\nThreshold: $%.2f", alert.Value, alert.Threshold)
	}

	for _, recipient := range c.config.Alerts.EmailRecipients {
		msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s",
			smtpFrom, recipient, subject, body)

		smtpUser := os.Getenv("KOPTIMIZER_SMTP_USER")
		smtpPass := os.Getenv("KOPTIMIZER_SMTP_PASS")

		var auth smtp.Auth
		if smtpUser != "" && smtpPass != "" {
			auth = smtp.PlainAuth("", smtpUser, smtpPass, smtpHost)
		}

		addr := smtpHost + ":" + smtpPort
		if err := smtp.SendMail(addr, auth, smtpFrom, []string{recipient}, []byte(msg)); err != nil {
			return fmt.Errorf("sending email to %s: %w", recipient, err)
		}
	}

	return nil
}

func (c *Controller) initCostHistoryTable() {
	if c.db == nil {
		return
	}
	logger := log.Log.WithName("alerts")
	if _, err := c.db.Exec(`CREATE TABLE IF NOT EXISTS cost_history (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		timestamp INTEGER NOT NULL,
		daily_cost REAL NOT NULL
	)`); err != nil {
		logger.Error(err, "Failed to create cost_history table")
	}
	if _, err := c.db.Exec(`CREATE INDEX IF NOT EXISTS idx_cost_history_ts ON cost_history(timestamp)`); err != nil {
		logger.Error(err, "Failed to create cost_history index")
	}
}

func (c *Controller) loadCostHistory() {
	if c.db == nil {
		return
	}
	// Load last 90 samples
	rows, err := c.db.Query(`SELECT daily_cost FROM cost_history ORDER BY timestamp DESC LIMIT 90`)
	if err != nil {
		return
	}
	defer rows.Close()

	var costs []float64
	for rows.Next() {
		var cost float64
		if err := rows.Scan(&cost); err != nil {
			continue
		}
		costs = append(costs, cost)
	}
	// Reverse to get chronological order
	for i, j := 0, len(costs)-1; i < j; i, j = i+1, j-1 {
		costs[i], costs[j] = costs[j], costs[i]
	}
	c.costHistory = costs
}

func (c *Controller) persistCostSample(dailyCost float64) {
	if c.db == nil {
		return
	}
	logger := log.Log.WithName("alerts")
	ts := time.Now().Unix()
	cutoff := time.Now().Add(-90 * 24 * time.Hour).Unix()

	// Route writes through async writer to avoid contention with the
	// main database writer goroutine.
	if c.writer != nil {
		c.writer.Enqueue(func(db *sql.DB) {
			if _, err := db.Exec(`INSERT INTO cost_history (timestamp, daily_cost) VALUES (?, ?)`, ts, dailyCost); err != nil {
				logger.Error(err, "Failed to insert cost sample")
			}
			if _, err := db.Exec(`DELETE FROM cost_history WHERE timestamp < ?`, cutoff); err != nil {
				logger.Error(err, "Failed to clean up old cost history")
			}
		})
		return
	}
	// Fallback: direct write if no async writer available.
	if _, err := c.db.Exec(`INSERT INTO cost_history (timestamp, daily_cost) VALUES (?, ?)`, ts, dailyCost); err != nil {
		logger.Error(err, "Failed to insert cost sample")
	}
	if _, err := c.db.Exec(`DELETE FROM cost_history WHERE timestamp < ?`, cutoff); err != nil {
		logger.Error(err, "Failed to clean up old cost history")
	}
}

func (c *Controller) run(ctx context.Context) {
	logger := log.FromContext(ctx).WithName("alerts")
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			snapshot := c.state.Snapshot()
			if _, err := c.Analyze(ctx, snapshot); err != nil {
				logger.Error(err, "Alert analysis failed")
			}
		case <-ctx.Done():
			return
		}
	}
}

func meanStdDev(data []float64) (float64, float64) {
	if len(data) == 0 {
		return 0, 0
	}
	var sum float64
	for _, v := range data {
		sum += v
	}
	mean := sum / float64(len(data))

	var sumSq float64
	for _, v := range data {
		diff := v - mean
		sumSq += diff * diff
	}
	stddev := math.Sqrt(sumSq / float64(len(data)))
	return mean, stddev
}

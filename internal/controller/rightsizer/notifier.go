package rightsizer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/internal/state"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// Notifier sends rightsizer recommendations to configured notification channels
// (Slack, Teams) and records them in the audit log.
type Notifier struct {
	config     *config.Config
	auditLog   *state.AuditLog
	httpClient *http.Client

	mu           sync.Mutex
	lastNotified map[string]time.Time // target key -> last notification time
}

func NewNotifier(cfg *config.Config, auditLog *state.AuditLog) *Notifier {
	return &Notifier{
		config:       cfg,
		auditLog:     auditLog,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
		lastNotified: make(map[string]time.Time),
	}
}

// Notify records the recommendation to the audit log and sends it to
// configured notification channels. Deduplicates by target using the
// alerts cooldown setting.
func (n *Notifier) Notify(ctx context.Context, rec optimizer.Recommendation) {
	logger := log.FromContext(ctx).WithName("rightsizer-notifier")

	targetKey := rec.TargetNamespace + "/" + rec.TargetKind + "/" + rec.TargetName

	// Record to audit log (always, regardless of cooldown)
	if n.auditLog != nil {
		n.auditLog.Record("rightsize", targetKey, "system", rec.Summary)
	}

	// Check cooldown before sending to channels
	n.mu.Lock()
	cooldown := time.Duration(n.config.Alerts.CooldownMinutes) * time.Minute
	if cooldown == 0 {
		cooldown = 60 * time.Minute
	}
	last, ok := n.lastNotified[targetKey]
	if ok && time.Since(last) < cooldown {
		n.mu.Unlock()
		return
	}
	n.lastNotified[targetKey] = time.Now()
	n.mu.Unlock()

	// Skip if no channels configured
	if n.config.Alerts.SlackWebhookURL == "" && len(n.config.Alerts.Channels) == 0 {
		return
	}

	// Send to static Slack webhook
	if n.config.Alerts.SlackWebhookURL != "" {
		if err := n.sendSlack(ctx, rec, n.config.Alerts.SlackWebhookURL); err != nil {
			logger.Error(err, "Failed to send Slack notification", "target", targetKey)
		}
	}

	// Send to dynamic channels
	for _, ch := range n.config.Alerts.Channels {
		if !ch.Enabled {
			continue
		}
		switch ch.Type {
		case "slack":
			if err := n.sendSlack(ctx, rec, ch.URL); err != nil {
				logger.Error(err, "Failed to send Slack notification", "channel", ch.Name)
			}
		case "teams":
			if err := n.sendTeams(ctx, rec, ch.URL); err != nil {
				logger.Error(err, "Failed to send Teams notification", "channel", ch.Name)
			}
		}
	}
}

func (n *Notifier) sendSlack(ctx context.Context, rec optimizer.Recommendation, webhookURL string) error {
	color := "#36a64f" // green for savings
	if rec.Priority == optimizer.PriorityHigh {
		color = "#ffcc00"
	}

	savingsText := ""
	if rec.EstimatedSaving.MonthlySavingsUSD > 0 {
		savingsText = fmt.Sprintf(" (saves $%.0f/mo)", rec.EstimatedSaving.MonthlySavingsUSD)
	}

	payload := map[string]interface{}{
		"attachments": []map[string]interface{}{
			{
				"color":  color,
				"title":  fmt.Sprintf("[KOptimizer] Rightsizing: %s/%s", rec.TargetNamespace, rec.TargetName),
				"text":   rec.Summary + savingsText,
				"footer": "KOptimizer Rightsizer",
				"ts":     rec.CreatedAt.Unix(),
			},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshaling Slack payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating Slack request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending Slack notification: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("Slack returned status %d", resp.StatusCode)
	}
	return nil
}

func (n *Notifier) sendTeams(ctx context.Context, rec optimizer.Recommendation, webhookURL string) error {
	themeColor := "00FF00"
	if rec.Priority == optimizer.PriorityHigh {
		themeColor = "FFCC00"
	}

	savingsText := ""
	if rec.EstimatedSaving.MonthlySavingsUSD > 0 {
		savingsText = fmt.Sprintf(" (saves $%.0f/mo)", rec.EstimatedSaving.MonthlySavingsUSD)
	}

	payload := map[string]interface{}{
		"@type":      "MessageCard",
		"@context":   "http://schema.org/extensions",
		"themeColor": themeColor,
		"title":      fmt.Sprintf("[KOptimizer] Rightsizing: %s/%s", rec.TargetNamespace, rec.TargetName),
		"text":       rec.Summary + savingsText,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshaling Teams payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating Teams request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending Teams notification: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("Teams returned status %d", resp.StatusCode)
	}
	return nil
}

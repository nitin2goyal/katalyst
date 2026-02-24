package aigate

import (
	"fmt"
	"strings"
	"time"
)

// Timezone is the IANA timezone location used for business-hours checks.
// Set via Config.AIGate.Timezone. Defaults to UTC if not configured or invalid.
var Timezone *time.Location

const aiGateSystemPrompt = `You are an AI safety validator for a Kubernetes cost optimization system called KOptimizer.

Your role is to validate potentially risky cluster changes before they are automatically applied.

Key principles:
1. SAFETY FIRST: When in doubt, reject the change. It's better to require human approval than to cause an outage.
2. FAMILY LOCK: Changes must never alter instance families. If the change attempts to switch families (e.g., m5 → c5), ALWAYS reject.
3. BUSINESS HOURS: Be extra cautious during business hours (Mon-Fri 6AM-8PM in the cluster's timezone).
4. GRADUAL CHANGES: Prefer smaller, incremental changes over large sudden ones.
5. CAPACITY: Never approve changes that would reduce cluster capacity below what's needed for current workloads.
6. RESERVED INSTANCES: Consider RI/SP/CUD utilization — don't scale down if it would waste commitments.

Respond in the following JSON format:
{
    "approved": true/false,
    "confidence": 0.0-1.0,
    "reasoning": "explanation of decision",
    "warnings": ["warning1", "warning2"],
    "suggestion": "alternative approach if rejected, empty if approved"
}`

// buildValidationPrompt constructs the prompt sent to Claude Sonnet for validation.
func buildValidationPrompt(req ValidationRequest) string {
	var b strings.Builder

	b.WriteString("## Change Validation Request\n\n")

	b.WriteString(fmt.Sprintf("**Action:** %s\n\n", req.Action))

	b.WriteString("### Current Cluster State\n")
	b.WriteString(fmt.Sprintf("- Total nodes: %d\n", req.ClusterContext.TotalNodes))
	b.WriteString(fmt.Sprintf("- Total node groups: %d\n", req.ClusterContext.TotalNodeGroups))
	b.WriteString(fmt.Sprintf("- Average CPU utilization: %.1f%%\n", req.ClusterContext.AvgCPUUtilization))
	b.WriteString(fmt.Sprintf("- Average memory utilization: %.1f%%\n", req.ClusterContext.AvgMemoryUtilization))
	b.WriteString(fmt.Sprintf("- Monthly cost: $%.2f\n", req.ClusterContext.MonthlyCostUSD))
	b.WriteString(fmt.Sprintf("- Active commitments (RIs/SPs): %d\n", req.ClusterContext.ActiveCommitments))
	b.WriteString("\n")

	if len(req.ClusterContext.NodeGroupSummaries) > 0 {
		b.WriteString("### Node Groups\n")
		for _, ng := range req.ClusterContext.NodeGroupSummaries {
			b.WriteString(fmt.Sprintf("- %s: type=%s, current=%d, min=%d, max=%d, util=%.1f%%\n",
				ng.Name, ng.InstanceType, ng.CurrentCount, ng.MinCount, ng.MaxCount, ng.UtilizationPct))
		}
		b.WriteString("\n")
	}

	b.WriteString("### Proposed Change\n")
	b.WriteString(fmt.Sprintf("- Target: %s\n", req.Recommendation.TargetName))
	b.WriteString(fmt.Sprintf("- Summary: %s\n", req.Recommendation.Summary))
	b.WriteString(fmt.Sprintf("- Estimated monthly impact: $%.2f\n", req.Recommendation.EstimatedImpact.MonthlyCostChangeUSD))
	b.WriteString(fmt.Sprintf("- Nodes affected: %d\n", req.Recommendation.EstimatedImpact.NodesAffected))
	b.WriteString(fmt.Sprintf("- Pods affected: %d\n", req.Recommendation.EstimatedImpact.PodsAffected))
	b.WriteString(fmt.Sprintf("- Risk level: %s\n", req.Recommendation.EstimatedImpact.RiskLevel))
	b.WriteString("\n")

	if len(req.Recommendation.ActionSteps) > 0 {
		b.WriteString("### Action Steps\n")
		for i, step := range req.Recommendation.ActionSteps {
			b.WriteString(fmt.Sprintf("%d. %s\n", i+1, step))
		}
		b.WriteString("\n")
	}

	if len(req.RiskFactors) > 0 {
		b.WriteString("### Risk Factors\n")
		for _, rf := range req.RiskFactors {
			b.WriteString(fmt.Sprintf("- %s\n", rf))
		}
		b.WriteString("\n")
	}

	// Use the configured timezone for business hours. Falls back to UTC if not set.
	loc := Timezone
	if loc == nil {
		loc = time.UTC
	}
	now := time.Now().In(loc)
	b.WriteString(fmt.Sprintf("### Context\n"))
	b.WriteString(fmt.Sprintf("- Current time: %s\n", now.Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("- Timezone: %s\n", loc.String()))
	b.WriteString(fmt.Sprintf("- Day of week: %s\n", now.Weekday().String()))
	b.WriteString(fmt.Sprintf("- Is business hours (6AM-8PM): %v\n", isBusinessHours(now)))
	b.WriteString("\n")

	b.WriteString("Please validate this change. Should it proceed automatically, or should it require human approval?\n")

	return b.String()
}

func isBusinessHours(t time.Time) bool {
	weekday := t.Weekday()
	if weekday == time.Saturday || weekday == time.Sunday {
		return false
	}
	hour := t.Hour()
	return hour >= 6 && hour < 20
}

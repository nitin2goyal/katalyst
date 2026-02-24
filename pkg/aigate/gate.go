package aigate

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

const (
	DefaultModel   = "claude-sonnet-4-6"
	DefaultTimeout = 10 * time.Second
)

// AIGate validates risky changes using Claude Sonnet before execution.
type AIGate struct {
	client  *anthropic.Client
	model   string
	enabled bool
	timeout time.Duration

	// Thresholds for triggering validation
	CostThresholdUSD  float64 // Changes with impact > this amount require validation
	ScaleThresholdPct float64 // Scaling changes > this percentage require validation
	MaxEvictNodes     int     // Evicting from > this many nodes requires validation
}

// Config holds AI Gate configuration.
type Config struct {
	Enabled           bool
	APIKey            string
	Model             string
	Timeout           time.Duration
	CostThresholdUSD  float64
	ScaleThresholdPct float64
	MaxEvictNodes     int
}

// NewAIGate creates a new AI Safety Gate.
func NewAIGate(cfg Config) (*AIGate, error) {
	if !cfg.Enabled {
		return &AIGate{enabled: false}, nil
	}

	client := anthropic.NewClient()
	clientPtr := &client

	model := cfg.Model
	if model == "" {
		model = DefaultModel
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}

	costThreshold := cfg.CostThresholdUSD
	if costThreshold == 0 {
		costThreshold = 500.0
	}

	scaleThreshold := cfg.ScaleThresholdPct
	if scaleThreshold == 0 {
		scaleThreshold = 30.0
	}

	maxEvictNodes := cfg.MaxEvictNodes
	if maxEvictNodes == 0 {
		maxEvictNodes = 3
	}

	return &AIGate{
		client:            clientPtr,
		model:             model,
		enabled:           true,
		timeout:           timeout,
		CostThresholdUSD:  costThreshold,
		ScaleThresholdPct: scaleThreshold,
		MaxEvictNodes:     maxEvictNodes,
	}, nil
}

// ValidationRequest contains all context needed for AI validation.
type ValidationRequest struct {
	Action         string
	ClusterContext ClusterSummary
	Recommendation optimizer.Recommendation
	RiskFactors    []string
}

// ClusterSummary provides cluster context for the AI gate.
type ClusterSummary struct {
	TotalNodes           int
	TotalNodeGroups      int
	AvgCPUUtilization    float64
	AvgMemoryUtilization float64
	MonthlyCostUSD       float64
	ActiveCommitments    int
	NodeGroupSummaries   []NodeGroupSummary
}

// NodeGroupSummary provides node group context.
type NodeGroupSummary struct {
	Name           string
	InstanceType   string
	CurrentCount   int
	MinCount       int
	MaxCount       int
	UtilizationPct float64
}

// ValidationResponse is the parsed response from Claude Sonnet.
type ValidationResponse struct {
	Approved   bool     `json:"approved"`
	Confidence float64  `json:"confidence"`
	Reasoning  string   `json:"reasoning"`
	Warnings   []string `json:"warnings"`
	Suggestion string   `json:"suggestion"`
}

// RequiresValidation checks if a recommendation needs AI Gate validation.
// Safe to call on a nil receiver — returns false.
// Validates based on BOTH the RequiresAIGate flag AND actual impact metrics,
// so high-impact recommendations cannot bypass the gate by omitting the flag.
func (g *AIGate) RequiresValidation(rec optimizer.Recommendation) bool {
	if g == nil || !g.enabled {
		return false
	}
	// Secondary check: always validate high-impact changes regardless of flag
	if abs(rec.EstimatedImpact.MonthlyCostChangeUSD) > g.CostThresholdUSD {
		return true
	}
	if rec.EstimatedImpact.NodesAffected > g.MaxEvictNodes {
		return true
	}
	return rec.RequiresAIGate
}

// Validate sends the change to Claude Sonnet for review.
// If Sonnet rejects, the change becomes a recommendation (human must approve).
// If Sonnet approves, the change proceeds automatically.
// If the API is unreachable or the gate is nil, falls back to recommendation mode (reject).
func (g *AIGate) Validate(ctx context.Context, req ValidationRequest) (*ValidationResponse, error) {
	if g == nil {
		return &ValidationResponse{
			Approved:   false,
			Confidence: 0,
			Reasoning:  "AI Gate not configured, requiring manual approval",
			Warnings:   []string{"AI Gate is nil, falling back to manual approval"},
		}, nil
	}
	if !g.enabled {
		return &ValidationResponse{
			Approved:   false,
			Confidence: 0,
			Reasoning:  "AI Gate disabled, requiring manual approval",
			Warnings:   []string{"AI Gate is disabled, falling back to manual approval"},
		}, nil
	}

	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()

	prompt := buildValidationPrompt(req)

	resp, err := g.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(g.model),
		MaxTokens: int64(1024),
		System: []anthropic.TextBlockParam{
			{Text: aiGateSystemPrompt},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
	})
	if err != nil {
		// Fallback: if API unreachable, require human approval
		return &ValidationResponse{
			Approved:   false,
			Confidence: 0,
			Reasoning:  fmt.Sprintf("AI Gate API error (falling back to manual approval): %v", err),
			Warnings:   []string{"AI Gate unavailable, requiring manual approval"},
		}, nil
	}

	return parseValidationResponse(resp)
}

// parseValidationResponse extracts the structured response from Claude's output.
func parseValidationResponse(resp *anthropic.Message) (*ValidationResponse, error) {
	if len(resp.Content) == 0 {
		return nil, fmt.Errorf("empty response from AI Gate")
	}

	text := resp.Content[0].Text

	var result ValidationResponse
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		// Try to extract JSON from the response if it's wrapped in markdown
		jsonStart := findJSONStart(text)
		if jsonStart >= 0 {
			jsonEnd := findJSONEnd(text, jsonStart)
			if jsonEnd > jsonStart {
				if err2 := json.Unmarshal([]byte(text[jsonStart:jsonEnd+1]), &result); err2 != nil {
					return nil, fmt.Errorf("parsing AI Gate response: %w (raw: %s)", err2, text)
				}
				return &result, nil
			}
		}
		return nil, fmt.Errorf("parsing AI Gate response: %w (raw: %s)", err, text)
	}
	return &result, nil
}

func findJSONStart(s string) int {
	for i, c := range s {
		if c == '{' {
			return i
		}
	}
	return -1
}

func findJSONEnd(s string, start int) int {
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		ch := s[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && inString {
			escaped = true
			continue
		}
		if ch == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch ch {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// APIClient wraps an http.Client and a base URL to call the KOptimizer REST API.
type APIClient struct {
	baseURL    string
	httpClient *http.Client
}

// NewAPIClient creates a new APIClient targeting the given base URL.
func NewAPIClient(baseURL string) *APIClient {
	return &APIClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// doGet performs an HTTP GET and returns the response body as raw JSON.
func (c *APIClient) doGet(path string) (json.RawMessage, error) {
	url := c.baseURL + path
	resp, err := c.httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response from GET %s: %w", path, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GET %s returned HTTP %d: %s", path, resp.StatusCode, string(body))
	}

	return json.RawMessage(body), nil
}

// doPost performs an HTTP POST with a JSON body and returns the response body as raw JSON.
func (c *APIClient) doPost(path string, payload interface{}) (json.RawMessage, error) {
	url := c.baseURL + path

	var reqBody io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("marshaling POST body for %s: %w", path, err)
		}
		reqBody = bytes.NewReader(data)
	}

	resp, err := c.httpClient.Post(url, "application/json", reqBody)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response from POST %s: %w", path, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("POST %s returned HTTP %d: %s", path, resp.StatusCode, string(body))
	}

	return json.RawMessage(body), nil
}

// doPut performs an HTTP PUT with a JSON body and returns the response body as raw JSON.
func (c *APIClient) doPut(path string, payload interface{}) (json.RawMessage, error) {
	url := c.baseURL + path

	var reqBody []byte
	if payload != nil {
		var err error
		reqBody, err = json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("marshaling PUT body for %s: %w", path, err)
		}
	}

	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("creating PUT request for %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("PUT %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response from PUT %s: %w", path, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("PUT %s returned HTTP %d: %s", path, resp.StatusCode, string(body))
	}

	return json.RawMessage(body), nil
}

// ── Cluster ──────────────────────────────────────────────────────────────

// GetClusterSummary calls GET /api/v1/cluster/summary.
func (c *APIClient) GetClusterSummary() (json.RawMessage, error) {
	return c.doGet("/api/v1/cluster/summary")
}

// GetClusterHealth calls GET /api/v1/cluster/health.
func (c *APIClient) GetClusterHealth() (json.RawMessage, error) {
	return c.doGet("/api/v1/cluster/health")
}

// ── Node Groups ──────────────────────────────────────────────────────────

// ListNodeGroups calls GET /api/v1/nodegroups.
func (c *APIClient) ListNodeGroups() (json.RawMessage, error) {
	return c.doGet("/api/v1/nodegroups")
}

// GetNodeGroup calls GET /api/v1/nodegroups/{id}.
func (c *APIClient) GetNodeGroup(id string) (json.RawMessage, error) {
	return c.doGet("/api/v1/nodegroups/" + id)
}

// GetNodeGroupNodes calls GET /api/v1/nodegroups/{id}/nodes.
func (c *APIClient) GetNodeGroupNodes(id string) (json.RawMessage, error) {
	return c.doGet("/api/v1/nodegroups/" + id + "/nodes")
}

// ListEmptyNodeGroups calls GET /api/v1/nodegroups/empty.
func (c *APIClient) ListEmptyNodeGroups() (json.RawMessage, error) {
	return c.doGet("/api/v1/nodegroups/empty")
}

// ── Nodes ────────────────────────────────────────────────────────────────

// ListNodes calls GET /api/v1/nodes.
func (c *APIClient) ListNodes() (json.RawMessage, error) {
	return c.doGet("/api/v1/nodes")
}

// GetNode calls GET /api/v1/nodes/{name}.
func (c *APIClient) GetNode(name string) (json.RawMessage, error) {
	return c.doGet("/api/v1/nodes/" + name)
}

// ── Cost ─────────────────────────────────────────────────────────────────

// GetCostSummary calls GET /api/v1/cost/summary.
func (c *APIClient) GetCostSummary() (json.RawMessage, error) {
	return c.doGet("/api/v1/cost/summary")
}

// GetCostByNamespace calls GET /api/v1/cost/by-namespace.
func (c *APIClient) GetCostByNamespace() (json.RawMessage, error) {
	return c.doGet("/api/v1/cost/by-namespace")
}

// GetCostByWorkload calls GET /api/v1/cost/by-workload.
func (c *APIClient) GetCostByWorkload() (json.RawMessage, error) {
	return c.doGet("/api/v1/cost/by-workload")
}

// GetCostByLabel calls GET /api/v1/cost/by-label.
func (c *APIClient) GetCostByLabel() (json.RawMessage, error) {
	return c.doGet("/api/v1/cost/by-label")
}

// GetCostTrend calls GET /api/v1/cost/trend.
func (c *APIClient) GetCostTrend() (json.RawMessage, error) {
	return c.doGet("/api/v1/cost/trend")
}

// GetCostSavings calls GET /api/v1/cost/savings.
func (c *APIClient) GetCostSavings() (json.RawMessage, error) {
	return c.doGet("/api/v1/cost/savings")
}

// ── Commitments ──────────────────────────────────────────────────────────

// ListCommitments calls GET /api/v1/commitments.
func (c *APIClient) ListCommitments() (json.RawMessage, error) {
	return c.doGet("/api/v1/commitments")
}

// ListUnderutilizedCommitments calls GET /api/v1/commitments/underutilized.
func (c *APIClient) ListUnderutilizedCommitments() (json.RawMessage, error) {
	return c.doGet("/api/v1/commitments/underutilized")
}

// ListExpiringCommitments calls GET /api/v1/commitments/expiring.
func (c *APIClient) ListExpiringCommitments() (json.RawMessage, error) {
	return c.doGet("/api/v1/commitments/expiring")
}

// ── Recommendations ──────────────────────────────────────────────────────

// ListRecommendations calls GET /api/v1/recommendations.
func (c *APIClient) ListRecommendations() (json.RawMessage, error) {
	return c.doGet("/api/v1/recommendations")
}

// GetRecommendation calls GET /api/v1/recommendations/{id}.
func (c *APIClient) GetRecommendation(id string) (json.RawMessage, error) {
	return c.doGet("/api/v1/recommendations/" + id)
}

// ApproveRecommendation calls POST /api/v1/recommendations/{id}/approve.
func (c *APIClient) ApproveRecommendation(id string) (json.RawMessage, error) {
	return c.doPost("/api/v1/recommendations/"+id+"/approve", nil)
}

// DismissRecommendation calls POST /api/v1/recommendations/{id}/dismiss.
func (c *APIClient) DismissRecommendation(id string) (json.RawMessage, error) {
	return c.doPost("/api/v1/recommendations/"+id+"/dismiss", nil)
}

// GetRecommendationsSummary calls GET /api/v1/recommendations/summary.
func (c *APIClient) GetRecommendationsSummary() (json.RawMessage, error) {
	return c.doGet("/api/v1/recommendations/summary")
}

// ── Workloads ────────────────────────────────────────────────────────────

// ListWorkloads calls GET /api/v1/workloads.
func (c *APIClient) ListWorkloads() (json.RawMessage, error) {
	return c.doGet("/api/v1/workloads")
}

// GetWorkload calls GET /api/v1/workloads/{ns}/{kind}/{name}.
func (c *APIClient) GetWorkload(ns, kind, name string) (json.RawMessage, error) {
	return c.doGet("/api/v1/workloads/" + ns + "/" + kind + "/" + name)
}

// GetWorkloadRightsizing calls GET /api/v1/workloads/{ns}/{kind}/{name}/rightsizing.
func (c *APIClient) GetWorkloadRightsizing(ns, kind, name string) (json.RawMessage, error) {
	return c.doGet("/api/v1/workloads/" + ns + "/" + kind + "/" + name + "/rightsizing")
}

// GetWorkloadScaling calls GET /api/v1/workloads/{ns}/{kind}/{name}/scaling.
func (c *APIClient) GetWorkloadScaling(ns, kind, name string) (json.RawMessage, error) {
	return c.doGet("/api/v1/workloads/" + ns + "/" + kind + "/" + name + "/scaling")
}

// ── GPU ──────────────────────────────────────────────────────────────────

// ListGPUNodes calls GET /api/v1/gpu/nodes.
func (c *APIClient) ListGPUNodes() (json.RawMessage, error) {
	return c.doGet("/api/v1/gpu/nodes")
}

// GetGPUUtilization calls GET /api/v1/gpu/utilization.
func (c *APIClient) GetGPUUtilization() (json.RawMessage, error) {
	return c.doGet("/api/v1/gpu/utilization")
}

// ListGPURecommendations calls GET /api/v1/gpu/recommendations.
func (c *APIClient) ListGPURecommendations() (json.RawMessage, error) {
	return c.doGet("/api/v1/gpu/recommendations")
}

// ── Config ───────────────────────────────────────────────────────────────

// GetConfig calls GET /api/v1/config.
func (c *APIClient) GetConfig() (json.RawMessage, error) {
	return c.doGet("/api/v1/config")
}

// SetMode calls PUT /api/v1/config/mode with the given mode.
func (c *APIClient) SetMode(mode string) (json.RawMessage, error) {
	return c.doPut("/api/v1/config/mode", map[string]string{"mode": mode})
}

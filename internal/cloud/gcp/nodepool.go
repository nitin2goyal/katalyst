package gcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
	"github.com/koptimizer/koptimizer/pkg/familylock"
)

const gkeBaseURL = "https://container.googleapis.com/v1"

// gkeNodePoolConfig represents a GKE node pool's config section.
type gkeNodePoolConfig struct {
	MachineType string            `json:"machineType"`
	DiskType    string            `json:"diskType"`
	DiskSizeGb  int               `json:"diskSizeGb"`
	Spot        bool              `json:"spot"`
	Preemptible bool              `json:"preemptible"`
	Labels      map[string]string `json:"labels"`
	Taints      []gkeTaint        `json:"taints"`
}

type gkeTaint struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Effect string `json:"effect"`
}

// gkeAutoscaling represents node pool autoscaling config.
type gkeAutoscaling struct {
	Enabled      bool `json:"enabled"`
	MinNodeCount int  `json:"minNodeCount"`
	MaxNodeCount int  `json:"maxNodeCount"`
}

// gkeNodePool represents a GKE node pool from the REST API.
type gkeNodePool struct {
	Name              string            `json:"name"`
	Config            gkeNodePoolConfig `json:"config"`
	InitialNodeCount  int               `json:"initialNodeCount"`
	Autoscaling       gkeAutoscaling    `json:"autoscaling"`
	InstanceGroupUrls []string          `json:"instanceGroupUrls"`
	Status            string            `json:"status"`
	Locations         []string          `json:"locations"`
}

// gkeNodePoolListResponse is the response from listing node pools.
type gkeNodePoolListResponse struct {
	NodePools []gkeNodePool `json:"nodePools"`
}

// gkeInstanceGroupManager represents a GCE instance group manager (for getting current size).
type gkeInstanceGroupManager struct {
	TargetSize int `json:"targetSize"`
}

// discoverNodePools lists all node pools in the GKE cluster.
func discoverNodePools(ctx context.Context, project, region, cluster string, client *http.Client) ([]*cloudprovider.NodeGroup, error) {
	url := fmt.Sprintf("%s/projects/%s/locations/%s/clusters/%s/nodePools", gkeBaseURL, project, region, cluster)

	body, err := doGCPGet(ctx, client, url)
	if err != nil {
		return nil, fmt.Errorf("listing GKE node pools: %w", err)
	}

	var resp gkeNodePoolListResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parsing node pool list response: %w", err)
	}

	var nodeGroups []*cloudprovider.NodeGroup
	for _, np := range resp.NodePools {
		ng, err := mapNodePoolToNodeGroup(ctx, np, region, client)
		if err != nil {
			return nil, fmt.Errorf("mapping node pool %s: %w", np.Name, err)
		}
		nodeGroups = append(nodeGroups, ng)
	}

	return nodeGroups, nil
}

// getNodePool retrieves a single node pool by name.
func getNodePool(ctx context.Context, project, region, cluster, nodePoolID string, client *http.Client) (*cloudprovider.NodeGroup, error) {
	url := fmt.Sprintf("%s/projects/%s/locations/%s/clusters/%s/nodePools/%s", gkeBaseURL, project, region, cluster, nodePoolID)

	body, err := doGCPGet(ctx, client, url)
	if err != nil {
		return nil, fmt.Errorf("getting GKE node pool %s: %w", nodePoolID, err)
	}

	var np gkeNodePool
	if err := json.Unmarshal(body, &np); err != nil {
		return nil, fmt.Errorf("parsing node pool response: %w", err)
	}

	return mapNodePoolToNodeGroup(ctx, np, region, client)
}

// scaleNodePool sets the size of a node pool.
func scaleNodePool(ctx context.Context, project, region, cluster, nodePoolID string, desiredCount int, client *http.Client) error {
	url := fmt.Sprintf("%s/projects/%s/locations/%s/clusters/%s/nodePools/%s:setSize", gkeBaseURL, project, region, cluster, nodePoolID)

	reqBody := map[string]interface{}{
		"nodeCount": desiredCount,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshaling scale request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("creating scale request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("scaling node pool %s: %w", nodePoolID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("scaling node pool %s: HTTP %d: %s", nodePoolID, resp.StatusCode, string(respBody))
	}

	return nil
}

// setNodePoolAutoscaling updates the autoscaling configuration for a node pool.
// Pass nil for minCount or maxCount to leave them unchanged (fetches current values).
func setNodePoolAutoscaling(ctx context.Context, project, region, cluster, nodePoolID string, minCount, maxCount *int, client *http.Client) error {
	// First, get the current node pool to read existing autoscaling settings.
	currentNP, err := getNodePool(ctx, project, region, cluster, nodePoolID, client)
	if err != nil {
		return fmt.Errorf("fetching current node pool for autoscaling update: %w", err)
	}

	effectiveMin := currentNP.MinCount
	effectiveMax := currentNP.MaxCount
	if minCount != nil {
		effectiveMin = *minCount
	}
	if maxCount != nil {
		effectiveMax = *maxCount
	}

	url := fmt.Sprintf("%s/projects/%s/locations/%s/clusters/%s/nodePools/%s:setAutoscaling", gkeBaseURL, project, region, cluster, nodePoolID)

	reqBody := map[string]interface{}{
		"autoscaling": map[string]interface{}{
			"enabled":      true,
			"minNodeCount": effectiveMin,
			"maxNodeCount": effectiveMax,
		},
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshaling autoscaling request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("creating autoscaling request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("setting autoscaling for node pool %s: %w", nodePoolID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("setting autoscaling for node pool %s: HTTP %d: %s", nodePoolID, resp.StatusCode, string(respBody))
	}

	return nil
}

// mapNodePoolToNodeGroup converts a GKE node pool to a cloudprovider.NodeGroup.
func mapNodePoolToNodeGroup(ctx context.Context, np gkeNodePool, region string, client *http.Client) (*cloudprovider.NodeGroup, error) {
	lifecycle := "on-demand"
	if np.Config.Spot || np.Config.Preemptible {
		lifecycle = "spot"
	}

	family := ""
	if np.Config.MachineType != "" {
		f, err := familylock.ExtractFamily(np.Config.MachineType)
		if err == nil {
			family = f
		}
	}

	// Determine the current count by querying instance group managers.
	currentCount := np.InitialNodeCount
	if len(np.InstanceGroupUrls) > 0 {
		total, err := getInstanceGroupsTotalSize(ctx, np.InstanceGroupUrls, client)
		if err == nil && total > 0 {
			currentCount = total
		}
	}

	// Determine zone from locations or instance group URLs.
	zone := ""
	if len(np.Locations) > 0 {
		zone = np.Locations[0]
	}

	// Convert GKE taints to corev1 taints.
	var taints []corev1.Taint
	for _, t := range np.Config.Taints {
		taint := corev1.Taint{
			Key:   t.Key,
			Value: t.Value,
		}
		switch strings.ToUpper(t.Effect) {
		case "NO_SCHEDULE":
			taint.Effect = corev1.TaintEffectNoSchedule
		case "NO_EXECUTE":
			taint.Effect = corev1.TaintEffectNoExecute
		case "PREFER_NO_SCHEDULE":
			taint.Effect = corev1.TaintEffectPreferNoSchedule
		}
		taints = append(taints, taint)
	}

	return &cloudprovider.NodeGroup{
		ID:             np.Name,
		Name:           np.Name,
		InstanceType:   np.Config.MachineType,
		InstanceFamily: family,
		CurrentCount:   currentCount,
		MinCount:       np.Autoscaling.MinNodeCount,
		MaxCount:       np.Autoscaling.MaxNodeCount,
		DesiredCount:   currentCount,
		Zone:           zone,
		Region:         region,
		Labels:         np.Config.Labels,
		Taints:         taints,
		Lifecycle:      lifecycle,
		DiskType:       np.Config.DiskType,
		DiskSizeGB:     np.Config.DiskSizeGb,
	}, nil
}

// getInstanceGroupsTotalSize queries instance group manager URLs to get the total current size.
// Queries are performed concurrently with bounded parallelism to reduce latency
// for clusters with many node pools across zones.
func getInstanceGroupsTotalSize(ctx context.Context, instanceGroupUrls []string, client *http.Client) (int, error) {
	if len(instanceGroupUrls) == 0 {
		return 0, nil
	}
	// For a single URL, skip the goroutine overhead.
	if len(instanceGroupUrls) == 1 {
		body, err := doGCPGet(ctx, client, instanceGroupUrls[0])
		if err != nil {
			return 0, fmt.Errorf("querying instance group %s: %w", instanceGroupUrls[0], err)
		}
		var igm gkeInstanceGroupManager
		if err := json.Unmarshal(body, &igm); err != nil {
			return 0, fmt.Errorf("parsing instance group response: %w", err)
		}
		return igm.TargetSize, nil
	}

	type result struct {
		size int
		err  error
	}
	results := make(chan result, len(instanceGroupUrls))

	// Bounded parallelism: limit to 10 concurrent requests to avoid
	// overwhelming the GCP API with rate-limited calls.
	const maxConcurrency = 10
	sem := make(chan struct{}, maxConcurrency)

	for _, igURL := range instanceGroupUrls {
		sem <- struct{}{} // acquire semaphore
		go func(u string) {
			defer func() { <-sem }() // release semaphore
			body, err := doGCPGet(ctx, client, u)
			if err != nil {
				results <- result{0, fmt.Errorf("querying instance group %s: %w", u, err)}
				return
			}
			var igm gkeInstanceGroupManager
			if err := json.Unmarshal(body, &igm); err != nil {
				results <- result{0, fmt.Errorf("parsing instance group response: %w", err)}
				return
			}
			results <- result{igm.TargetSize, nil}
		}(igURL)
	}

	total := 0
	for range instanceGroupUrls {
		r := <-results
		if r.err != nil {
			return 0, r.err
		}
		total += r.size
	}
	return total, nil
}

// doGCPGet performs an authenticated GET request with retry and exponential backoff.
// Retries up to 3 times on 429 (rate limit) and 5xx (server error) responses.
func doGCPGet(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	const maxRetries = 3
	backoff := 1 * time.Second

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2 // exponential backoff: 1s, 2s, 4s
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("creating request for %s: %w", url, err)
		}

		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("GET %s: %w", url, err)
			continue // retry on network errors
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("reading response from %s: %w", url, err)
			continue
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return body, nil
		}

		// Retry on rate limit or server errors
		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("GET %s: HTTP %d: %s", url, resp.StatusCode, string(body))
			// Respect Retry-After header if present
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				if seconds, err := strconv.Atoi(ra); err == nil && seconds > 0 {
					backoff = time.Duration(seconds) * time.Second
				}
			}
			continue
		}

		// Non-retryable error
		return nil, fmt.Errorf("GET %s: HTTP %d: %s", url, resp.StatusCode, string(body))
	}

	return nil, fmt.Errorf("GET %s failed after %d retries: %w", url, maxRetries, lastErr)
}

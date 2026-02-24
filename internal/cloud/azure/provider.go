package azure

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/koptimizer/koptimizer/internal/store"
	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
	"github.com/koptimizer/koptimizer/pkg/cost"
	"github.com/koptimizer/koptimizer/pkg/familylock"
)

// Provider implements cloudprovider.CloudProvider for Azure AKS.
type Provider struct {
	region         string
	subscriptionID string
	resourceGroup  string
	clusterName    string
	httpClient     *http.Client
	bearerToken    string
	tokenMu        sync.Mutex
	tokenExpiry    time.Time
	pricingCache   *store.PricingCache
}

// imdsTokenResponse is the response from the Azure Instance Metadata Service token endpoint.
type imdsTokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   string `json:"expires_in"`
	ExpiresOn   string `json:"expires_on"`
	Resource    string `json:"resource"`
	TokenType   string `json:"token_type"`
}

// servicePrincipalTokenResponse is the response from Azure AD token endpoint.
type servicePrincipalTokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   string `json:"expires_in"`
	ExpiresOn   string `json:"expires_on"`
	TokenType   string `json:"token_type"`
}

func NewProvider(region string, pricingCache *store.PricingCache) (*Provider, error) {
	subscriptionID := os.Getenv("AZURE_SUBSCRIPTION_ID")
	if subscriptionID == "" {
		return nil, fmt.Errorf("AZURE_SUBSCRIPTION_ID environment variable is required")
	}

	resourceGroup := os.Getenv("AZURE_RESOURCE_GROUP")
	if resourceGroup == "" {
		resourceGroup = os.Getenv("AKS_RESOURCE_GROUP")
	}
	if resourceGroup == "" {
		return nil, fmt.Errorf("AZURE_RESOURCE_GROUP or AKS_RESOURCE_GROUP environment variable is required")
	}

	clusterName := os.Getenv("KOPTIMIZER_CLUSTER_NAME")

	p := &Provider{
		region:         region,
		subscriptionID: subscriptionID,
		resourceGroup:  resourceGroup,
		clusterName:    clusterName,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		pricingCache:   pricingCache,
	}

	return p, nil
}

func (p *Provider) Name() string { return "azure" }

// getToken returns a valid bearer token, refreshing if needed.
func (p *Provider) getToken(ctx context.Context) (string, error) {
	p.tokenMu.Lock()
	defer p.tokenMu.Unlock()

	// Return cached token if still valid.
	if p.bearerToken != "" && time.Now().Before(p.tokenExpiry.Add(-2*time.Minute)) {
		return p.bearerToken, nil
	}

	// Try service principal auth first (AZURE_CLIENT_ID + AZURE_CLIENT_SECRET + AZURE_TENANT_ID).
	clientID := os.Getenv("AZURE_CLIENT_ID")
	clientSecret := os.Getenv("AZURE_CLIENT_SECRET")
	tenantID := os.Getenv("AZURE_TENANT_ID")

	if clientID != "" && clientSecret != "" && tenantID != "" {
		token, expiry, err := p.getServicePrincipalToken(ctx, clientID, clientSecret, tenantID)
		if err == nil {
			p.bearerToken = token
			p.tokenExpiry = expiry
			return token, nil
		}
		// Fall through to IMDS if service principal fails.
	}

	// Try Azure Instance Metadata Service (IMDS) for managed identity.
	token, expiry, err := p.getIMDSToken(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to acquire Azure token: %w (set AZURE_CLIENT_ID/AZURE_CLIENT_SECRET/AZURE_TENANT_ID for service principal auth, or ensure managed identity is available)", err)
	}

	p.bearerToken = token
	p.tokenExpiry = expiry
	return token, nil
}

// getIMDSToken gets a token from the Azure Instance Metadata Service.
func (p *Provider) getIMDSToken(ctx context.Context) (string, time.Time, error) {
	imdsURL := "http://169.254.169.254/metadata/identity/oauth2/token"
	params := url.Values{
		"api-version": {"2018-02-01"},
		"resource":    {"https://management.azure.com/"},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imdsURL+"?"+params.Encode(), nil)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("creating IMDS request: %w", err)
	}
	req.Header.Set("Metadata", "true")

	// Use a timeout client for IMDS since it's a local endpoint.
	// 10s allows for slow IMDS responses in some environments.
	imdsClient := &http.Client{Timeout: 10 * time.Second}
	resp, err := imdsClient.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("IMDS request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", time.Time{}, fmt.Errorf("IMDS returned status %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp imdsTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", time.Time{}, fmt.Errorf("decoding IMDS token response: %w", err)
	}

	expiry := time.Now().Add(1 * time.Hour) // default expiry
	if tokenResp.ExpiresOn != "" {
		if expiresOnSec, err := strconv.ParseInt(tokenResp.ExpiresOn, 10, 64); err == nil {
			expiry = time.Unix(expiresOnSec, 0)
		} else if t, err := time.Parse("2006-01-02 15:04:05 -0700", tokenResp.ExpiresOn); err == nil {
			expiry = t
		}
	}

	return tokenResp.AccessToken, expiry, nil
}

// getServicePrincipalToken gets a token via client credentials flow.
func (p *Provider) getServicePrincipalToken(ctx context.Context, clientID, clientSecret, tenantID string) (string, time.Time, error) {
	tokenURL := fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", tenantID)

	data := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"scope":         {"https://management.azure.com/.default"},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("creating token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", time.Time{}, fmt.Errorf("token endpoint returned status %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp servicePrincipalTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", time.Time{}, fmt.Errorf("decoding token response: %w", err)
	}

	expiry := time.Now().Add(1 * time.Hour) // default
	if tokenResp.ExpiresIn != "" {
		var expiresInSec int64
		if _, err := fmt.Sscanf(tokenResp.ExpiresIn, "%d", &expiresInSec); err == nil {
			expiry = time.Now().Add(time.Duration(expiresInSec) * time.Second)
		}
	}

	return tokenResp.AccessToken, expiry, nil
}

// doARMRequest performs an authenticated request to the Azure Resource Manager API.
// It handles 401 (token refresh), 429, and 503 with exponential backoff (max 3 retries).
func (p *Provider) doARMRequest(ctx context.Context, method, reqURL string, body io.Reader) (*http.Response, error) {
	const maxRetries = 3

	// Buffer the body so we can replay it on retries.
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = io.ReadAll(body)
		if err != nil {
			return nil, fmt.Errorf("reading request body: %w", err)
		}
	}

	token, err := p.getToken(ctx)
	if err != nil {
		return nil, err
	}

	for attempt := 0; attempt <= maxRetries; attempt++ {
		var reqBody io.Reader
		if bodyBytes != nil {
			reqBody = bytes.NewReader(bodyBytes)
		}

		req, err := http.NewRequestWithContext(ctx, method, reqURL, reqBody)
		if err != nil {
			return nil, fmt.Errorf("creating ARM request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := p.httpClient.Do(req)
		if err != nil {
			if attempt == maxRetries {
				return nil, fmt.Errorf("ARM request failed after %d retries: %w", maxRetries, err)
			}
			p.armBackoff(ctx, attempt, nil)
			continue
		}

		switch resp.StatusCode {
		case http.StatusUnauthorized:
			resp.Body.Close()
			// Token might have expired; clear it and retry once.
			p.tokenMu.Lock()
			p.bearerToken = ""
			p.tokenExpiry = time.Time{}
			p.tokenMu.Unlock()

			token, err = p.getToken(ctx)
			if err != nil {
				return nil, err
			}
			continue

		case http.StatusTooManyRequests, http.StatusServiceUnavailable:
			if attempt == maxRetries {
				return resp, nil
			}
			resp.Body.Close()
			p.armBackoff(ctx, attempt, resp)
			continue

		default:
			return resp, nil
		}
	}

	return nil, fmt.Errorf("ARM request failed: exhausted retries")
}

// armBackoff sleeps with exponential backoff, respecting Retry-After header if present.
func (p *Provider) armBackoff(ctx context.Context, attempt int, resp *http.Response) {
	delay := time.Duration(1<<uint(attempt)) * time.Second // 1s, 2s, 4s

	// Respect Retry-After header if present.
	if resp != nil {
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if secs, err := strconv.Atoi(ra); err == nil && secs > 0 {
				delay = time.Duration(secs) * time.Second
			}
		}
	}

	select {
	case <-time.After(delay):
	case <-ctx.Done():
	}
}

func (p *Provider) GetInstanceTypes(ctx context.Context, region string) ([]*cloudprovider.InstanceType, error) {
	return getAzureVMSizes(ctx, p, region)
}

func (p *Provider) GetCurrentPricing(ctx context.Context, region string) (*cloudprovider.PricingInfo, error) {
	return getAzurePricing(ctx, region, p.pricingCache)
}

func (p *Provider) GetNodeCost(ctx context.Context, node *corev1.Node) (*cloudprovider.NodeCost, error) {
	instanceType, err := p.GetNodeInstanceType(ctx, node)
	if err != nil {
		return nil, err
	}
	pricing, err := p.GetCurrentPricing(ctx, p.region)
	if err != nil {
		return nil, err
	}
	price, ok := pricing.Prices[instanceType]
	if !ok {
		return nil, fmt.Errorf("no pricing for %s", instanceType)
	}
	isSpot := false
	if v, ok := node.Labels["kubernetes.azure.com/scalesetpriority"]; ok && v == "spot" {
		isSpot = true
	}
	spotDiscount := 0.0
	if isSpot {
		spotDiscount = estimateAzureSpotDiscount(instanceType) * 100
	}
	// Apply spot discount to reflect actual cost, not on-demand rate.
	effectivePrice := price
	if isSpot && spotDiscount > 0 {
		effectivePrice = price * (1 - spotDiscount/100)
	}
	return &cloudprovider.NodeCost{
		NodeName:       node.Name,
		InstanceType:   instanceType,
		HourlyCostUSD:  effectivePrice,
		MonthlyCostUSD: effectivePrice * cost.HoursPerMonth,
		IsSpot:         isSpot,
		SpotDiscount:   spotDiscount,
	}, nil
}

func (p *Provider) GetGPUInstanceTypes(ctx context.Context, region string) ([]*cloudprovider.GPUInstanceType, error) {
	types, err := p.GetInstanceTypes(ctx, region)
	if err != nil {
		return nil, err
	}
	var gpuTypes []*cloudprovider.GPUInstanceType
	for _, t := range types {
		if t.GPUs > 0 {
			gpuTypes = append(gpuTypes, &cloudprovider.GPUInstanceType{InstanceType: *t})
		}
	}
	return gpuTypes, nil
}

func (p *Provider) GetNodeInstanceType(ctx context.Context, node *corev1.Node) (string, error) {
	if it, ok := node.Labels["node.kubernetes.io/instance-type"]; ok {
		return it, nil
	}
	if it, ok := node.Labels["beta.kubernetes.io/instance-type"]; ok {
		return it, nil
	}
	return "", fmt.Errorf("instance type not found on node %s", node.Name)
}

func (p *Provider) GetNodeRegion(ctx context.Context, node *corev1.Node) (string, error) {
	if r, ok := node.Labels["topology.kubernetes.io/region"]; ok {
		return r, nil
	}
	return p.region, nil
}

func (p *Provider) GetNodeZone(ctx context.Context, node *corev1.Node) (string, error) {
	if z, ok := node.Labels["topology.kubernetes.io/zone"]; ok {
		return z, nil
	}
	return "", fmt.Errorf("zone not found on node %s", node.Name)
}

func (p *Provider) DiscoverNodeGroups(ctx context.Context) ([]*cloudprovider.NodeGroup, error) {
	return discoverVMSS(ctx, p)
}

func (p *Provider) GetNodeGroup(ctx context.Context, id string) (*cloudprovider.NodeGroup, error) {
	return getVMSS(ctx, p, id)
}

func (p *Provider) ScaleNodeGroup(ctx context.Context, id string, desiredCount int) error {
	if desiredCount < 0 {
		return fmt.Errorf("invalid desired count %d for VMSS %s: must be >= 0", desiredCount, id)
	}
	ng, err := p.GetNodeGroup(ctx, id)
	if err != nil {
		return fmt.Errorf("cannot validate bounds for VMSS %s: %w", id, err)
	}
	if desiredCount < ng.MinCount {
		return fmt.Errorf("desired count %d is below min %d for VMSS %s", desiredCount, ng.MinCount, id)
	}
	if desiredCount > ng.MaxCount {
		return fmt.Errorf("desired count %d exceeds max %d for VMSS %s", desiredCount, ng.MaxCount, id)
	}
	return scaleVMSS(ctx, p, id, desiredCount)
}

func (p *Provider) SetNodeGroupMinCount(ctx context.Context, id string, minCount int) error {
	return setVMSSMinCount(ctx, p, id, minCount)
}

func (p *Provider) SetNodeGroupMaxCount(ctx context.Context, id string, maxCount int) error {
	return setVMSSMaxCount(ctx, p, id, maxCount)
}

func (p *Provider) GetFamilySizes(ctx context.Context, instanceType string) ([]*cloudprovider.InstanceType, error) {
	family, err := familylock.ExtractFamily(instanceType)
	if err != nil {
		return nil, err
	}
	allTypes, err := p.GetInstanceTypes(ctx, p.region)
	if err != nil {
		return nil, err
	}
	var result []*cloudprovider.InstanceType
	for _, t := range allTypes {
		if strings.EqualFold(t.Family, family) {
			result = append(result, t)
		}
	}
	return result, nil
}

// GetReservedInstances is not applicable to Azure. Azure uses a unified
// "Reservations" model (returned by GetReservations) instead of the
// AWS-style RI/SP split. This method returns an empty slice intentionally.
func (p *Provider) GetReservedInstances(ctx context.Context) ([]*cloudprovider.Commitment, error) {
	return []*cloudprovider.Commitment{}, nil
}

// GetSavingsPlans is not yet implemented for Azure. Azure Savings Plans exist
// but require the Azure Benefit API (preview) to query programmatically.
// Users on Azure Savings Plans will see 0% utilization in commitment reports
// until this is implemented. Use GetReservations for Azure Reservation data.
// TODO: Implement via Azure Benefits RP API when it reaches GA.
func (p *Provider) GetSavingsPlans(ctx context.Context) ([]*cloudprovider.Commitment, error) {
	return []*cloudprovider.Commitment{}, nil
}

// GetCommittedUseDiscounts is not applicable to Azure. CUDs are a GCP-only
// concept. This method returns an empty slice intentionally.
func (p *Provider) GetCommittedUseDiscounts(ctx context.Context) ([]*cloudprovider.Commitment, error) {
	return []*cloudprovider.Commitment{}, nil
}

func (p *Provider) GetReservations(ctx context.Context) ([]*cloudprovider.Commitment, error) {
	return getAzureReservations(ctx, p)
}

// EstimateSpotDiscount implements cloudprovider.SpotDiscountEstimator.
func (p *Provider) EstimateSpotDiscount(instanceType string) float64 {
	return estimateAzureSpotDiscount(instanceType)
}

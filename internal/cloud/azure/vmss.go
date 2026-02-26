package azure

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
	"github.com/koptimizer/koptimizer/pkg/familylock"
)

const (
	armBaseURL         = "https://management.azure.com"
	computeAPIVersion  = "2024-03-01"
	aksAPIVersion      = "2024-01-01"
)

// vmssListResponse is the ARM response for listing VMSS.
type vmssListResponse struct {
	Value    []vmssResource `json:"value"`
	NextLink string         `json:"nextLink"`
}

// vmssResource represents an Azure Virtual Machine Scale Set from the ARM API.
type vmssResource struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Location string            `json:"location"`
	Tags     map[string]string `json:"tags"`
	Sku      vmsSku            `json:"sku"`
	Properties vmssProperties  `json:"properties"`
}

// vmsSku represents the SKU of a VMSS.
type vmsSku struct {
	Name     string `json:"name"`
	Tier     string `json:"tier"`
	Capacity int    `json:"capacity"`
}

// vmssProperties holds the VMSS properties.
type vmssProperties struct {
	ProvisioningState     string                `json:"provisioningState"`
	VirtualMachineProfile vmProfile             `json:"virtualMachineProfile"`
}

// vmProfile represents the virtual machine profile in a VMSS.
type vmProfile struct {
	Priority       string         `json:"priority"`
	EvictionPolicy string         `json:"evictionPolicy"`
	BillingProfile *billingProfile `json:"billingProfile"`
}

// billingProfile holds spot billing info.
type billingProfile struct {
	MaxPrice float64 `json:"maxPrice"`
}

// agentPoolResponse is the ARM response for an AKS agent pool.
type agentPoolResponse struct {
	ID         string              `json:"id"`
	Name       string              `json:"name"`
	Properties agentPoolProperties `json:"properties"`
}

// agentPoolProperties holds the properties of an AKS agent pool.
type agentPoolProperties struct {
	Count              int    `json:"count"`
	VMSize             string `json:"vmSize"`
	MinCount           int    `json:"minCount"`
	MaxCount           int    `json:"maxCount"`
	EnableAutoScaling  bool   `json:"enableAutoScaling"`
	Mode               string `json:"mode"`
	OrchestratorVersion string `json:"orchestratorVersion"`
	ScaleSetPriority   string `json:"scaleSetPriority"`
	ProvisioningState  string `json:"provisioningState"`
	OsDiskSizeGB       int    `json:"osDiskSizeGB"`
	OsDiskType         string `json:"osDiskType"`
}

// agentPoolListResponse is the ARM response for listing agent pools.
type agentPoolListResponse struct {
	Value    []agentPoolResponse `json:"value"`
	NextLink string              `json:"nextLink"`
}

// discoverVMSS discovers AKS Virtual Machine Scale Sets.
func discoverVMSS(ctx context.Context, p *Provider) ([]*cloudprovider.NodeGroup, error) {
	url := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/virtualMachineScaleSets?api-version=%s",
		armBaseURL, p.subscriptionID, p.resourceGroup, computeAPIVersion)

	var allGroups []*cloudprovider.NodeGroup

	// Fetch agent pool info for min/max counts if cluster name is available.
	agentPools := make(map[string]*agentPoolResponse)
	if p.clusterName != "" {
		pools, err := listAgentPools(ctx, p)
		if err == nil {
			for i := range pools {
				agentPools[pools[i].Name] = &pools[i]
			}
		}
	}

	for url != "" {
		resp, err := p.doARMRequest(ctx, "GET", url, nil)
		if err != nil {
			return nil, fmt.Errorf("listing VMSS: %w", err)
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("reading VMSS list response: %w", err)
		}

		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("VMSS list returned status %d: %s", resp.StatusCode, string(body))
		}

		var listResp vmssListResponse
		if err := json.Unmarshal(body, &listResp); err != nil {
			return nil, fmt.Errorf("decoding VMSS list response: %w", err)
		}

		for _, vmss := range listResp.Value {
			// Filter for AKS-managed VMSS by checking for the aks-managed-poolName tag.
			poolName, isAKS := vmss.Tags["aks-managed-poolName"]
			if !isAKS {
				continue
			}

			ng := vmssToNodeGroup(vmss, poolName, p.region)

			// Enrich with agent pool data for min/max counts and disk info.
			if ap, ok := agentPools[poolName]; ok {
				ng.MinCount = ap.Properties.MinCount
				ng.MaxCount = ap.Properties.MaxCount
				ng.DiskSizeGB = ap.Properties.OsDiskSizeGB
				ng.DiskType = ap.Properties.OsDiskType
			}

			allGroups = append(allGroups, ng)
		}

		url = listResp.NextLink
	}

	return allGroups, nil
}

// getVMSS retrieves a single VMSS by name.
func getVMSS(ctx context.Context, p *Provider, id string) (*cloudprovider.NodeGroup, error) {
	url := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/virtualMachineScaleSets/%s?api-version=%s",
		armBaseURL, p.subscriptionID, p.resourceGroup, id, computeAPIVersion)

	resp, err := p.doARMRequest(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("getting VMSS %s: %w", id, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading VMSS response: %w", err)
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("VMSS get returned status %d: %s", resp.StatusCode, string(body))
	}

	var vmss vmssResource
	if err := json.Unmarshal(body, &vmss); err != nil {
		return nil, fmt.Errorf("decoding VMSS response: %w", err)
	}

	poolName := vmss.Tags["aks-managed-poolName"]
	if poolName == "" {
		poolName = vmss.Name
	}

	ng := vmssToNodeGroup(vmss, poolName, p.region)

	// Try to get agent pool info for min/max counts and disk info.
	if p.clusterName != "" {
		ap, err := getAgentPool(ctx, p, poolName)
		if err == nil {
			ng.MinCount = ap.Properties.MinCount
			ng.MaxCount = ap.Properties.MaxCount
			ng.DiskSizeGB = ap.Properties.OsDiskSizeGB
			ng.DiskType = ap.Properties.OsDiskType
		}
	}

	return ng, nil
}

// scaleVMSS sets the capacity of a VMSS.
func scaleVMSS(ctx context.Context, p *Provider, id string, desiredCount int) error {
	url := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/virtualMachineScaleSets/%s?api-version=%s",
		armBaseURL, p.subscriptionID, p.resourceGroup, id, computeAPIVersion)

	payload := map[string]interface{}{
		"sku": map[string]interface{}{
			"capacity": desiredCount,
		},
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshaling scale payload: %w", err)
	}

	resp, err := p.doARMRequest(ctx, "PATCH", url, bytes.NewReader(payloadBytes))
	if err != nil {
		return fmt.Errorf("scaling VMSS %s: %w", id, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 && resp.StatusCode != 202 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("VMSS scale returned status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// setVMSSMinCount sets the minimum node count on the AKS agent pool.
func setVMSSMinCount(ctx context.Context, p *Provider, id string, minCount int) error {
	if p.clusterName == "" {
		return fmt.Errorf("KOPTIMIZER_CLUSTER_NAME is required to set min count on AKS agent pools")
	}

	// Look up the pool name from the VMSS id.
	poolName, err := resolvePoolName(ctx, p, id)
	if err != nil {
		return err
	}

	// Get current agent pool config.
	ap, err := getAgentPool(ctx, p, poolName)
	if err != nil {
		return fmt.Errorf("getting agent pool %s: %w", poolName, err)
	}

	return updateAgentPool(ctx, p, poolName, &agentPoolUpdateRequest{
		Properties: agentPoolUpdateProperties{
			EnableAutoScaling: true,
			MinCount:          minCount,
			MaxCount:          ap.Properties.MaxCount,
			Count:             ap.Properties.Count,
		},
	})
}

// setVMSSMaxCount sets the maximum node count on the AKS agent pool.
func setVMSSMaxCount(ctx context.Context, p *Provider, id string, maxCount int) error {
	if p.clusterName == "" {
		return fmt.Errorf("KOPTIMIZER_CLUSTER_NAME is required to set max count on AKS agent pools")
	}

	poolName, err := resolvePoolName(ctx, p, id)
	if err != nil {
		return err
	}

	ap, err := getAgentPool(ctx, p, poolName)
	if err != nil {
		return fmt.Errorf("getting agent pool %s: %w", poolName, err)
	}

	return updateAgentPool(ctx, p, poolName, &agentPoolUpdateRequest{
		Properties: agentPoolUpdateProperties{
			EnableAutoScaling: true,
			MinCount:          ap.Properties.MinCount,
			MaxCount:          maxCount,
			Count:             ap.Properties.Count,
		},
	})
}

// agentPoolUpdateRequest is the request body for updating an AKS agent pool.
type agentPoolUpdateRequest struct {
	Properties agentPoolUpdateProperties `json:"properties"`
}

// agentPoolUpdateProperties holds the updatable properties of an AKS agent pool.
type agentPoolUpdateProperties struct {
	EnableAutoScaling bool `json:"enableAutoScaling"`
	MinCount          int  `json:"minCount"`
	MaxCount          int  `json:"maxCount"`
	Count             int  `json:"count"`
}

// listAgentPools lists all agent pools for the AKS cluster.
func listAgentPools(ctx context.Context, p *Provider) ([]agentPoolResponse, error) {
	url := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ContainerService/managedClusters/%s/agentPools?api-version=%s",
		armBaseURL, p.subscriptionID, p.resourceGroup, p.clusterName, aksAPIVersion)

	resp, err := p.doARMRequest(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("listing agent pools: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading agent pool list response: %w", err)
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("agent pool list returned status %d: %s", resp.StatusCode, string(body))
	}

	var listResp agentPoolListResponse
	if err := json.Unmarshal(body, &listResp); err != nil {
		return nil, fmt.Errorf("decoding agent pool list: %w", err)
	}

	return listResp.Value, nil
}

// getAgentPool retrieves a single AKS agent pool by name.
func getAgentPool(ctx context.Context, p *Provider, poolName string) (*agentPoolResponse, error) {
	url := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ContainerService/managedClusters/%s/agentPools/%s?api-version=%s",
		armBaseURL, p.subscriptionID, p.resourceGroup, p.clusterName, poolName, aksAPIVersion)

	resp, err := p.doARMRequest(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("getting agent pool %s: %w", poolName, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading agent pool response: %w", err)
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("agent pool get returned status %d: %s", resp.StatusCode, string(body))
	}

	var ap agentPoolResponse
	if err := json.Unmarshal(body, &ap); err != nil {
		return nil, fmt.Errorf("decoding agent pool response: %w", err)
	}

	return &ap, nil
}

// updateAgentPool sends a PUT request to update an AKS agent pool.
func updateAgentPool(ctx context.Context, p *Provider, poolName string, update *agentPoolUpdateRequest) error {
	url := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ContainerService/managedClusters/%s/agentPools/%s?api-version=%s",
		armBaseURL, p.subscriptionID, p.resourceGroup, p.clusterName, poolName, aksAPIVersion)

	payloadBytes, err := json.Marshal(update)
	if err != nil {
		return fmt.Errorf("marshaling agent pool update: %w", err)
	}

	resp, err := p.doARMRequest(ctx, "PUT", url, bytes.NewReader(payloadBytes))
	if err != nil {
		return fmt.Errorf("updating agent pool %s: %w", poolName, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 && resp.StatusCode != 201 && resp.StatusCode != 202 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("agent pool update returned status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// resolvePoolName resolves a VMSS ID to an AKS agent pool name.
// The ID could be the VMSS name directly, or contain the pool name in tags.
func resolvePoolName(ctx context.Context, p *Provider, vmssID string) (string, error) {
	// First, try to get the VMSS and read the pool name from tags.
	ng, err := getVMSS(ctx, p, vmssID)
	if err != nil {
		// If we cannot fetch the VMSS, assume the ID is the pool name.
		return vmssID, nil
	}
	if ng.Name != "" {
		return ng.Name, nil
	}
	return vmssID, nil
}

// vmssToNodeGroup converts a VMSS resource to a NodeGroup.
func vmssToNodeGroup(vmss vmssResource, poolName, region string) *cloudprovider.NodeGroup {
	instanceType := vmss.Sku.Name
	family, _ := familylock.ExtractFamily(instanceType)

	lifecycle := "on-demand"
	if strings.EqualFold(vmss.Properties.VirtualMachineProfile.Priority, "Spot") {
		lifecycle = "spot"
	}

	// Extract min/max from VMSS tags if available (fallback when agent pool API is not used).
	minCount := 0
	maxCount := 0
	if v, ok := vmss.Tags["aks-managed-autoScalerMinCount"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			minCount = n
		}
	}
	if v, ok := vmss.Tags["aks-managed-autoScalerMaxCount"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			maxCount = n
		}
	}

	labels := make(map[string]string)
	for k, v := range vmss.Tags {
		labels[k] = v
	}

	return &cloudprovider.NodeGroup{
		ID:             vmss.Name,
		Name:           poolName,
		InstanceType:   instanceType,
		InstanceFamily: family,
		CurrentCount:   vmss.Sku.Capacity,
		DesiredCount:   vmss.Sku.Capacity,
		MinCount:       minCount,
		MaxCount:       maxCount,
		Region:         region,
		Labels:         labels,
		Lifecycle:      lifecycle,
	}
}

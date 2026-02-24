package gcp

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/koptimizer/koptimizer/internal/store"
	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
	"github.com/koptimizer/koptimizer/pkg/cost"
	"github.com/koptimizer/koptimizer/pkg/familylock"
)

// Provider implements cloudprovider.CloudProvider for GCP GKE.
type Provider struct {
	region       string
	project      string
	clusterName  string
	httpClient   *http.Client
	tokenSource  oauth2.TokenSource
	pricingCache *store.PricingCache
}

func NewProvider(region string, pricingCache *store.PricingCache) (*Provider, error) {
	project := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if project == "" {
		project = os.Getenv("GCP_PROJECT")
	}
	if project == "" {
		return nil, fmt.Errorf("GCP project not configured: set GOOGLE_CLOUD_PROJECT or GCP_PROJECT")
	}

	clusterName := os.Getenv("KOPTIMIZER_CLUSTER_NAME")
	if clusterName == "" {
		return nil, fmt.Errorf("cluster name not configured: set KOPTIMIZER_CLUSTER_NAME")
	}

	ctx := context.Background()
	creds, err := google.FindDefaultCredentials(ctx,
		"https://www.googleapis.com/auth/cloud-platform",
		"https://www.googleapis.com/auth/compute",
	)
	if err != nil {
		return nil, fmt.Errorf("finding GCP credentials: %w", err)
	}

	ts := creds.TokenSource
	httpClient := oauth2.NewClient(ctx, ts)
	httpClient.Timeout = 30 * time.Second

	return &Provider{
		region:       region,
		project:      project,
		clusterName:  clusterName,
		httpClient:   httpClient,
		tokenSource:  ts,
		pricingCache: pricingCache,
	}, nil
}

func (p *Provider) Name() string { return "gcp" }

func (p *Provider) GetInstanceTypes(ctx context.Context, region string) ([]*cloudprovider.InstanceType, error) {
	return getGCPMachineTypes(ctx, p.project, region, p.httpClient)
}

func (p *Provider) GetCurrentPricing(ctx context.Context, region string) (*cloudprovider.PricingInfo, error) {
	return getGCPPricing(ctx, p.project, region, p.httpClient, p.pricingCache)
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
	isPreemptible := false
	if v, ok := node.Labels["cloud.google.com/gke-preemptible"]; ok && v == "true" {
		isPreemptible = true
	}
	if v, ok := node.Labels["cloud.google.com/gke-spot"]; ok && v == "true" {
		isPreemptible = true
	}
	spotDiscount := 0.0
	if isPreemptible {
		family := extractGCPFamily(instanceType)
		spotDiscount = estimateSpotDiscount(family) * 100
	}
	// Apply spot/preemptible discount to reflect actual cost, not on-demand rate.
	effectivePrice := price
	if isPreemptible && spotDiscount > 0 {
		effectivePrice = price * (1 - spotDiscount/100)
	}
	return &cloudprovider.NodeCost{
		NodeName:       node.Name,
		InstanceType:   instanceType,
		HourlyCostUSD:  effectivePrice,
		MonthlyCostUSD: effectivePrice * cost.HoursPerMonth,
		IsSpot:         isPreemptible,
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
	return discoverNodePools(ctx, p.project, p.region, p.clusterName, p.httpClient)
}

func (p *Provider) GetNodeGroup(ctx context.Context, id string) (*cloudprovider.NodeGroup, error) {
	return getNodePool(ctx, p.project, p.region, p.clusterName, id, p.httpClient)
}

func (p *Provider) ScaleNodeGroup(ctx context.Context, id string, desiredCount int) error {
	if desiredCount < 0 {
		return fmt.Errorf("invalid desired count %d for node pool %s: must be >= 0", desiredCount, id)
	}
	ng, err := p.GetNodeGroup(ctx, id)
	if err != nil {
		return fmt.Errorf("cannot validate bounds for node pool %s: %w", id, err)
	}
	if desiredCount < ng.MinCount {
		return fmt.Errorf("desired count %d is below min %d for node pool %s", desiredCount, ng.MinCount, id)
	}
	if desiredCount > ng.MaxCount {
		return fmt.Errorf("desired count %d exceeds max %d for node pool %s", desiredCount, ng.MaxCount, id)
	}
	return scaleNodePool(ctx, p.project, p.region, p.clusterName, id, desiredCount, p.httpClient)
}

func (p *Provider) SetNodeGroupMinCount(ctx context.Context, id string, minCount int) error {
	return setNodePoolAutoscaling(ctx, p.project, p.region, p.clusterName, id, &minCount, nil, p.httpClient)
}

func (p *Provider) SetNodeGroupMaxCount(ctx context.Context, id string, maxCount int) error {
	return setNodePoolAutoscaling(ctx, p.project, p.region, p.clusterName, id, nil, &maxCount, p.httpClient)
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
		if strings.HasPrefix(t.Name, family) {
			result = append(result, t)
		}
	}
	return result, nil
}

func (p *Provider) GetReservedInstances(ctx context.Context) ([]*cloudprovider.Commitment, error) {
	return []*cloudprovider.Commitment{}, nil // GCP doesn't have RIs
}

func (p *Provider) GetSavingsPlans(ctx context.Context) ([]*cloudprovider.Commitment, error) {
	return []*cloudprovider.Commitment{}, nil // GCP doesn't have Savings Plans
}

func (p *Provider) GetCommittedUseDiscounts(ctx context.Context) ([]*cloudprovider.Commitment, error) {
	return getGCPCUDs(ctx, p.project, p.region, p.httpClient)
}

func (p *Provider) GetReservations(ctx context.Context) ([]*cloudprovider.Commitment, error) {
	return []*cloudprovider.Commitment{}, nil
}

// EstimateSpotDiscount implements cloudprovider.SpotDiscountEstimator.
func (p *Provider) EstimateSpotDiscount(instanceType string) float64 {
	family := extractGCPFamily(instanceType)
	return estimateSpotDiscount(family)
}

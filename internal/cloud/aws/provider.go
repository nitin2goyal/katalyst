package aws

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/savingsplans"
	corev1 "k8s.io/api/core/v1"

	"github.com/koptimizer/koptimizer/internal/store"
	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
	"github.com/koptimizer/koptimizer/pkg/cost"
)

// Provider implements cloudprovider.CloudProvider for AWS EKS.
type Provider struct {
	region    string
	ec2Client *ec2.Client
	asgClient *autoscaling.Client
	spClient  *savingsplans.Client
	pricing   *PricingService
}

func NewProvider(region string, pricingCache *store.PricingCache) (*Provider, error) {
	cfg, err := config.LoadDefaultConfig(context.Background(), config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return &Provider{
		region:    region,
		ec2Client: ec2.NewFromConfig(cfg),
		asgClient: autoscaling.NewFromConfig(cfg),
		spClient:  savingsplans.NewFromConfig(cfg),
		pricing:   NewPricingService(cfg, pricingCache),
	}, nil
}

func (p *Provider) Name() string { return "aws" }

// StartBackgroundRefresh starts proactive pricing cache refresh.
// Call this after the provider is created and a context is available.
func (p *Provider) StartBackgroundRefresh(ctx context.Context) {
	p.pricing.StartBackgroundRefresh(ctx)
}

func (p *Provider) GetInstanceTypes(ctx context.Context, region string) ([]*cloudprovider.InstanceType, error) {
	return p.pricing.GetInstanceTypes(ctx, region)
}

func (p *Provider) GetCurrentPricing(ctx context.Context, region string) (*cloudprovider.PricingInfo, error) {
	return p.pricing.GetCurrentPricing(ctx, region)
}

func (p *Provider) GetNodeCost(ctx context.Context, node *corev1.Node) (*cloudprovider.NodeCost, error) {
	instanceType, err := p.GetNodeInstanceType(ctx, node)
	if err != nil {
		return nil, err
	}
	price, err := p.pricing.GetPrice(ctx, p.region, instanceType)
	if err != nil {
		return nil, err
	}
	isSpot := false
	if lc, ok := node.Labels["node.kubernetes.io/lifecycle"]; ok && lc == "spot" {
		isSpot = true
	}
	spotDiscount := 0.0
	if isSpot {
		// Use estimated discount since GetPrice returns on-demand rates
		family := extractAWSFamily(instanceType)
		spotDiscount = estimateSpotDiscount(family) * 100
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
			gpuTypes = append(gpuTypes, &cloudprovider.GPUInstanceType{
				InstanceType: *t,
			})
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
	return "", fmt.Errorf("instance type label not found on node %s", node.Name)
}

func (p *Provider) GetNodeRegion(ctx context.Context, node *corev1.Node) (string, error) {
	if r, ok := node.Labels["topology.kubernetes.io/region"]; ok {
		return r, nil
	}
	if r, ok := node.Labels["failure-domain.beta.kubernetes.io/region"]; ok {
		return r, nil
	}
	return p.region, nil
}

func (p *Provider) GetNodeZone(ctx context.Context, node *corev1.Node) (string, error) {
	if z, ok := node.Labels["topology.kubernetes.io/zone"]; ok {
		return z, nil
	}
	if z, ok := node.Labels["failure-domain.beta.kubernetes.io/zone"]; ok {
		return z, nil
	}
	return "", fmt.Errorf("zone label not found on node %s", node.Name)
}

func (p *Provider) DiscoverNodeGroups(ctx context.Context) ([]*cloudprovider.NodeGroup, error) {
	groups, err := discoverASGs(ctx, p.asgClient)
	if err != nil {
		return nil, err
	}
	for _, ng := range groups {
		ng.Region = p.region
	}
	return groups, nil
}

func (p *Provider) GetNodeGroup(ctx context.Context, id string) (*cloudprovider.NodeGroup, error) {
	ng, err := getASG(ctx, p.asgClient, id)
	if err != nil {
		return nil, err
	}
	ng.Region = p.region
	return ng, nil
}

func (p *Provider) ScaleNodeGroup(ctx context.Context, id string, desiredCount int) error {
	if desiredCount < 0 {
		return fmt.Errorf("invalid desired count %d for ASG %s: must be >= 0", desiredCount, id)
	}
	ng, err := p.GetNodeGroup(ctx, id)
	if err != nil {
		return fmt.Errorf("cannot validate bounds for ASG %s: %w", id, err)
	}
	if desiredCount < ng.MinCount {
		return fmt.Errorf("desired count %d is below min %d for ASG %s", desiredCount, ng.MinCount, id)
	}
	if desiredCount > ng.MaxCount {
		return fmt.Errorf("desired count %d exceeds max %d for ASG %s", desiredCount, ng.MaxCount, id)
	}
	return scaleASG(ctx, p.asgClient, id, desiredCount)
}

func (p *Provider) SetNodeGroupMinCount(ctx context.Context, id string, minCount int) error {
	return setASGMinCount(ctx, p.asgClient, id, minCount)
}

func (p *Provider) SetNodeGroupMaxCount(ctx context.Context, id string, maxCount int) error {
	return setASGMaxCount(ctx, p.asgClient, id, maxCount)
}

func (p *Provider) GetFamilySizes(ctx context.Context, instanceType string) ([]*cloudprovider.InstanceType, error) {
	parts := strings.SplitN(instanceType, ".", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid instance type format: %s", instanceType)
	}
	family := parts[0]
	allTypes, err := p.GetInstanceTypes(ctx, p.region)
	if err != nil {
		return nil, err
	}
	var familyTypes []*cloudprovider.InstanceType
	for _, t := range allTypes {
		if t.Family == family {
			familyTypes = append(familyTypes, t)
		}
	}
	return familyTypes, nil
}

func (p *Provider) GetReservedInstances(ctx context.Context) ([]*cloudprovider.Commitment, error) {
	return getReservedInstances(ctx, p.ec2Client)
}

func (p *Provider) GetSavingsPlans(ctx context.Context) ([]*cloudprovider.Commitment, error) {
	return getSavingsPlans(ctx, p.spClient)
}

func (p *Provider) GetCommittedUseDiscounts(ctx context.Context) ([]*cloudprovider.Commitment, error) {
	return []*cloudprovider.Commitment{}, nil // AWS doesn't have CUDs
}

func (p *Provider) GetReservations(ctx context.Context) ([]*cloudprovider.Commitment, error) {
	return []*cloudprovider.Commitment{}, nil // AWS uses RIs, not Azure-style Reservations
}

// EstimateSpotDiscount implements cloudprovider.SpotDiscountEstimator.
func (p *Provider) EstimateSpotDiscount(instanceType string) float64 {
	family := extractAWSFamily(instanceType)
	return estimateSpotDiscount(family)
}

package nodetemplates

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/internal/state"
	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// NodeTemplate defines constraints for selecting instance types for a node group.
type NodeTemplate struct {
	Name               string   `yaml:"name"`
	AllowedFamilies    []string `yaml:"allowedFamilies"`    // e.g., ["m5", "m6i", "c5"]
	BlockedFamilies    []string `yaml:"blockedFamilies"`    // e.g., ["p3", "g5"]
	AllowedAZs         []string `yaml:"allowedAZs"`         // e.g., ["us-east-1a", "us-east-1b"]
	MinCPU             int      `yaml:"minCPU"`             // minimum vCPUs per instance
	MaxCPU             int      `yaml:"maxCPU"`             // maximum vCPUs per instance (0 = unlimited)
	MinMemoryMiB       int      `yaml:"minMemoryMiB"`
	MaxMemoryMiB       int      `yaml:"maxMemoryMiB"`       // 0 = unlimited
	Architectures      []string `yaml:"architectures"`      // ["amd64"], ["arm64"], or both
	GPURequired        bool     `yaml:"gpuRequired"`
	SpotAllowed        bool     `yaml:"spotAllowed"`
	MaxPricePerHour    float64  `yaml:"maxPricePerHour"`    // 0 = no limit
}

// ScoredInstance is an instance type with a price-performance score.
type ScoredInstance struct {
	InstanceType *cloudprovider.InstanceType
	Score        float64 // lower is better (cost per effective CPU core)
	Reason       string
}

// Controller recommends optimal instance types for node groups based on
// templates and current pricing.
type Controller struct {
	provider  cloudprovider.CloudProvider
	state     *state.ClusterState
	config    *config.Config
	templates []NodeTemplate
}

func NewController(mgr ctrl.Manager, provider cloudprovider.CloudProvider, st *state.ClusterState, cfg *config.Config) *Controller {
	return &Controller{
		provider:  provider,
		state:     st,
		config:    cfg,
		templates: defaultTemplates(cfg.CloudProvider),
	}
}

// defaultTemplates returns cloud-appropriate instance family templates.
func defaultTemplates(cloudProvider string) []NodeTemplate {
	switch cloudProvider {
	case "gcp":
		return []NodeTemplate{
			{Name: "general-purpose", AllowedFamilies: []string{"n2-standard", "n2d-standard", "e2-standard", "n1-standard", "c3-standard"}, Architectures: []string{"amd64"}, SpotAllowed: true},
			{Name: "compute-optimized", AllowedFamilies: []string{"c2-standard", "c2d-standard", "c3-highcpu", "h3-standard"}, Architectures: []string{"amd64"}, SpotAllowed: true},
			{Name: "memory-optimized", AllowedFamilies: []string{"n2-highmem", "n2d-highmem", "m2-ultramem", "m1-ultramem"}, Architectures: []string{"amd64"}, SpotAllowed: true},
			{Name: "arm-general", AllowedFamilies: []string{"t2a-standard"}, Architectures: []string{"arm64"}, SpotAllowed: true},
			{Name: "gpu", GPURequired: true, SpotAllowed: false},
		}
	case "azure":
		return []NodeTemplate{
			{Name: "general-purpose", AllowedFamilies: []string{"Standard_D_v3", "Standard_D_v4", "Standard_D_v5", "Standard_D_v6"}, Architectures: []string{"amd64"}, SpotAllowed: true},
			{Name: "compute-optimized", AllowedFamilies: []string{"Standard_F_v2", "Standard_F_v3"}, Architectures: []string{"amd64"}, SpotAllowed: true},
			{Name: "memory-optimized", AllowedFamilies: []string{"Standard_E_v3", "Standard_E_v4", "Standard_E_v5"}, Architectures: []string{"amd64"}, SpotAllowed: true},
			{Name: "arm-general", AllowedFamilies: []string{"Standard_Dp_v5"}, Architectures: []string{"arm64"}, SpotAllowed: true},
			{Name: "gpu", GPURequired: true, SpotAllowed: false},
		}
	default: // aws
		return []NodeTemplate{
			{Name: "general-purpose", AllowedFamilies: []string{"m5", "m6i", "m6a", "m7i", "m7a"}, Architectures: []string{"amd64"}, SpotAllowed: true},
			{Name: "compute-optimized", AllowedFamilies: []string{"c5", "c6i", "c6a", "c7i", "c7a"}, Architectures: []string{"amd64"}, SpotAllowed: true},
			{Name: "memory-optimized", AllowedFamilies: []string{"r5", "r6i", "r6a", "r7i", "r7a"}, Architectures: []string{"amd64"}, SpotAllowed: true},
			{Name: "arm-general", AllowedFamilies: []string{"m6g", "m7g"}, Architectures: []string{"arm64"}, SpotAllowed: true},
			{Name: "gpu", GPURequired: true, SpotAllowed: false},
		}
	}
}

func (c *Controller) SetupWithManager(mgr ctrl.Manager) error {
	return mgr.Add(c)
}

// Start implements manager.Runnable.
func (c *Controller) Start(ctx context.Context) error {
	c.run(ctx)
	return nil
}

func (c *Controller) Name() string { return "node-templates" }

func (c *Controller) Analyze(ctx context.Context, snapshot *optimizer.ClusterSnapshot) ([]optimizer.Recommendation, error) {
	var recs []optimizer.Recommendation

	// Get available instance types with pricing
	instanceTypes, err := c.provider.GetInstanceTypes(ctx, c.config.Region)
	if err != nil {
		return nil, fmt.Errorf("getting instance types: %w", err)
	}

	// For each node group, check if there's a better instance type
	for _, ng := range snapshot.NodeGroups {
		if ng.CurrentCount == 0 {
			continue
		}

		// Find matching template
		template := c.matchTemplate(ng)
		if template == nil {
			continue
		}

		// Score all eligible instance types
		eligible := c.filterEligible(instanceTypes, template)
		scored := c.scoreInstances(eligible, ng)

		if len(scored) == 0 {
			continue
		}

		// Sort by score (lower is better)
		sort.Slice(scored, func(i, j int) bool {
			return scored[i].Score < scored[j].Score
		})

		// Compare best option against current
		best := scored[0]
		if best.InstanceType.Name == ng.InstanceType {
			continue // Already using the best option
		}

		// Find current instance type pricing
		var currentPrice float64
		for _, it := range instanceTypes {
			if it.Name == ng.InstanceType {
				currentPrice = it.PricePerHour
				break
			}
		}

		if currentPrice == 0 || best.InstanceType.PricePerHour >= currentPrice {
			continue // No savings
		}

		savingsPerNode := (currentPrice - best.InstanceType.PricePerHour) * 730
		totalMonthlySavings := savingsPerNode * float64(ng.CurrentCount)

		// Only recommend if savings are meaningful (>10%)
		if totalMonthlySavings/currentPrice/730/float64(ng.CurrentCount) < 0.10 {
			continue
		}

		recs = append(recs, optimizer.Recommendation{
			ID:             fmt.Sprintf("instance-upgrade-%s", ng.ID),
			Type:           optimizer.RecommendationNodeGroupAdjust,
			Priority:       optimizer.PriorityMedium,
			AutoExecutable: false,
			TargetKind:     "NodeGroup",
			TargetName:     ng.Name,
			Summary:        fmt.Sprintf("Switch %s from %s to %s — save $%.0f/month (%d nodes)", ng.Name, ng.InstanceType, best.InstanceType.Name, totalMonthlySavings, ng.CurrentCount),
			ActionSteps: []string{
				fmt.Sprintf("Update node group %s instance type from %s to %s", ng.Name, ng.InstanceType, best.InstanceType.Name),
				fmt.Sprintf("New type: %d vCPU, %d MiB RAM at $%.4f/hr (vs $%.4f/hr current)", best.InstanceType.CPUCores, best.InstanceType.MemoryMiB, best.InstanceType.PricePerHour, currentPrice),
				"Rolling update: new nodes provision first, then old nodes drain",
				best.Reason,
			},
			EstimatedSaving: optimizer.SavingEstimate{
				MonthlySavingsUSD: totalMonthlySavings,
				AnnualSavingsUSD:  totalMonthlySavings * 12,
				Currency:          "USD",
			},
			Details: map[string]string{
				"action":           "change-instance-type",
				"nodeGroupID":      ng.ID,
				"currentType":      ng.InstanceType,
				"recommendedType":  best.InstanceType.Name,
				"savingsPerNode":   fmt.Sprintf("%.2f", savingsPerNode),
				"template":         template.Name,
			},
		})
	}

	return recs, nil
}

func (c *Controller) Execute(ctx context.Context, rec optimizer.Recommendation) error {
	// Instance type changes are significant infrastructure changes.
	// Always generate as non-auto-executable recommendations.
	return nil
}

// matchTemplate finds the first template that matches the node group's characteristics.
func (c *Controller) matchTemplate(ng *cloudprovider.NodeGroup) *NodeTemplate {
	for i := range c.templates {
		t := &c.templates[i]
		if t.GPURequired {
			// GPU template matches if instance family starts with gpu families
			if isGPUFamily(ng.InstanceFamily) {
				return t
			}
			continue
		}
		if len(t.AllowedFamilies) > 0 {
			found := false
			for _, f := range t.AllowedFamilies {
				if f == ng.InstanceFamily {
					found = true
					break
				}
			}
			if found {
				return t
			}
		}
	}
	// Default: return first non-GPU template
	for i := range c.templates {
		if !c.templates[i].GPURequired {
			return &c.templates[i]
		}
	}
	return nil
}

func isGPUFamily(family string) bool {
	gpuFamilies := map[string]bool{
		// AWS
		"p3": true, "p4d": true, "p5": true,
		"g4dn": true, "g5": true, "g6": true,
		"inf1": true, "inf2": true, "trn1": true,
		// GCP
		"a2-highgpu": true, "a2-ultragpu": true, "a3-highgpu": true,
		"g2-standard": true,
		// Azure
		"Standard_N": true, "Standard_NC": true, "Standard_ND": true, "Standard_NV": true,
	}
	// Check prefix match for Azure families
	for gpuFamily := range gpuFamilies {
		if strings.HasPrefix(family, gpuFamily) {
			return true
		}
	}
	return gpuFamilies[family]
}

// filterEligible returns instance types that match the template constraints.
func (c *Controller) filterEligible(types []*cloudprovider.InstanceType, t *NodeTemplate) []*cloudprovider.InstanceType {
	blocked := make(map[string]bool)
	for _, f := range t.BlockedFamilies {
		blocked[f] = true
	}

	allowed := make(map[string]bool)
	for _, f := range t.AllowedFamilies {
		allowed[f] = true
	}

	archSet := make(map[string]bool)
	for _, a := range t.Architectures {
		archSet[a] = true
	}

	var eligible []*cloudprovider.InstanceType
	for _, it := range types {
		if blocked[it.Family] {
			continue
		}
		if len(allowed) > 0 && !allowed[it.Family] {
			continue
		}
		if len(archSet) > 0 && !archSet[it.Architecture] {
			continue
		}
		if t.MinCPU > 0 && it.CPUCores < t.MinCPU {
			continue
		}
		if t.MaxCPU > 0 && it.CPUCores > t.MaxCPU {
			continue
		}
		if t.MinMemoryMiB > 0 && it.MemoryMiB < t.MinMemoryMiB {
			continue
		}
		if t.MaxMemoryMiB > 0 && it.MemoryMiB > t.MaxMemoryMiB {
			continue
		}
		if t.MaxPricePerHour > 0 && it.PricePerHour > t.MaxPricePerHour {
			continue
		}
		if t.GPURequired && it.GPUs == 0 {
			continue
		}
		if !t.GPURequired && it.GPUs > 0 {
			continue
		}
		eligible = append(eligible, it)
	}
	return eligible
}

// scoreInstances ranks instance types by price-performance for a given node group's workload.
func (c *Controller) scoreInstances(types []*cloudprovider.InstanceType, ng *cloudprovider.NodeGroup) []ScoredInstance {
	var scored []ScoredInstance

	for _, it := range types {
		if it.CPUCores == 0 {
			continue
		}
		// Score = cost per vCPU-hour (lower is better)
		score := it.PricePerHour / float64(it.CPUCores)

		reason := fmt.Sprintf("%s: %d vCPU, %d MiB, $%.4f/hr ($%.4f/vCPU-hr)",
			it.Name, it.CPUCores, it.MemoryMiB, it.PricePerHour, score)

		scored = append(scored, ScoredInstance{
			InstanceType: it,
			Score:        score,
			Reason:       reason,
		})
	}

	return scored
}

func (c *Controller) run(ctx context.Context) {
	logger := log.FromContext(ctx).WithName("node-templates")
	ticker := time.NewTicker(10 * time.Minute) // less frequent, pricing doesn't change often
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			snapshot := c.state.Snapshot()
			recs, err := c.Analyze(ctx, snapshot)
			if err != nil {
				logger.Error(err, "Node template analysis failed")
				continue
			}
			_ = recs
		case <-ctx.Done():
			return
		}
	}
}

package hibernation

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/koptimizer/koptimizer/internal/config"
	intmetrics "github.com/koptimizer/koptimizer/internal/metrics"
	"github.com/koptimizer/koptimizer/internal/state"
	"github.com/koptimizer/koptimizer/pkg/aigate"
	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
	"github.com/koptimizer/koptimizer/pkg/cost"
	"github.com/koptimizer/koptimizer/pkg/familylock"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

const hibernationStateConfigMap = "koptimizer-hibernation-state"
const hibernationStateNamespace = "kube-system"

// Controller manages cluster hibernation: scaling node groups to zero on
// schedule (nights, weekends) and restoring them on wake schedule.
// Preserves node group min/max/desired counts for restoration.
type Controller struct {
	client   client.Client
	provider cloudprovider.CloudProvider
	state    *state.ClusterState
	guard    *familylock.FamilyLockGuard
	gate     *aigate.AIGate
	config   *config.Config

	mu              sync.Mutex
	hibernated      bool
	savedDesired    map[string]int // nodeGroupID -> saved desired count
	savedMin        map[string]int // nodeGroupID -> saved min count
	cron            *cron.Cron
}

// SavedState holds the pre-hibernation state for restoration.
type SavedState struct {
	NodeGroupID  string
	DesiredCount int
	MinCount     int
}

func NewController(mgr ctrl.Manager, provider cloudprovider.CloudProvider, st *state.ClusterState, guard *familylock.FamilyLockGuard, gate *aigate.AIGate, cfg *config.Config) *Controller {
	return &Controller{
		client:       mgr.GetClient(),
		provider:     provider,
		state:        st,
		guard:        guard,
		gate:         gate,
		config:       cfg,
		savedDesired: make(map[string]int),
		savedMin:     make(map[string]int),
		cron:         cron.New(),
	}
}

func (c *Controller) SetupWithManager(mgr ctrl.Manager) error {
	logger := log.Log.WithName("hibernation")

	// Restore persisted hibernation state (survives pod restarts).
	// Use a short-lived background context for the one-time load; the
	// manager context is not available yet at setup time.
	if err := c.loadState(context.Background()); err != nil {
		logger.Error(err, "Failed to load hibernation state, starting fresh")
	}

	// Pre-validate cron schedules before the manager starts.
	for _, s := range c.config.Hibernation.Schedules {
		if _, err := cron.ParseStandard(s); err != nil {
			return fmt.Errorf("invalid hibernate schedule %q: %w", s, err)
		}
	}
	for _, s := range c.config.Hibernation.WakeSchedules {
		if _, err := cron.ParseStandard(s); err != nil {
			return fmt.Errorf("invalid wake schedule %q: %w", s, err)
		}
	}

	// Register as a manager.Runnable so we get a lifecycle-managed context.
	return mgr.Add(c)
}

// Start implements manager.Runnable. It registers cron schedules with a
// context that is cancelled on shutdown, then enters the monitoring loop.
func (c *Controller) Start(ctx context.Context) error {
	logger := log.FromContext(ctx).WithName("hibernation")

	// Register hibernate schedules with the manager-provided context.
	for _, schedule := range c.config.Hibernation.Schedules {
		s := schedule
		if _, err := c.cron.AddFunc(s, func() {
			if err := c.Hibernate(ctx); err != nil {
				logger.Error(err, "Hibernate failed", "schedule", s)
			}
		}); err != nil {
			return fmt.Errorf("invalid hibernate schedule %q: %w", s, err)
		}
	}

	// Register wake schedules.
	for _, schedule := range c.config.Hibernation.WakeSchedules {
		s := schedule
		if _, err := c.cron.AddFunc(s, func() {
			if err := c.Wake(ctx); err != nil {
				logger.Error(err, "Wake failed", "schedule", s)
			}
		}); err != nil {
			return fmt.Errorf("invalid wake schedule %q: %w", s, err)
		}
	}

	c.cron.Start()
	c.run(ctx)
	return nil
}

func (c *Controller) Name() string { return "hibernation" }

func (c *Controller) Analyze(ctx context.Context, snapshot *optimizer.ClusterSnapshot) ([]optimizer.Recommendation, error) {
	c.mu.Lock()
	isHibernated := c.hibernated
	c.mu.Unlock()

	hibernatedCount := 0
	if isHibernated {
		hibernatedCount = len(snapshot.NodeGroups)
	}
	intmetrics.HibernatedNodeGroups.Set(float64(hibernatedCount))

	// Estimate savings from hibernation if not currently hibernated
	if !isHibernated && len(c.config.Hibernation.Schedules) > 0 {
		var totalHourlyCost float64
		for _, node := range snapshot.Nodes {
			totalHourlyCost += node.HourlyCostUSD
		}
		// Estimate: if hibernating 12 hours/day on weekdays = ~36% time savings.
		// Subtract cost of nodes kept alive during hibernation (1 per group for safety).
		keptAliveHourly := float64(0)
		if len(snapshot.NodeGroups) > 0 && len(snapshot.Nodes) > 0 {
			avgNodeHourly := totalHourlyCost / float64(len(snapshot.Nodes))
			keptAliveHourly = avgNodeHourly * float64(len(snapshot.NodeGroups))
		}
		monthlySavings := (totalHourlyCost - keptAliveHourly) * cost.HoursPerMonth * 0.36
		if monthlySavings < 0 {
			monthlySavings = 0
		}
		intmetrics.HibernationSavingsUSD.Set(monthlySavings)
	}

	return nil, nil
}

func (c *Controller) Execute(ctx context.Context, rec optimizer.Recommendation) error {
	action := rec.Details["action"]
	switch action {
	case "hibernate":
		return c.Hibernate(ctx)
	case "wake":
		return c.Wake(ctx)
	default:
		return nil
	}
}

// Hibernate scales all non-excluded node groups to zero, saving their state.
func (c *Controller) Hibernate(ctx context.Context) error {
	logger := log.FromContext(ctx).WithName("hibernation")

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.hibernated {
		logger.Info("Already hibernated, skipping")
		return nil
	}

	if c.config.Mode != "active" {
		logger.Info("Not in active mode, skipping hibernate")
		return nil
	}

	// Family lock guard validation
	if c.guard != nil {
		if err := c.guard.ValidateNodeGroupAction(familylock.NodeGroupScale); err != nil {
			logger.Info("Family lock guard blocked hibernation", "error", err)
			return err
		}
	}

	// AI Gate validation for hibernation (high-impact operation)
	if c.gate != nil {
		rec := optimizer.Recommendation{
			Summary:        "Hibernate cluster: scale all non-excluded node groups to minimum",
			RequiresAIGate: true,
			EstimatedImpact: optimizer.ImpactEstimate{
				RiskLevel: "high",
			},
		}
		if c.gate.RequiresValidation(rec) {
			valReq := aigate.ValidationRequest{
				Action:         rec.Summary,
				Recommendation: rec,
				RiskFactors:    []string{"Cluster hibernation scales most node groups to minimum"},
			}
			result, err := c.gate.Validate(ctx, valReq)
			if err != nil || !result.Approved {
				logger.Info("AI Gate rejected hibernation", "reasoning", result.Reasoning)
				return nil
			}
		}
	}

	groups, err := c.provider.DiscoverNodeGroups(ctx)
	if err != nil {
		return fmt.Errorf("discovering node groups: %w", err)
	}

	excludeSet := make(map[string]bool)
	for _, name := range c.config.Hibernation.ExcludeGroups {
		excludeSet[name] = true
	}

	hibernatedCount := 0
	for _, ng := range groups {
		if excludeSet[ng.Name] {
			logger.Info("Skipping excluded node group", "nodeGroup", ng.Name)
			continue
		}

		// Save current state for restoration
		c.savedDesired[ng.ID] = ng.DesiredCount
		c.savedMin[ng.ID] = ng.MinCount

		targetCount := 1 // Always preserve at least 1 node per group for safety

		// Temporarily set min to allow scaling to target
		if ng.MinCount > targetCount {
			if err := c.provider.SetNodeGroupMinCount(ctx, ng.ID, targetCount); err != nil {
				logger.Error(err, "Failed to set min count", "nodeGroup", ng.Name)
				// Roll back saved state for this group since we couldn't hibernate it
				delete(c.savedDesired, ng.ID)
				delete(c.savedMin, ng.ID)
				continue
			}
		}

		if err := c.provider.ScaleNodeGroup(ctx, ng.ID, targetCount); err != nil {
			logger.Error(err, "Failed to hibernate node group", "nodeGroup", ng.Name)
			// Roll back saved state for this group since we couldn't hibernate it
			delete(c.savedDesired, ng.ID)
			delete(c.savedMin, ng.ID)
			continue
		}

		hibernatedCount++
		logger.Info("Hibernated node group",
			"nodeGroup", ng.Name,
			"previousDesired", ng.DesiredCount,
			"newDesired", targetCount,
		)
	}

	// Only mark as hibernated if at least one group was successfully hibernated.
	if hibernatedCount == 0 {
		logger.Error(nil, "All node group hibernations failed, not marking cluster as hibernated")
		return fmt.Errorf("hibernation failed: no node groups were successfully hibernated")
	}
	c.hibernated = true

	// Persist state to ConfigMap so it survives pod restarts
	if err := c.saveState(ctx); err != nil {
		logger.Error(err, "Failed to persist hibernation state")
	}

	logger.Info("Cluster hibernation complete")
	return nil
}

// Wake restores node groups to their pre-hibernation desired counts.
func (c *Controller) Wake(ctx context.Context) error {
	logger := log.FromContext(ctx).WithName("hibernation")

	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.hibernated {
		logger.Info("Not hibernated, skipping wake")
		return nil
	}

	if c.config.Mode != "active" {
		logger.Info("Not in active mode, skipping wake")
		return nil
	}

	groups, err := c.provider.DiscoverNodeGroups(ctx)
	if err != nil {
		return fmt.Errorf("discovering node groups: %w", err)
	}

	for _, ng := range groups {
		savedDesired, ok := c.savedDesired[ng.ID]
		if !ok {
			continue
		}

		// Restore min count first from saved state (ng.MinCount is already the
		// lowered value, so we must use the pre-hibernation saved value)
		if savedMin, ok := c.savedMin[ng.ID]; ok && savedMin > ng.MinCount {
			if err := c.provider.SetNodeGroupMinCount(ctx, ng.ID, savedMin); err != nil {
				logger.Error(err, "Failed to restore min count", "nodeGroup", ng.Name)
			}
		}

		if err := c.provider.ScaleNodeGroup(ctx, ng.ID, savedDesired); err != nil {
			logger.Error(err, "Failed to wake node group", "nodeGroup", ng.Name)
			continue
		}

		logger.Info("Woke node group",
			"nodeGroup", ng.Name,
			"desiredCount", savedDesired,
		)
	}

	c.savedDesired = make(map[string]int)
	c.savedMin = make(map[string]int)
	c.hibernated = false

	// Persist cleared state so a restart doesn't re-trigger wake
	if err := c.saveState(ctx); err != nil {
		logger.Error(err, "Failed to persist hibernation state after wake")
	}

	logger.Info("Cluster wake complete")
	return nil
}

func (c *Controller) saveState(ctx context.Context) error {
	data := make(map[string]string)
	data["hibernated"] = fmt.Sprintf("%v", c.hibernated)
	for id, count := range c.savedDesired {
		data["desired-"+id] = fmt.Sprintf("%d", count)
	}
	for id, count := range c.savedMin {
		data["min-"+id] = fmt.Sprintf("%d", count)
	}

	cm := &corev1.ConfigMap{}
	err := c.client.Get(ctx, client.ObjectKey{Name: hibernationStateConfigMap, Namespace: hibernationStateNamespace}, cm)
	if err != nil {
		// Create new ConfigMap
		cm = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      hibernationStateConfigMap,
				Namespace: hibernationStateNamespace,
			},
			Data: data,
		}
		return c.client.Create(ctx, cm)
	}
	cm.Data = data
	return c.client.Update(ctx, cm)
}

func (c *Controller) loadState(ctx context.Context) error {
	cm := &corev1.ConfigMap{}
	err := c.client.Get(ctx, client.ObjectKey{Name: hibernationStateConfigMap, Namespace: hibernationStateNamespace}, cm)
	if err != nil {
		return nil // No saved state, start fresh
	}
	if cm.Data == nil {
		return nil
	}
	if v, ok := cm.Data["hibernated"]; ok && v == "true" {
		c.hibernated = true
	}
	for key, val := range cm.Data {
		if strings.HasPrefix(key, "desired-") {
			id := strings.TrimPrefix(key, "desired-")
			var count int
			if n, _ := fmt.Sscanf(val, "%d", &count); n != 1 || count <= 0 {
				continue // Skip corrupt or zero values to prevent restoring groups to 0
			}
			c.savedDesired[id] = count
		}
		if strings.HasPrefix(key, "min-") {
			id := strings.TrimPrefix(key, "min-")
			var count int
			if n, _ := fmt.Sscanf(val, "%d", &count); n != 1 || count < 0 {
				continue // Skip corrupt values
			}
			c.savedMin[id] = count
		}
	}
	return nil
}

func (c *Controller) run(ctx context.Context) {
	logger := log.FromContext(ctx).WithName("hibernation")
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if c.state.Breaker.IsTripped(c.Name()) {
				logger.V(1).Info("Circuit breaker tripped, skipping execution cycle")
				continue
			}
			snapshot := c.state.Snapshot()
			if _, err := c.Analyze(ctx, snapshot); err != nil {
				logger.Error(err, "Hibernation analysis failed")
				c.state.Breaker.RecordFailure(c.Name())
			} else {
				c.state.Breaker.RecordSuccess(c.Name())
			}
		case <-ctx.Done():
			c.cron.Stop()
			return
		}
	}
}

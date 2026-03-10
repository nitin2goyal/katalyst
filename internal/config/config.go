package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level configuration for KOptimizer.
// Mu protects fields that can be mutated at runtime via the API (Mode,
// controller Enabled flags, AutoApprove flags). Readers of these fields
// should use GetMode() / IsControllerEnabled() or acquire Mu.RLock().
type Config struct {
	Mu sync.RWMutex `yaml:"-"`

	Mode              string        `yaml:"mode"` // "monitor", "recommend", "active"
	CloudProvider     string        `yaml:"cloudProvider"` // "aws", "gcp", "azure"
	Region            string        `yaml:"region"`
	ClusterName       string        `yaml:"clusterName"`
	ReconcileInterval time.Duration `yaml:"reconcileInterval"`

	CostMonitor    CostMonitorConfig    `yaml:"costMonitor"`
	NodeGroupMgr   NodeGroupMgrConfig   `yaml:"nodegroupManager"`
	Rightsizer     RightsizingConfig    `yaml:"rightsizer"`
	WorkloadScaler WorkloadScalerConfig `yaml:"workloadScaler"`
	PodPurger      PodPurgerConfig      `yaml:"podPurger"`
	GPU            GPUConfig            `yaml:"gpu"`
	Spot           SpotConfig           `yaml:"spot"`
	Hibernation    HibernationConfig    `yaml:"hibernation"`
	StorageMonitor StorageMonitorConfig `yaml:"storageMonitor"`
	NetworkMonitor NetworkMonitorConfig `yaml:"networkMonitor"`
	Alerts         AlertsConfig         `yaml:"alerts"`
	Commitments    CommitmentsConfig    `yaml:"commitments"`
	AIGate         AIGateConfig         `yaml:"aiGate"`
	APIServer      APIServerConfig      `yaml:"apiServer"`
	Database       DatabaseConfig       `yaml:"database"`
}

type CostMonitorConfig struct {
	Enabled        bool          `yaml:"enabled"`
	UpdateInterval time.Duration `yaml:"updateInterval"`
}

type NodeGroupMgrConfig struct {
	Enabled       bool `yaml:"enabled"`
	MinAdjustment struct {
		Enabled           bool          `yaml:"enabled"`
		MinUtilizationPct float64       `yaml:"minUtilizationPct"`
		ObservationPeriod time.Duration `yaml:"observationPeriod"`
	} `yaml:"minAdjustment"`
	EmptyGroupDetection struct {
		Enabled     bool          `yaml:"enabled"`
		EmptyPeriod time.Duration `yaml:"emptyPeriod"`
	} `yaml:"emptyGroupDetection"`
}

type RightsizingConfig struct {
	Enabled             bool          `yaml:"enabled"`
	AutoApprove         bool          `yaml:"autoApprove"` // Auto-approve downsize recommendations (once per workload)
	LookbackWindow      time.Duration `yaml:"lookbackWindow"`
	CPUTargetUtilPct    float64       `yaml:"cpuTargetUtilPct"`
	MemoryTargetUtilPct float64       `yaml:"memoryTargetUtilPct"`
	MinCPURequest       string        `yaml:"minCPURequest"`
	MinMemoryRequest    string        `yaml:"minMemoryRequest"`
	MinKeepRatio        float64       `yaml:"minKeepRatio"` // Min fraction to keep per cycle (default 0.7 = max 30% reduction)
	OOMBumpMultiplier   float64       `yaml:"oomBumpMultiplier"` // e.g., 2.5
	ExcludeNamespaces   []string      `yaml:"excludeNamespaces"`
}

type WorkloadScalerConfig struct {
	Enabled            bool     `yaml:"enabled"`
	VerticalEnabled    bool     `yaml:"verticalEnabled"`
	HorizontalEnabled  bool     `yaml:"horizontalEnabled"`
	SurgeDetection     bool     `yaml:"surgeDetection"`
	SurgeThreshold     float64  `yaml:"surgeThreshold"`
	MaxReplicasLimit   int      `yaml:"maxReplicasLimit"` // Safety cap for HPA max replicas (0 = no limit)
	ConfidenceStartPct float64  `yaml:"confidenceStartPct"`
	ConfidenceFullDays int      `yaml:"confidenceFullDays"`
	ExcludeNamespaces  []string `yaml:"excludeNamespaces"`
}

type PodPurgerConfig struct {
	Enabled      bool          `yaml:"enabled"`
	PollInterval time.Duration `yaml:"pollInterval"`
	MinPodAge    time.Duration `yaml:"minPodAge"`
}

type GPUConfig struct {
	Enabled                      bool          `yaml:"enabled"`
	IdleThresholdPct             float64       `yaml:"idleThresholdPct"`
	IdleDuration                 time.Duration `yaml:"idleDuration"`
	CPUFallbackEnabled           bool          `yaml:"cpuFallbackEnabled"`
	CPUScavengingEnabled         bool          `yaml:"cpuScavengingEnabled"`
	ScavengingCPUThresholdMillis int64         `yaml:"scavengingCPUThresholdMillis"`
	ReclaimEnabled               bool          `yaml:"reclaimEnabled"`
	ReclaimGracePeriod           time.Duration `yaml:"reclaimGracePeriod"`
}

type SpotConfig struct {
	Enabled                 bool    `yaml:"enabled"`
	MaxSpotPercentage       int     `yaml:"maxSpotPercentage"`       // Max % of nodes that can be spot (default 70)
	MaxSpotPct              float64 `yaml:"maxSpotPct"`              // Alias for maxSpotPercentage as float (validated <= 90)
	FallbackToOnDemand      bool    `yaml:"fallbackToOnDemand"`      // Auto-fallback when spot unavailable
	DiversityMinTypes       int     `yaml:"diversityMinTypes"`       // Min instance types for spot diversity (default 3)
	InterruptionHandling    bool    `yaml:"interruptionHandling"`    // Enable preemptive drain on interruption
	DrainGracePeriodSeconds int     `yaml:"drainGracePeriodSeconds"` // Grace period for spot interruption evictions (default 30)
	MaxCostOverODPercent    float64 `yaml:"maxCostOverODPercent"`    // Max spot price as % of on-demand before switching
}

type HibernationConfig struct {
	Enabled        bool     `yaml:"enabled"`
	Schedules      []string `yaml:"schedules"`      // Cron expressions for hibernate ["0 20 * * MON-FRI"]
	WakeSchedules  []string `yaml:"wakeSchedules"`   // Cron expressions for wake ["0 7 * * MON-FRI"]
	ExcludeGroups  []string `yaml:"excludeGroups"`   // Node groups to never hibernate
	PreserveMinOne bool     `yaml:"preserveMinOne"`  // Keep at least 1 node for system pods
}

type StorageMonitorConfig struct {
	Enabled                bool    `yaml:"enabled"`
	OverprovisionThreshold float64 `yaml:"overprovisionThreshold"` // Flag PVs using less than this % (default 30)
	UnusedRetentionDays    int     `yaml:"unusedRetentionDays"`    // Days before flagging unused PVs (default 7)
	StorageCostPerGBUSD    float64 `yaml:"storageCostPerGBUSD"`    // Monthly cost per GB (default 0.10)
}

type NetworkMonitorConfig struct {
	Enabled                 bool    `yaml:"enabled"`
	CrossAZCostPerGBUSD     float64 `yaml:"crossAZCostPerGBUSD"`     // Cost per GB cross-AZ (default 0.01)
	TrafficPerNodeGBPerHour float64 `yaml:"trafficPerNodeGBPerHour"` // Estimated cross-AZ traffic per node per hour (default 5.0)
	TrafficPerPodGBPerHour  float64 `yaml:"trafficPerPodGBPerHour"`  // Estimated cross-AZ traffic per pod per hour (default 0.1)
	EnablePodAnnotations    bool    `yaml:"enablePodAnnotations"`    // Annotate pods with network cost
}

// NotificationChannel represents a dynamically-added notification channel.
type NotificationChannel struct {
	Type    string `yaml:"type" json:"type"`       // "slack", "teams"
	Name    string `yaml:"name" json:"name"`
	URL     string `yaml:"url" json:"url"`
	Enabled bool   `yaml:"enabled" json:"enabled"`
}

type AlertsConfig struct {
	Enabled            bool                  `yaml:"enabled"`
	SlackWebhookURL    string                `yaml:"slackWebhookURL"`
	EmailRecipients    []string              `yaml:"emailRecipients"`
	Webhooks           []string              `yaml:"webhooks"`
	Channels           []NotificationChannel `yaml:"channels"`
	CostAnomalyStdDev float64               `yaml:"costAnomalyStdDev"` // Std deviations for anomaly (default 2.0)
	CooldownMinutes    int                   `yaml:"cooldownMinutes"`   // Min time between repeat alerts (default 60)
}

type CommitmentsConfig struct {
	Enabled           bool          `yaml:"enabled"`
	UpdateInterval    time.Duration `yaml:"updateInterval"`
	ExpiryWarningDays []int         `yaml:"expiryWarningDays"` // e.g., [30, 60, 90]
}

type AIGateConfig struct {
	Enabled           bool          `yaml:"enabled"`
	Model             string        `yaml:"model"`
	Timeout           time.Duration `yaml:"timeout"`
	CostThresholdUSD  float64       `yaml:"costThresholdUSD"`
	ScaleThresholdPct float64       `yaml:"scaleThresholdPct"`
	MaxEvictNodes     int           `yaml:"maxEvictNodes"`
	Timezone          string        `yaml:"timezone"` // IANA timezone for business hours check (e.g., "America/New_York"). Defaults to UTC.
}

type APIServerConfig struct {
	Enabled bool   `yaml:"enabled"`
	Address string `yaml:"address"`
	Port    int    `yaml:"port"`
}

type DatabaseConfig struct {
	Path          string `yaml:"path"`
	RetentionDays int    `yaml:"retentionDays"`
}

// DefaultConfig returns a Config with sensible defaults.
// Cloud provider and region can be set via CLOUD_PROVIDER and REGION env vars.
func DefaultConfig() *Config {
	cfg := &Config{
		Mode:          "recommend",
		CloudProvider: "gcp",
		Region:        "asia-south1",
		ClusterName:   "apps-gke",
		ReconcileInterval: 60 * time.Second,
		CostMonitor: CostMonitorConfig{
			Enabled:        true,
			UpdateInterval: 5 * time.Minute,
		},
		NodeGroupMgr: NodeGroupMgrConfig{
			Enabled: true,
		},
		Rightsizer: RightsizingConfig{
			Enabled:             false,
			LookbackWindow:      7 * 24 * time.Hour,
			CPUTargetUtilPct:    95.0,
			MemoryTargetUtilPct: 95.0,
			MinCPURequest:       "10m",
			MinMemoryRequest:    "32Mi",
			MinKeepRatio:        0.7,
			OOMBumpMultiplier:   2.5,
			ExcludeNamespaces:   []string{"kube-system"},
		},
		WorkloadScaler: WorkloadScalerConfig{
			Enabled:            false,
			VerticalEnabled:    true,
			HorizontalEnabled:  true,
			SurgeDetection:     true,
			SurgeThreshold:     2.0,
			MaxReplicasLimit:   500, // safety cap to prevent runaway scaling
			ConfidenceStartPct: 50.0,
			ConfidenceFullDays: 7,
			ExcludeNamespaces:  []string{"kube-system", "monitoring"},
		},
		PodPurger: PodPurgerConfig{
			Enabled:      true,
			PollInterval: 5 * time.Minute,
			MinPodAge:    30 * time.Minute,
		},
		GPU: GPUConfig{
			Enabled:                      true,
			IdleThresholdPct:             5.0,
			IdleDuration:                 30 * time.Minute,
			CPUFallbackEnabled:           true,
			CPUScavengingEnabled:         true,
			ScavengingCPUThresholdMillis: 2000,
			ReclaimEnabled:               true,
			ReclaimGracePeriod:           5 * time.Minute,
		},
		Spot: SpotConfig{
			Enabled:                 true,
			MaxSpotPercentage:       70,
			MaxSpotPct:              70.0,
			FallbackToOnDemand:      true,
			DiversityMinTypes:       3,
			MaxCostOverODPercent:    90,
		},
		Hibernation: HibernationConfig{
			Enabled:        false,
			PreserveMinOne: true,
		},
		StorageMonitor: StorageMonitorConfig{
			Enabled:                true,
			OverprovisionThreshold: 30.0,
			UnusedRetentionDays:    7,
			StorageCostPerGBUSD:    0.10,
		},
		NetworkMonitor: NetworkMonitorConfig{
			Enabled:                 true,
			CrossAZCostPerGBUSD:     0.01,
			TrafficPerNodeGBPerHour: 5.0,
			TrafficPerPodGBPerHour:  0.1,
		},
		Alerts: AlertsConfig{
			Enabled:            false,
			CostAnomalyStdDev: 2.0,
			CooldownMinutes:    60,
		},
		Commitments: CommitmentsConfig{
			Enabled:           true,
			UpdateInterval:    1 * time.Hour,
			ExpiryWarningDays: []int{30, 60, 90},
		},
		AIGate: AIGateConfig{
			Enabled:           false,
			Model:             "claude-sonnet-4-6",
			Timeout:           10 * time.Second,
			CostThresholdUSD:  500.0,
			ScaleThresholdPct: 30.0,
			MaxEvictNodes:     3,
		},
		APIServer: APIServerConfig{
			Enabled: true,
			Address: "0.0.0.0",
			Port:    8080,
		},
		Database: DatabaseConfig{
			Path:          "/tmp/koptimizer.db",
			RetentionDays: 90,
		},
	}

	// NodeGroupMgr defaults
	cfg.NodeGroupMgr.MinAdjustment.Enabled = true
	cfg.NodeGroupMgr.MinAdjustment.MinUtilizationPct = 30.0
	cfg.NodeGroupMgr.MinAdjustment.ObservationPeriod = 48 * time.Hour
	cfg.NodeGroupMgr.EmptyGroupDetection.Enabled = true
	cfg.NodeGroupMgr.EmptyGroupDetection.EmptyPeriod = 14 * 24 * time.Hour

	cfg.applyEnvOverrides()
	return cfg
}

// LoadFromFile loads config from a YAML file, overlaying on defaults.
// If the file does not exist, returns defaults (this is expected on first run).
// If the file exists but cannot be parsed, returns an error.
func LoadFromFile(path string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			slog.Info("config file not found, using defaults", "path", path)
			cfg.applyEnvOverrides()
			return cfg, nil
		}
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		slog.Warn("config file exists but failed to parse", "path", path, "error", err)
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	cfg.applyEnvOverrides()
	return cfg, nil
}

// applyEnvOverrides fills in empty fields from environment variables.
// This handles cases where the config file has empty values but cloud-specific
// env vars are set (e.g., by the Helm chart or the cloud platform).
func (c *Config) applyEnvOverrides() {
	if c.CloudProvider == "" {
		if v := os.Getenv("CLOUD_PROVIDER"); v != "" {
			c.CloudProvider = v
		} else if os.Getenv("GOOGLE_CLOUD_PROJECT") != "" {
			c.CloudProvider = "gcp"
		} else if os.Getenv("AWS_REGION") != "" || os.Getenv("AWS_DEFAULT_REGION") != "" {
			c.CloudProvider = "aws"
		} else if os.Getenv("AZURE_SUBSCRIPTION_ID") != "" {
			c.CloudProvider = "azure"
		}
	}
	if c.Region == "" {
		if v := os.Getenv("REGION"); v != "" {
			c.Region = v
		} else if v := os.Getenv("AWS_REGION"); v != "" {
			c.Region = v
		} else if v := os.Getenv("AWS_DEFAULT_REGION"); v != "" {
			c.Region = v
		}
	}
	if c.ClusterName == "" {
		if v := os.Getenv("CLUSTER_NAME"); v != "" {
			c.ClusterName = v
		} else if v := os.Getenv("KOPTIMIZER_CLUSTER_NAME"); v != "" {
			c.ClusterName = v
		}
	}
	// Slack webhook URL from Secret (overrides config file value)
	if v := os.Getenv("KOPTIMIZER_SLACK_WEBHOOK_URL"); v != "" {
		c.Alerts.SlackWebhookURL = v
	}
}

// Validate checks the config for errors.
func (c *Config) Validate() error {
	switch c.Mode {
	case "monitor", "recommend", "active":
	default:
		return fmt.Errorf("invalid mode %q: must be monitor, recommend, or active", c.Mode)
	}

	if c.CloudProvider == "" {
		return fmt.Errorf("cloudProvider is required: set in config file or CLOUD_PROVIDER env var (aws, gcp, azure)")
	}
	switch c.CloudProvider {
	case "aws", "gcp", "azure":
	default:
		return fmt.Errorf("invalid cloud provider %q: must be aws, gcp, or azure", c.CloudProvider)
	}

	if c.Region == "" {
		return fmt.Errorf("region is required: set in config file or REGION env var")
	}

	if c.Rightsizer.OOMBumpMultiplier < 1.0 {
		return fmt.Errorf("oomBumpMultiplier must be >= 1.0, got %.1f", c.Rightsizer.OOMBumpMultiplier)
	}
	if c.Rightsizer.OOMBumpMultiplier > 10.0 {
		return fmt.Errorf("oomBumpMultiplier must be <= 10.0, got %.1f", c.Rightsizer.OOMBumpMultiplier)
	}

	if c.WorkloadScaler.SurgeThreshold < 1.0 {
		return fmt.Errorf("surgeThreshold must be >= 1.0, got %.1f", c.WorkloadScaler.SurgeThreshold)
	}

	return nil
}

// ValidateDetailed performs extended validation beyond basic Validate().
// This checks cross-field constraints that are important for safety.
func (c *Config) ValidateDetailed() error {
	if err := c.Validate(); err != nil {
		return err
	}

	// Active mode without AI Gate — warn but don't block startup
	if c.Mode == "active" && !c.AIGate.Enabled {
		slog.Warn("AI Gate is disabled in active mode — automated changes will not have AI safety review")
	}

	// Validate network monitor traffic estimate
	if c.NetworkMonitor.Enabled && c.NetworkMonitor.TrafficPerPodGBPerHour < 0 {
		return fmt.Errorf("trafficPerPodGBPerHour must be >= 0, got %.2f", c.NetworkMonitor.TrafficPerPodGBPerHour)
	}

	// Validate spot percentage bounds to prevent all nodes becoming spot
	if c.Spot.Enabled {
		if c.Spot.MaxSpotPercentage > 90 {
			return fmt.Errorf("maxSpotPercentage must be <= 90, got %d (100%% spot is dangerous for cluster stability)", c.Spot.MaxSpotPercentage)
		}
		if c.Spot.MaxSpotPct > 90 {
			return fmt.Errorf("maxSpotPct must be <= 90, got %.0f", c.Spot.MaxSpotPct)
		}
		// Reconcile dual fields: use whichever is set, prefer MaxSpotPercentage
		if c.Spot.MaxSpotPct == 0 && c.Spot.MaxSpotPercentage > 0 {
			c.Spot.MaxSpotPct = float64(c.Spot.MaxSpotPercentage)
		} else if c.Spot.MaxSpotPercentage == 0 && c.Spot.MaxSpotPct > 0 {
			c.Spot.MaxSpotPercentage = int(c.Spot.MaxSpotPct)
		}
	}

	return nil
}

// GetMode returns the current operating mode, safe for concurrent reads.
func (c *Config) GetMode() string {
	c.Mu.RLock()
	defer c.Mu.RUnlock()
	return c.Mode
}

// SetMode sets the operating mode, safe for concurrent writes.
func (c *Config) SetMode(mode string) {
	c.Mu.Lock()
	c.Mode = mode
	c.Mu.Unlock()
}

// SetControllerEnabled sets a controller's enabled state, safe for concurrent writes.
func (c *Config) SetControllerEnabled(name string, enabled bool) bool {
	c.Mu.Lock()
	defer c.Mu.Unlock()
	switch name {
	case "costMonitor":
		c.CostMonitor.Enabled = enabled
	case "nodegroupMgr":
		c.NodeGroupMgr.Enabled = enabled
	case "rightsizer":
		c.Rightsizer.Enabled = enabled
	case "workloadScaler":
		c.WorkloadScaler.Enabled = enabled
	case "gpu":
		c.GPU.Enabled = enabled
	case "gpuReclaim":
		c.GPU.ReclaimEnabled = enabled
	case "commitments":
		c.Commitments.Enabled = enabled
	case "aiGate":
		c.AIGate.Enabled = enabled
	case "podPurger":
		c.PodPurger.Enabled = enabled
	default:
		return false
	}
	return true
}

// IsControllerEnabled returns whether a named controller is enabled, safe for concurrent reads.
func (c *Config) IsControllerEnabled(name string) bool {
	c.Mu.RLock()
	defer c.Mu.RUnlock()
	switch name {
	case "costMonitor":
		return c.CostMonitor.Enabled
	case "nodegroupMgr":
		return c.NodeGroupMgr.Enabled
	case "rightsizer":
		return c.Rightsizer.Enabled
	case "workloadScaler":
		return c.WorkloadScaler.Enabled
	case "gpu":
		return c.GPU.Enabled
	case "gpuReclaim":
		return c.GPU.ReclaimEnabled
	case "commitments":
		return c.Commitments.Enabled
	case "aiGate":
		return c.AIGate.Enabled
	case "podPurger":
		return c.PodPurger.Enabled
	default:
		return false
	}
}

// SetAutoApprove sets a controller's auto-approve state, safe for concurrent writes.
func (c *Config) SetAutoApprove(name string, autoApprove bool) bool {
	c.Mu.Lock()
	defer c.Mu.Unlock()
	switch name {
	case "rightsizer":
		c.Rightsizer.AutoApprove = autoApprove
	default:
		return false
	}
	return true
}

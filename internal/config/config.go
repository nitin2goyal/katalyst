package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level configuration for KOptimizer.
type Config struct {
	Mode              string        `yaml:"mode"` // "monitor", "recommend", "active"
	CloudProvider     string        `yaml:"cloudProvider"` // "aws", "gcp", "azure"
	Region            string        `yaml:"region"`
	ClusterName       string        `yaml:"clusterName"`
	ReconcileInterval time.Duration `yaml:"reconcileInterval"`

	CostMonitor    CostMonitorConfig    `yaml:"costMonitor"`
	NodeAutoscaler NodeAutoscalerConfig `yaml:"nodeAutoscaler"`
	NodeGroupMgr   NodeGroupMgrConfig   `yaml:"nodegroupManager"`
	Rightsizer     RightsizingConfig    `yaml:"rightsizer"`
	WorkloadScaler WorkloadScalerConfig `yaml:"workloadScaler"`
	Evictor        EvictorConfig        `yaml:"evictor"`
	Rebalancer     RebalancerConfig     `yaml:"rebalancer"`
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

type NodeAutoscalerConfig struct {
	Enabled            bool          `yaml:"enabled"`
	NodeTemplatesEnabled bool        `yaml:"nodeTemplatesEnabled"`
	DryRun             bool          `yaml:"dryRun"`
	ScanInterval       time.Duration `yaml:"scanInterval"`
	ScaleUpThreshold   float64       `yaml:"scaleUpThreshold"`   // CPU util % to trigger scale up
	ScaleDownThreshold float64       `yaml:"scaleDownThreshold"` // CPU util % to trigger scale down
	ScaleDownDelay     time.Duration `yaml:"scaleDownDelay"`     // Wait before scaling down
	MaxScaleUpNodes    int           `yaml:"maxScaleUpNodes"`
	MaxScaleDownNodes  int           `yaml:"maxScaleDownNodes"`
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
	LookbackWindow      time.Duration `yaml:"lookbackWindow"`
	CPUTargetUtilPct    float64       `yaml:"cpuTargetUtilPct"`
	MemoryTargetUtilPct float64       `yaml:"memoryTargetUtilPct"`
	MinCPURequest       string        `yaml:"minCPURequest"`
	MinMemoryRequest    string        `yaml:"minMemoryRequest"`
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

type EvictorConfig struct {
	Enabled                bool          `yaml:"enabled"`
	UtilizationThreshold   float64       `yaml:"utilizationThreshold"` // Below this, node is underutilized
	ConsolidationInterval  time.Duration `yaml:"consolidationInterval"`
	MaxConcurrentEvictions int           `yaml:"maxConcurrentEvictions"`
	DrainTimeout           time.Duration `yaml:"drainTimeout"`
	PartialDrainTTL        time.Duration `yaml:"partialDrainTTL"` // Auto-uncordon partially drained nodes after this duration (default 30m)
	DryRun                 bool          `yaml:"dryRun"`
}

type RebalancerConfig struct {
	Enabled                bool          `yaml:"enabled"`
	DryRun                 bool          `yaml:"dryRun"`
	Schedule               string        `yaml:"schedule"` // Cron expression
	ImbalanceThresholdPct  float64       `yaml:"imbalanceThresholdPct"` // Min imbalance % to trigger rebalance (default 40)
	RescheduleTimeout      time.Duration `yaml:"rescheduleTimeout"`    // How long to wait for pods to reschedule after eviction (default 60s)
	BusyRedistribution struct {
		Enabled                bool    `yaml:"enabled"`
		OverloadedThresholdPct float64 `yaml:"overloadedThresholdPct"`
		TargetUtilizationPct   float64 `yaml:"targetUtilizationPct"`
	} `yaml:"busyRedistribution"`
}

type GPUConfig struct {
	Enabled                      bool          `yaml:"enabled"`
	IdleThresholdPct             float64       `yaml:"idleThresholdPct"`
	IdleDuration                 time.Duration `yaml:"idleDuration"`
	CPUFallbackEnabled           bool          `yaml:"cpuFallbackEnabled"`
	CPUScavengingEnabled         bool          `yaml:"cpuScavengingEnabled"`
	ScavengingCPUThresholdMillis int64         `yaml:"scavengingCPUThresholdMillis"`
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

type AlertsConfig struct {
	Enabled            bool     `yaml:"enabled"`
	SlackWebhookURL    string   `yaml:"slackWebhookURL"`
	EmailRecipients    []string `yaml:"emailRecipients"`
	Webhooks           []string `yaml:"webhooks"`
	CostAnomalyStdDev float64  `yaml:"costAnomalyStdDev"` // Std deviations for anomaly (default 2.0)
	CooldownMinutes    int      `yaml:"cooldownMinutes"`   // Min time between repeat alerts (default 60)
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
		NodeAutoscaler: NodeAutoscalerConfig{
			Enabled:              true,
			DryRun:               true, // safe default
			NodeTemplatesEnabled: true,
			ScanInterval:       30 * time.Second,
			ScaleUpThreshold:   80.0,
			ScaleDownThreshold: 30.0,
			ScaleDownDelay:     10 * time.Minute,
			MaxScaleUpNodes:    5,
			MaxScaleDownNodes:  3,
		},
		NodeGroupMgr: NodeGroupMgrConfig{
			Enabled: true,
		},
		Rightsizer: RightsizingConfig{
			Enabled:             true,
			LookbackWindow:      7 * 24 * time.Hour,
			CPUTargetUtilPct:    70.0,
			MemoryTargetUtilPct: 75.0,
			MinCPURequest:       "10m",
			MinMemoryRequest:    "32Mi",
			OOMBumpMultiplier:   2.5,
			ExcludeNamespaces:   []string{"kube-system"},
		},
		WorkloadScaler: WorkloadScalerConfig{
			Enabled:            true,
			VerticalEnabled:    true,
			HorizontalEnabled:  true,
			SurgeDetection:     true,
			SurgeThreshold:     2.0,
			MaxReplicasLimit:   500, // safety cap to prevent runaway scaling
			ConfidenceStartPct: 50.0,
			ConfidenceFullDays: 7,
			ExcludeNamespaces:  []string{"kube-system", "monitoring"},
		},
		Evictor: EvictorConfig{
			Enabled:                true,
			DryRun:                 true, // safe default: recommend-only until explicitly enabled
			UtilizationThreshold:   40.0,
			ConsolidationInterval:  5 * time.Minute,
			MaxConcurrentEvictions: 5,
			DrainTimeout:           5 * time.Minute,
			PartialDrainTTL:        30 * time.Minute,
		},
		Rebalancer: RebalancerConfig{
			Enabled:               true,
			DryRun:                true, // safe default
			Schedule:              "0 3 * * SUN",
			ImbalanceThresholdPct: 40.0,
		},
		GPU: GPUConfig{
			Enabled:                      true,
			IdleThresholdPct:             5.0,
			IdleDuration:                 30 * time.Minute,
			CPUFallbackEnabled:           true,
			CPUScavengingEnabled:         true,
			ScavengingCPUThresholdMillis: 2000,
		},
		Spot: SpotConfig{
			Enabled:                 true,
			MaxSpotPercentage:       70,
			MaxSpotPct:              70.0,
			FallbackToOnDemand:      true,
			DiversityMinTypes:       3,
			InterruptionHandling:    true,
			DrainGracePeriodSeconds: 30,
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
			Path:          "/data/koptimizer.db",
			RetentionDays: 90,
		},
	}

	// NodeGroupMgr defaults
	cfg.NodeGroupMgr.MinAdjustment.Enabled = true
	cfg.NodeGroupMgr.MinAdjustment.MinUtilizationPct = 30.0
	cfg.NodeGroupMgr.MinAdjustment.ObservationPeriod = 48 * time.Hour
	cfg.NodeGroupMgr.EmptyGroupDetection.Enabled = true
	cfg.NodeGroupMgr.EmptyGroupDetection.EmptyPeriod = 14 * 24 * time.Hour

	// Rebalancer BusyRedistribution defaults
	cfg.Rebalancer.BusyRedistribution.Enabled = true
	cfg.Rebalancer.BusyRedistribution.OverloadedThresholdPct = 90.0
	cfg.Rebalancer.BusyRedistribution.TargetUtilizationPct = 70.0

	cfg.applyEnvOverrides()
	return cfg
}

// LoadFromFile loads config from a YAML file, overlaying on defaults.
func LoadFromFile(path string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
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

	if c.NodeAutoscaler.ScaleUpThreshold <= c.NodeAutoscaler.ScaleDownThreshold {
		return fmt.Errorf("scaleUpThreshold (%.1f) must be greater than scaleDownThreshold (%.1f)",
			c.NodeAutoscaler.ScaleUpThreshold, c.NodeAutoscaler.ScaleDownThreshold)
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

	// Active mode requires AI Gate to be enabled for safety
	if c.Mode == "active" && !c.AIGate.Enabled {
		return fmt.Errorf("AI Gate must be enabled when mode is \"active\" to prevent unsafe automated changes")
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

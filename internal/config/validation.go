package config

import (
	"fmt"
	"strings"
)

// ValidationError collects multiple validation errors.
type ValidationError struct {
	Errors []string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("config validation failed: %s", strings.Join(e.Errors, "; "))
}

func (e *ValidationError) Add(msg string) {
	e.Errors = append(e.Errors, msg)
}

func (e *ValidationError) HasErrors() bool {
	return len(e.Errors) > 0
}

// ValidateDetailed performs comprehensive config validation.
func ValidateDetailed(cfg *Config) *ValidationError {
	ve := &ValidationError{}

	// Mode validation
	switch cfg.Mode {
	case "monitor", "recommend", "active":
	default:
		ve.Add(fmt.Sprintf("invalid mode %q", cfg.Mode))
	}

	// Cloud provider
	switch cfg.CloudProvider {
	case "aws", "gcp", "azure", "":
	default:
		ve.Add(fmt.Sprintf("invalid cloud provider %q", cfg.CloudProvider))
	}

	// Rightsizer
	if cfg.Rightsizer.Enabled {
		if cfg.Rightsizer.OOMBumpMultiplier < 1.0 {
			ve.Add("rightsizer.oomBumpMultiplier must be >= 1.0")
		}
		if cfg.Rightsizer.CPUTargetUtilPct <= 0 || cfg.Rightsizer.CPUTargetUtilPct > 100 {
			ve.Add("rightsizer.cpuTargetUtilPct must be between 0 and 100")
		}
	}

	// Workload scaler
	if cfg.WorkloadScaler.Enabled {
		if cfg.WorkloadScaler.SurgeThreshold < 1.0 {
			ve.Add("workloadScaler.surgeThreshold must be >= 1.0")
		}
		if cfg.WorkloadScaler.MaxReplicasLimit < 0 {
			ve.Add("workloadScaler.maxReplicasLimit must be >= 0")
		}
	}

	// Spot safety
	if cfg.Spot.Enabled {
		if cfg.Spot.MaxSpotPct > 90 {
			ve.Add("spot.maxSpotPct should not exceed 90% to avoid mass interruption risk")
		}
	}

	// AI Gate
	if cfg.AIGate.Enabled {
		if cfg.AIGate.CostThresholdUSD < 0 {
			ve.Add("aiGate.costThresholdUSD must be >= 0")
		}
		if cfg.AIGate.ScaleThresholdPct < 0 || cfg.AIGate.ScaleThresholdPct > 100 {
			ve.Add("aiGate.scaleThresholdPct must be between 0 and 100")
		}
	}

	// API Server
	if cfg.APIServer.Enabled {
		if cfg.APIServer.Port < 1 || cfg.APIServer.Port > 65535 {
			ve.Add("apiServer.port must be between 1 and 65535")
		}
	}

	if ve.HasErrors() {
		return ve
	}
	return nil
}

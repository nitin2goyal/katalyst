package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultConfig_ReturnsExpectedDefaults(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Mode != "recommend" {
		t.Errorf("Mode = %q, want %q", cfg.Mode, "recommend")
	}
	if cfg.ReconcileInterval != 60*time.Second {
		t.Errorf("ReconcileInterval = %v, want %v", cfg.ReconcileInterval, 60*time.Second)
	}
	if cfg.NodeAutoscaler.ScaleUpThreshold != 80.0 {
		t.Errorf("ScaleUpThreshold = %v, want %v", cfg.NodeAutoscaler.ScaleUpThreshold, 80.0)
	}
	if cfg.NodeAutoscaler.ScaleDownThreshold != 30.0 {
		t.Errorf("ScaleDownThreshold = %v, want %v", cfg.NodeAutoscaler.ScaleDownThreshold, 30.0)
	}
	if cfg.Rightsizer.OOMBumpMultiplier != 2.5 {
		t.Errorf("OOMBumpMultiplier = %v, want %v", cfg.Rightsizer.OOMBumpMultiplier, 2.5)
	}
	if cfg.WorkloadScaler.SurgeThreshold != 2.0 {
		t.Errorf("SurgeThreshold = %v, want %v", cfg.WorkloadScaler.SurgeThreshold, 2.0)
	}
	if cfg.CostMonitor.Enabled != true {
		t.Error("CostMonitor.Enabled = false, want true")
	}
	if cfg.NodeAutoscaler.Enabled != true {
		t.Error("NodeAutoscaler.Enabled = false, want true")
	}
	if cfg.Rightsizer.Enabled != true {
		t.Error("Rightsizer.Enabled = false, want true")
	}
	if cfg.APIServer.Port != 8080 {
		t.Errorf("APIServer.Port = %d, want %d", cfg.APIServer.Port, 8080)
	}
	if cfg.Database.RetentionDays != 90 {
		t.Errorf("Database.RetentionDays = %d, want %d", cfg.Database.RetentionDays, 90)
	}
}

func TestDefaultConfig_Validate_ReturnsNil(t *testing.T) {
	cfg := DefaultConfig()
	// DefaultConfig does not set CloudProvider or Region, so set them for validation.
	cfg.CloudProvider = "aws"
	cfg.Region = "us-east-1"

	if err := cfg.Validate(); err != nil {
		t.Fatalf("DefaultConfig().Validate() returned error: %v", err)
	}
}

func TestLoadFromFile_ValidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	yamlContent := []byte(`mode: active
cloudProvider: gcp
region: us-central1-a
clusterName: test-cluster
`)
	if err := os.WriteFile(path, yamlContent, 0644); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

	cfg, err := LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile(%q) returned error: %v", path, err)
	}

	if cfg.Mode != "active" {
		t.Errorf("Mode = %q, want %q", cfg.Mode, "active")
	}
	if cfg.CloudProvider != "gcp" {
		t.Errorf("CloudProvider = %q, want %q", cfg.CloudProvider, "gcp")
	}
	if cfg.Region != "us-central1-a" {
		t.Errorf("Region = %q, want %q", cfg.Region, "us-central1-a")
	}
	if cfg.ClusterName != "test-cluster" {
		t.Errorf("ClusterName = %q, want %q", cfg.ClusterName, "test-cluster")
	}
}

func TestLoadFromFile_MergesWithDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "partial.yaml")

	// Only set a few fields; the rest should come from defaults.
	yamlContent := []byte(`mode: monitor
cloudProvider: azure
region: eastus
`)
	if err := os.WriteFile(path, yamlContent, 0644); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

	cfg, err := LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile(%q) returned error: %v", path, err)
	}

	// Explicitly set fields
	if cfg.Mode != "monitor" {
		t.Errorf("Mode = %q, want %q", cfg.Mode, "monitor")
	}
	if cfg.CloudProvider != "azure" {
		t.Errorf("CloudProvider = %q, want %q", cfg.CloudProvider, "azure")
	}

	// Default fields should still be present
	if cfg.NodeAutoscaler.ScaleUpThreshold != 80.0 {
		t.Errorf("ScaleUpThreshold = %v, want default %v", cfg.NodeAutoscaler.ScaleUpThreshold, 80.0)
	}
	if cfg.NodeAutoscaler.ScaleDownThreshold != 30.0 {
		t.Errorf("ScaleDownThreshold = %v, want default %v", cfg.NodeAutoscaler.ScaleDownThreshold, 30.0)
	}
	if cfg.Rightsizer.OOMBumpMultiplier != 2.5 {
		t.Errorf("OOMBumpMultiplier = %v, want default %v", cfg.Rightsizer.OOMBumpMultiplier, 2.5)
	}
	if cfg.WorkloadScaler.SurgeThreshold != 2.0 {
		t.Errorf("SurgeThreshold = %v, want default %v", cfg.WorkloadScaler.SurgeThreshold, 2.0)
	}
	if cfg.ReconcileInterval != 60*time.Second {
		t.Errorf("ReconcileInterval = %v, want default %v", cfg.ReconcileInterval, 60*time.Second)
	}
	if cfg.APIServer.Port != 8080 {
		t.Errorf("APIServer.Port = %d, want default %d", cfg.APIServer.Port, 8080)
	}
}

func TestLoadFromFile_InvalidPath(t *testing.T) {
	_, err := LoadFromFile("/nonexistent/path/config.yaml")
	if err == nil {
		t.Fatal("LoadFromFile with invalid path expected error, got nil")
	}
}

func TestLoadFromFile_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")

	badContent := []byte(`mode: [invalid
  yaml: {{broken
`)
	if err := os.WriteFile(path, badContent, 0644); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

	_, err := LoadFromFile(path)
	if err == nil {
		t.Fatal("LoadFromFile with invalid YAML expected error, got nil")
	}
}

func TestValidate_ValidModes(t *testing.T) {
	validModes := []string{"monitor", "recommend", "active"}

	for _, mode := range validModes {
		t.Run(mode, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Mode = mode
			cfg.CloudProvider = "aws"
			cfg.Region = "us-east-1"

			if err := cfg.Validate(); err != nil {
				t.Errorf("Validate() with mode %q returned error: %v", mode, err)
			}
		})
	}
}

func TestValidate_InvalidMode(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Mode = "turbo"
	cfg.CloudProvider = "aws"
	cfg.Region = "us-east-1"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() with invalid mode expected error, got nil")
	}
}

func TestValidate_MissingCloudProvider(t *testing.T) {
	cfg := DefaultConfig()
	cfg.CloudProvider = ""
	cfg.Region = "us-east-1"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() with missing cloudProvider expected error, got nil")
	}
}

func TestValidate_InvalidCloudProvider(t *testing.T) {
	cfg := DefaultConfig()
	cfg.CloudProvider = "digitalocean"
	cfg.Region = "us-east-1"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() with invalid cloudProvider expected error, got nil")
	}
}

func TestValidate_MissingRegion(t *testing.T) {
	cfg := DefaultConfig()
	cfg.CloudProvider = "aws"
	cfg.Region = ""

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() with missing region expected error, got nil")
	}
}

func TestValidate_ScaleUpThresholdLessThanOrEqualToScaleDown(t *testing.T) {
	tests := []struct {
		name      string
		scaleUp   float64
		scaleDown float64
	}{
		{
			name:      "equal thresholds",
			scaleUp:   50.0,
			scaleDown: 50.0,
		},
		{
			name:      "scaleUp less than scaleDown",
			scaleUp:   20.0,
			scaleDown: 60.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.CloudProvider = "aws"
			cfg.Region = "us-east-1"
			cfg.NodeAutoscaler.ScaleUpThreshold = tt.scaleUp
			cfg.NodeAutoscaler.ScaleDownThreshold = tt.scaleDown

			err := cfg.Validate()
			if err == nil {
				t.Errorf("Validate() with scaleUp=%.1f, scaleDown=%.1f expected error, got nil",
					tt.scaleUp, tt.scaleDown)
			}
		})
	}
}

func TestValidate_OOMBumpMultiplierBelowOne(t *testing.T) {
	cfg := DefaultConfig()
	cfg.CloudProvider = "aws"
	cfg.Region = "us-east-1"
	cfg.Rightsizer.OOMBumpMultiplier = 0.5

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() with OOMBumpMultiplier < 1.0 expected error, got nil")
	}
}

func TestValidate_SurgeThresholdBelowOne(t *testing.T) {
	cfg := DefaultConfig()
	cfg.CloudProvider = "aws"
	cfg.Region = "us-east-1"
	cfg.WorkloadScaler.SurgeThreshold = 0.3

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() with SurgeThreshold < 1.0 expected error, got nil")
	}
}

func TestValidate_AllValidCloudProviders(t *testing.T) {
	providers := []string{"aws", "gcp", "azure"}

	for _, provider := range providers {
		t.Run(provider, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.CloudProvider = provider
			cfg.Region = "some-region"

			if err := cfg.Validate(); err != nil {
				t.Errorf("Validate() with cloudProvider %q returned error: %v", provider, err)
			}
		})
	}
}

func TestValidate_BoundaryOOMBumpMultiplier(t *testing.T) {
	cfg := DefaultConfig()
	cfg.CloudProvider = "aws"
	cfg.Region = "us-east-1"
	cfg.Rightsizer.OOMBumpMultiplier = 1.0

	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() with OOMBumpMultiplier=1.0 should pass, got error: %v", err)
	}
}

func TestValidate_BoundarySurgeThreshold(t *testing.T) {
	cfg := DefaultConfig()
	cfg.CloudProvider = "aws"
	cfg.Region = "us-east-1"
	cfg.WorkloadScaler.SurgeThreshold = 1.0

	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() with SurgeThreshold=1.0 should pass, got error: %v", err)
	}
}

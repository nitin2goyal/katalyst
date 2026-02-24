package azure

import (
	"testing"
)

func TestEstimateAzureEvictionRate(t *testing.T) {
	tests := []struct {
		name     string
		vmSize   string
		expected float64
	}{
		// GPU NC/ND series → 15%
		{"NC6", "Standard_NC6", 15.0},
		{"NC24", "Standard_NC24", 15.0},
		{"NC6s_v3", "Standard_NC6s_v3", 15.0},
		{"NC24ads_A100_v4", "Standard_NC24ads_A100_v4", 15.0},
		{"ND40rs_v2", "Standard_ND40rs_v2", 15.0},
		{"ND96asr_A100_v4", "Standard_ND96asr_A100_v4", 15.0},

		// GPU NV series → 12%
		{"NV6", "Standard_NV6", 12.0},
		{"NV12s_v3", "Standard_NV12s_v3", 12.0},
		{"NV24s_v3", "Standard_NV24s_v3", 12.0},

		// B-series → 12%
		{"B2ms", "Standard_B2ms", 12.0},
		{"B4ms", "Standard_B4ms", 12.0},
		{"B1s", "Standard_B1s", 12.0},

		// D v2/v3 → 10%
		{"D4_v2", "Standard_D4_v2", 10.0},
		{"D4s_v3", "Standard_D4s_v3", 10.0},
		{"D8s_v3", "Standard_D8s_v3", 10.0},
		{"D2_v2", "Standard_D2_v2", 10.0},

		// v5/v6 → 5%
		{"D8s_v5", "Standard_D8s_v5", 5.0},
		{"E8s_v5", "Standard_E8s_v5", 5.0},
		{"D4s_v5", "Standard_D4s_v5", 5.0},
		{"E16s_v6", "Standard_E16s_v6", 5.0},

		// Default → 8%
		{"F8s_v2", "Standard_F8s_v2", 8.0},
		{"L8s_v2", "Standard_L8s_v2", 8.0},
		{"M64ms", "Standard_M64ms", 8.0},
		{"D4s_v4", "Standard_D4s_v4", 8.0},

		// Case insensitivity
		{"lowercase", "standard_nc6", 15.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := estimateAzureEvictionRate(tt.vmSize)
			if got != tt.expected {
				t.Errorf("estimateAzureEvictionRate(%q) = %f, want %f", tt.vmSize, got, tt.expected)
			}
		})
	}
}

func TestIsARM64VM(t *testing.T) {
	tests := []struct {
		name     string
		vmName   string
		expected bool
	}{
		// isARM64VM checks if parts[1] (after splitting by _) starts with
		// a letter followed by 'p'. Real Azure ARM VMs have names like
		// "Standard_Dpds_v5" where parts[1]="Dpds" → [0]='d',[1]='p' → true

		// Names where the second char of parts[1] is 'p'
		{"Dp_prefix", "Standard_Dp4s_v5", true},
		{"Ep_prefix", "Standard_Ep4s_v5", true},

		// Real Azure ARM VM names with number before 'p' (D4ps)
		{"D4ps_v5", "Standard_D4ps_v5", true},
		{"E4ps_v5", "Standard_E4ps_v5", true},

		// Non-ARM64 VMs
		{"Ds_v5", "Standard_D4s_v5", false},
		{"Es_v3", "Standard_E8s_v3", false},
		{"B2ms", "Standard_B2ms", false},
		{"Fs_v2", "Standard_F8s_v2", false},

		// Edge cases
		{"no_underscore", "NoUnderscore", false},
		{"single_part", "Standard", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isARM64VM(tt.vmName)
			if got != tt.expected {
				t.Errorf("isARM64VM(%q) = %v, want %v", tt.vmName, got, tt.expected)
			}
		})
	}
}

func TestDetectGPU(t *testing.T) {
	tests := []struct {
		name        string
		vmName      string
		cores       int
		wantGPUs    int
		wantGPUType string
	}{
		// NC series (K80)
		{"NC6_K80", "Standard_NC6", 6, 1, "NVIDIA Tesla K80"},
		{"NC12_K80", "Standard_NC12", 12, 2, "NVIDIA Tesla K80"},
		{"NC24_K80", "Standard_NC24", 24, 4, "NVIDIA Tesla K80"},

		// NC v3 series (V100)
		{"NC6s_v3", "Standard_NC6s_v3", 6, 1, "NVIDIA Tesla V100"},
		{"NC12s_v3", "Standard_NC12s_v3", 12, 2, "NVIDIA Tesla V100"},
		{"NC24s_v3", "Standard_NC24s_v3", 24, 4, "NVIDIA Tesla V100"},

		// NC A100 series
		{"NC24ads_A100", "Standard_NC24ads_A100_v4", 24, 1, "NVIDIA A100"},
		{"NC48ads_A100", "Standard_NC48ads_A100_v4", 48, 2, "NVIDIA A100"},
		{"NC96ads_A100", "Standard_NC96ads_A100_v4", 96, 4, "NVIDIA A100"},

		// NC T4 series
		{"NC4as_T4", "Standard_NC4as_T4_v3", 4, 1, "NVIDIA T4"},

		// ND series (P40)
		{"ND6", "Standard_ND6", 6, 1, "NVIDIA Tesla P40"},
		{"ND12", "Standard_ND12", 12, 2, "NVIDIA Tesla P40"},
		{"ND24", "Standard_ND24", 24, 4, "NVIDIA Tesla P40"},

		// ND A100
		{"ND96_A100", "Standard_ND96asr_A100_v4", 96, 8, "NVIDIA A100"},

		// ND H100
		{"ND96_H100", "Standard_ND96isr_H100_v5", 96, 8, "NVIDIA H100"},

		// NV series (M60)
		{"NV6_M60", "Standard_NV6", 6, 1, "NVIDIA Tesla M60"},
		{"NV12_M60", "Standard_NV12", 12, 2, "NVIDIA Tesla M60"},
		{"NV24_M60", "Standard_NV24", 24, 4, "NVIDIA Tesla M60"},

		// NV v3 series (M60)
		{"NV12s_v3", "Standard_NV12s_v3", 12, 1, "NVIDIA Tesla M60"},
		{"NV24s_v3", "Standard_NV24s_v3", 24, 2, "NVIDIA Tesla M60"},
		{"NV48s_v3", "Standard_NV48s_v3", 48, 4, "NVIDIA Tesla M60"},

		// NV A10 series
		{"NV6ads_A10", "Standard_NV6ads_A10_v5", 6, 1, "NVIDIA A10"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gpus, gpuType := detectGPU(tt.vmName, tt.cores)
			if gpus != tt.wantGPUs {
				t.Errorf("detectGPU(%q, %d) gpus = %d, want %d", tt.vmName, tt.cores, gpus, tt.wantGPUs)
			}
			if gpuType != tt.wantGPUType {
				t.Errorf("detectGPU(%q, %d) gpuType = %q, want %q", tt.vmName, tt.cores, gpuType, tt.wantGPUType)
			}
		})
	}
}

func TestDetectGPU_NonGPU(t *testing.T) {
	// detectGPU is only called for names containing "standard_n",
	// but if called for a non-N series, it should still return something
	// due to the fallback at the end. This tests the function signature
	// behavior for N-series VMs that don't match specific patterns.
	gpus, gpuType := detectGPU("Standard_N_unknown", 4)
	if gpus != 1 || gpuType != "NVIDIA GPU" {
		t.Errorf("detectGPU for unknown N-series = (%d, %q), want (1, 'NVIDIA GPU')", gpus, gpuType)
	}
}

func TestVmssToNodeGroup(t *testing.T) {
	tests := []struct {
		name              string
		vmss              vmssResource
		poolName          string
		region            string
		wantLifecycle     string
		wantInstanceType  string
		wantCurrentCount  int
		wantName          string
		wantID            string
	}{
		{
			name: "regular on-demand VMSS",
			vmss: vmssResource{
				ID:       "/subscriptions/sub1/resourceGroups/rg1/providers/Microsoft.Compute/virtualMachineScaleSets/aks-nodepool1",
				Name:     "aks-nodepool1",
				Location: "eastus",
				Tags: map[string]string{
					"aks-managed-poolName": "nodepool1",
				},
				Sku: vmsSku{
					Name:     "Standard_D4s_v3",
					Tier:     "Standard",
					Capacity: 3,
				},
				Properties: vmssProperties{
					ProvisioningState: "Succeeded",
					VirtualMachineProfile: vmProfile{
						Priority: "Regular",
					},
				},
			},
			poolName:         "nodepool1",
			region:           "eastus",
			wantLifecycle:    "on-demand",
			wantInstanceType: "Standard_D4s_v3",
			wantCurrentCount: 3,
			wantName:         "nodepool1",
			wantID:           "aks-nodepool1",
		},
		{
			name: "spot VMSS",
			vmss: vmssResource{
				ID:       "/subscriptions/sub1/resourceGroups/rg1/providers/Microsoft.Compute/virtualMachineScaleSets/aks-spotpool1",
				Name:     "aks-spotpool1",
				Location: "eastus",
				Tags: map[string]string{
					"aks-managed-poolName": "spotpool1",
				},
				Sku: vmsSku{
					Name:     "Standard_D8s_v5",
					Capacity: 5,
				},
				Properties: vmssProperties{
					ProvisioningState: "Succeeded",
					VirtualMachineProfile: vmProfile{
						Priority:       "Spot",
						EvictionPolicy: "Delete",
						BillingProfile: &billingProfile{MaxPrice: -1},
					},
				},
			},
			poolName:         "spotpool1",
			region:           "westus2",
			wantLifecycle:    "spot",
			wantInstanceType: "Standard_D8s_v5",
			wantCurrentCount: 5,
			wantName:         "spotpool1",
			wantID:           "aks-spotpool1",
		},
		{
			name: "VMSS with autoscaler tags",
			vmss: vmssResource{
				Name: "aks-gpupool",
				Tags: map[string]string{
					"aks-managed-poolName":              "gpupool",
					"aks-managed-autoScalerMinCount":    "1",
					"aks-managed-autoScalerMaxCount":    "10",
				},
				Sku: vmsSku{
					Name:     "Standard_NC6s_v3",
					Capacity: 2,
				},
			},
			poolName:         "gpupool",
			region:           "eastus2",
			wantLifecycle:    "on-demand",
			wantInstanceType: "Standard_NC6s_v3",
			wantCurrentCount: 2,
			wantName:         "gpupool",
			wantID:           "aks-gpupool",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ng := vmssToNodeGroup(tt.vmss, tt.poolName, tt.region)

			if ng.Lifecycle != tt.wantLifecycle {
				t.Errorf("Lifecycle = %q, want %q", ng.Lifecycle, tt.wantLifecycle)
			}
			if ng.InstanceType != tt.wantInstanceType {
				t.Errorf("InstanceType = %q, want %q", ng.InstanceType, tt.wantInstanceType)
			}
			if ng.CurrentCount != tt.wantCurrentCount {
				t.Errorf("CurrentCount = %d, want %d", ng.CurrentCount, tt.wantCurrentCount)
			}
			if ng.DesiredCount != tt.wantCurrentCount {
				t.Errorf("DesiredCount = %d, want %d", ng.DesiredCount, tt.wantCurrentCount)
			}
			if ng.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", ng.Name, tt.wantName)
			}
			if ng.ID != tt.wantID {
				t.Errorf("ID = %q, want %q", ng.ID, tt.wantID)
			}
			if ng.Region != tt.region {
				t.Errorf("Region = %q, want %q", ng.Region, tt.region)
			}
		})
	}
}

func TestVmssToNodeGroup_AutoscalerTags(t *testing.T) {
	vmss := vmssResource{
		Name: "aks-pool1",
		Tags: map[string]string{
			"aks-managed-poolName":           "pool1",
			"aks-managed-autoScalerMinCount": "2",
			"aks-managed-autoScalerMaxCount": "20",
		},
		Sku: vmsSku{
			Name:     "Standard_D4s_v5",
			Capacity: 5,
		},
	}

	ng := vmssToNodeGroup(vmss, "pool1", "eastus")

	if ng.MinCount != 2 {
		t.Errorf("MinCount = %d, want 2", ng.MinCount)
	}
	if ng.MaxCount != 20 {
		t.Errorf("MaxCount = %d, want 20", ng.MaxCount)
	}
}

func TestVmssToNodeGroup_NoAutoscalerTags(t *testing.T) {
	vmss := vmssResource{
		Name: "aks-pool2",
		Tags: map[string]string{
			"aks-managed-poolName": "pool2",
		},
		Sku: vmsSku{
			Name:     "Standard_D4s_v5",
			Capacity: 3,
		},
	}

	ng := vmssToNodeGroup(vmss, "pool2", "eastus")

	if ng.MinCount != 0 {
		t.Errorf("MinCount = %d, want 0 (no tags)", ng.MinCount)
	}
	if ng.MaxCount != 0 {
		t.Errorf("MaxCount = %d, want 0 (no tags)", ng.MaxCount)
	}
}

func TestVmssToNodeGroup_InvalidAutoscalerTags(t *testing.T) {
	vmss := vmssResource{
		Name: "aks-pool3",
		Tags: map[string]string{
			"aks-managed-poolName":           "pool3",
			"aks-managed-autoScalerMinCount": "not-a-number",
			"aks-managed-autoScalerMaxCount": "also-not",
		},
		Sku: vmsSku{
			Name:     "Standard_D4s_v5",
			Capacity: 3,
		},
	}

	ng := vmssToNodeGroup(vmss, "pool3", "eastus")

	// Invalid tags should result in 0 (Atoi fails silently)
	if ng.MinCount != 0 {
		t.Errorf("MinCount = %d, want 0 (invalid tag)", ng.MinCount)
	}
	if ng.MaxCount != 0 {
		t.Errorf("MaxCount = %d, want 0 (invalid tag)", ng.MaxCount)
	}
}

func TestVmssToNodeGroup_InstanceFamily(t *testing.T) {
	vmss := vmssResource{
		Name: "aks-pool",
		Tags: map[string]string{"aks-managed-poolName": "pool"},
		Sku:  vmsSku{Name: "Standard_E8s_v5", Capacity: 1},
	}

	ng := vmssToNodeGroup(vmss, "pool", "eastus")

	// Standard_E8s_v5 → family should be extracted by familylock.ExtractFamily
	if ng.InstanceFamily == "" {
		t.Error("InstanceFamily should not be empty")
	}
}

func TestVmssToNodeGroup_LabelsFromTags(t *testing.T) {
	vmss := vmssResource{
		Name: "aks-pool",
		Tags: map[string]string{
			"aks-managed-poolName": "pool",
			"env":                 "production",
			"team":                "platform",
		},
		Sku: vmsSku{Name: "Standard_D4s_v5", Capacity: 1},
	}

	ng := vmssToNodeGroup(vmss, "pool", "eastus")

	if ng.Labels["env"] != "production" {
		t.Errorf("Labels[env] = %q, want %q", ng.Labels["env"], "production")
	}
	if ng.Labels["team"] != "platform" {
		t.Errorf("Labels[team] = %q, want %q", ng.Labels["team"], "platform")
	}
}

func TestVmssToNodeGroup_SpotCaseInsensitive(t *testing.T) {
	// Test that "spot" in various cases is handled
	vmss := vmssResource{
		Name: "aks-spot",
		Tags: map[string]string{"aks-managed-poolName": "spot"},
		Sku:  vmsSku{Name: "Standard_D4s_v5", Capacity: 1},
		Properties: vmssProperties{
			VirtualMachineProfile: vmProfile{
				Priority: "SPOT", // uppercase
			},
		},
	}

	ng := vmssToNodeGroup(vmss, "spot", "eastus")

	// EqualFold should handle this
	if ng.Lifecycle != "spot" {
		t.Errorf("Lifecycle = %q, want %q for Priority=SPOT", ng.Lifecycle, "spot")
	}
}

func TestGetHardcodedVMSizes(t *testing.T) {
	sizes := getHardcodedVMSizes()

	if len(sizes) == 0 {
		t.Fatal("getHardcodedVMSizes() returned 0 sizes")
	}

	// Verify some expected entries exist
	found := map[string]bool{
		"Standard_B2ms":   false,
		"Standard_D4s_v3": false,
		"Standard_D4s_v5": false,
		"Standard_E8s_v5": false,
		"Standard_NC6":    false,
		"Standard_D4ps_v5": false,
	}

	for _, s := range sizes {
		if _, ok := found[s.Name]; ok {
			found[s.Name] = true
		}
	}

	for name, wasFound := range found {
		if !wasFound {
			t.Errorf("expected VM size %q not found in hardcoded list", name)
		}
	}

	// Verify ARM64 VMs have correct architecture
	for _, s := range sizes {
		if s.Name == "Standard_D4ps_v5" {
			if s.Architecture != "arm64" {
				t.Errorf("Standard_D4ps_v5 Architecture = %q, want arm64", s.Architecture)
			}
		}
		if s.Name == "Standard_D4s_v5" {
			if s.Architecture != "amd64" {
				t.Errorf("Standard_D4s_v5 Architecture = %q, want amd64", s.Architecture)
			}
		}
	}

	// Verify GPU VMs have GPU info
	for _, s := range sizes {
		if s.Name == "Standard_NC6" {
			if s.GPUs != 1 {
				t.Errorf("Standard_NC6 GPUs = %d, want 1", s.GPUs)
			}
			if s.GPUType == "" {
				t.Error("Standard_NC6 should have GPUType set")
			}
		}
	}
}

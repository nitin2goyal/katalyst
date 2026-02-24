package familylock

import (
	"testing"
)

func TestExtractFamily_AWS(t *testing.T) {
	tests := []struct {
		name         string
		instanceType string
		wantFamily   string
	}{
		{
			name:         "m5.xlarge",
			instanceType: "m5.xlarge",
			wantFamily:   "m5",
		},
		{
			name:         "c5d.4xlarge",
			instanceType: "c5d.4xlarge",
			wantFamily:   "c5d",
		},
		{
			name:         "r6g.medium",
			instanceType: "r6g.medium",
			wantFamily:   "r6g",
		},
		{
			name:         "p3.2xlarge",
			instanceType: "p3.2xlarge",
			wantFamily:   "p3",
		},
		{
			name:         "m5a.2xlarge",
			instanceType: "m5a.2xlarge",
			wantFamily:   "m5a",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractFamily(tt.instanceType)
			if err != nil {
				t.Fatalf("ExtractFamily(%q) returned unexpected error: %v", tt.instanceType, err)
			}
			if got != tt.wantFamily {
				t.Errorf("ExtractFamily(%q) = %q, want %q", tt.instanceType, got, tt.wantFamily)
			}
		})
	}
}

func TestExtractFamily_GCP(t *testing.T) {
	tests := []struct {
		name         string
		instanceType string
		wantFamily   string
	}{
		{
			name:         "n2-standard-4",
			instanceType: "n2-standard-4",
			wantFamily:   "n2-standard",
		},
		{
			name:         "e2-medium",
			instanceType: "e2-medium",
			wantFamily:   "e2",
		},
		{
			name:         "c2-standard-8",
			instanceType: "c2-standard-8",
			wantFamily:   "c2-standard",
		},
		{
			name:         "n2d-standard-32",
			instanceType: "n2d-standard-32",
			wantFamily:   "n2d-standard",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractFamily(tt.instanceType)
			if err != nil {
				t.Fatalf("ExtractFamily(%q) returned unexpected error: %v", tt.instanceType, err)
			}
			if got != tt.wantFamily {
				t.Errorf("ExtractFamily(%q) = %q, want %q", tt.instanceType, got, tt.wantFamily)
			}
		})
	}
}

func TestExtractFamily_Azure(t *testing.T) {
	tests := []struct {
		name         string
		instanceType string
		wantFamily   string
	}{
		{
			name:         "Standard_D4s_v3",
			instanceType: "Standard_D4s_v3",
			wantFamily:   "Standard_D_v3",
		},
		{
			name:         "Standard_E8as_v4",
			instanceType: "Standard_E8as_v4",
			wantFamily:   "Standard_E_v4",
		},
		{
			name:         "Standard_B2ms",
			instanceType: "Standard_B2ms",
			wantFamily:   "Standard_B",
		},
		{
			name:         "Standard_NC24ads_A100_v4",
			instanceType: "Standard_NC24ads_A100_v4",
			wantFamily:   "Standard_NC_v4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractFamily(tt.instanceType)
			if err != nil {
				t.Fatalf("ExtractFamily(%q) returned unexpected error: %v", tt.instanceType, err)
			}
			if got != tt.wantFamily {
				t.Errorf("ExtractFamily(%q) = %q, want %q", tt.instanceType, got, tt.wantFamily)
			}
		})
	}
}

func TestExtractFamily_Errors(t *testing.T) {
	tests := []struct {
		name         string
		instanceType string
	}{
		{
			name:         "empty string",
			instanceType: "",
		},
		{
			name:         "unrecognized format no delimiters",
			instanceType: "foobar123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractFamily(tt.instanceType)
			if err == nil {
				t.Errorf("ExtractFamily(%q) = %q, expected error", tt.instanceType, got)
			}
		})
	}
}

func TestIsSameFamily_SameFamily(t *testing.T) {
	tests := []struct {
		name  string
		typeA string
		typeB string
	}{
		{
			name:  "AWS m5.xlarge and m5.2xlarge",
			typeA: "m5.xlarge",
			typeB: "m5.2xlarge",
		},
		{
			name:  "GCP n2-standard-4 and n2-standard-8",
			typeA: "n2-standard-4",
			typeB: "n2-standard-8",
		},
		{
			name:  "AWS c5d.xlarge and c5d.4xlarge",
			typeA: "c5d.xlarge",
			typeB: "c5d.4xlarge",
		},
		{
			name:  "Azure Standard_D4s_v3 and Standard_D8s_v3",
			typeA: "Standard_D4s_v3",
			typeB: "Standard_D8s_v3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			same, err := IsSameFamily(tt.typeA, tt.typeB)
			if err != nil {
				t.Fatalf("IsSameFamily(%q, %q) returned unexpected error: %v", tt.typeA, tt.typeB, err)
			}
			if !same {
				t.Errorf("IsSameFamily(%q, %q) = false, want true", tt.typeA, tt.typeB)
			}
		})
	}
}

func TestIsSameFamily_DifferentFamily(t *testing.T) {
	tests := []struct {
		name  string
		typeA string
		typeB string
	}{
		{
			name:  "AWS m5.xlarge and c5.xlarge",
			typeA: "m5.xlarge",
			typeB: "c5.xlarge",
		},
		{
			name:  "GCP n2-standard-4 and e2-standard-4",
			typeA: "n2-standard-4",
			typeB: "e2-standard-4",
		},
		{
			name:  "AWS r6g.medium and m5.medium",
			typeA: "r6g.medium",
			typeB: "m5.medium",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			same, err := IsSameFamily(tt.typeA, tt.typeB)
			if err != nil {
				t.Fatalf("IsSameFamily(%q, %q) returned unexpected error: %v", tt.typeA, tt.typeB, err)
			}
			if same {
				t.Errorf("IsSameFamily(%q, %q) = true, want false", tt.typeA, tt.typeB)
			}
		})
	}
}

func TestIsSameFamily_Errors(t *testing.T) {
	tests := []struct {
		name  string
		typeA string
		typeB string
	}{
		{
			name:  "first type empty",
			typeA: "",
			typeB: "m5.xlarge",
		},
		{
			name:  "second type empty",
			typeA: "m5.xlarge",
			typeB: "",
		},
		{
			name:  "both types empty",
			typeA: "",
			typeB: "",
		},
		{
			name:  "first type unrecognized",
			typeA: "unknownformat",
			typeB: "m5.xlarge",
		},
		{
			name:  "second type unrecognized",
			typeA: "m5.xlarge",
			typeB: "unknownformat",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := IsSameFamily(tt.typeA, tt.typeB)
			if err == nil {
				t.Errorf("IsSameFamily(%q, %q) expected error, got nil", tt.typeA, tt.typeB)
			}
		})
	}
}

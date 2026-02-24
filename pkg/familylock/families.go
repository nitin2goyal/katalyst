package familylock

import (
	"fmt"
	"strings"
)

// ExtractFamily extracts the instance family from an instance type string.
// Examples:
//   - "m5.xlarge" → "m5"
//   - "m5a.2xlarge" → "m5a"
//   - "c5d.4xlarge" → "c5d"
//   - "r6g.medium" → "r6g"
//   - "p3.2xlarge" → "p3"
//   - "n2-standard-4" (GCP) → "n2-standard"
//   - "e2-medium" (GCP) → "e2"
//   - "Standard_D4s_v3" (Azure) → "Standard_D_v3" (family class)
func ExtractFamily(instanceType string) (string, error) {
	if instanceType == "" {
		return "", fmt.Errorf("empty instance type")
	}

	// AWS: family.size format (e.g., m5.xlarge, c5d.2xlarge)
	if parts := strings.SplitN(instanceType, ".", 2); len(parts) == 2 {
		return parts[0], nil
	}

	// GCP: family-size format (e.g., n2-standard-4, e2-medium)
	if strings.Contains(instanceType, "-") {
		parts := strings.Split(instanceType, "-")
		if len(parts) >= 3 {
			// n2-standard-4 → n2-standard
			return strings.Join(parts[:len(parts)-1], "-"), nil
		}
		if len(parts) == 2 {
			// e2-medium → e2
			return parts[0], nil
		}
	}

	// Azure: Standard_D4s_v3 → Standard_D_v3
	if strings.HasPrefix(instanceType, "Standard_") {
		return extractAzureFamily(instanceType), nil
	}

	return "", fmt.Errorf("unrecognized instance type format: %s", instanceType)
}

// extractAzureFamily extracts the family from Azure VM sizes.
// Standard_D4s_v3 → Standard_D_v3
// Standard_E8as_v4 → Standard_E_v4
func extractAzureFamily(vmSize string) string {
	parts := strings.Split(vmSize, "_")
	if len(parts) < 2 {
		return vmSize
	}

	// Extract the letter prefix from the size part (e.g., "D4s" → "D", "E8as" → "E")
	sizePart := parts[1]
	family := ""
	for _, c := range sizePart {
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' {
			if family == "" || (c >= 'A' && c <= 'Z') {
				family += string(c)
			} else {
				break
			}
		} else {
			break
		}
	}

	result := parts[0] + "_" + family
	// Append version suffix if present (e.g., _v3, _v4)
	for i := 2; i < len(parts); i++ {
		if strings.HasPrefix(parts[i], "v") {
			result += "_" + parts[i]
		}
	}
	return result
}

// IsSameFamily checks if two instance types belong to the same family.
func IsSameFamily(typeA, typeB string) (bool, error) {
	familyA, err := ExtractFamily(typeA)
	if err != nil {
		return false, fmt.Errorf("extracting family from %s: %w", typeA, err)
	}
	familyB, err := ExtractFamily(typeB)
	if err != nil {
		return false, fmt.Errorf("extracting family from %s: %w", typeB, err)
	}
	return strings.EqualFold(familyA, familyB), nil
}

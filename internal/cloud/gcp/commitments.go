package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
)

// gceCommitment represents a GCP Committed Use Discount from the Compute Engine API.
type gceCommitment struct {
	ID            string              `json:"id"`
	Name          string              `json:"name"`
	Status        string              `json:"status"`
	Plan          string              `json:"plan"` // TWELVE_MONTH or THIRTY_SIX_MONTH
	StartTimestamp string             `json:"startTimestamp"`
	EndTimestamp   string             `json:"endTimestamp"`
	Region        string              `json:"region"`
	Resources     []gceCommitmentResource `json:"resources"`
	Type          string              `json:"type"` // GENERAL_PURPOSE, COMPUTE_OPTIMIZED, etc.
	Category      string              `json:"category"`
}

// gceCommitmentResource represents a resource within a commitment.
type gceCommitmentResource struct {
	Type   string `json:"type"`   // VCPU, MEMORY
	Amount string `json:"amount"` // amount as string
}

// gceCommitmentListResponse is the paginated response from the commitments API.
type gceCommitmentListResponse struct {
	Items         []gceCommitment `json:"items"`
	NextPageToken string          `json:"nextPageToken"`
}

// getGCPCUDs fetches Committed Use Discounts from GCP.
func getGCPCUDs(ctx context.Context, project, region string, client *http.Client) ([]*cloudprovider.Commitment, error) {
	var allCommitments []*cloudprovider.Commitment
	pageToken := ""

	const maxPages = 50
	for page := 0; page < maxPages; page++ {
		url := fmt.Sprintf("%s/projects/%s/regions/%s/commitments?filter=status=ACTIVE",
			computeBaseURL, project, region)
		if pageToken != "" {
			url += "&pageToken=" + pageToken
		}

		body, err := doGCPGet(ctx, client, url)
		if err != nil {
			return nil, fmt.Errorf("listing GCP CUDs in %s: %w", region, err)
		}

		var resp gceCommitmentListResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("parsing CUD response: %w", err)
		}

		for _, c := range resp.Items {
			commitment, err := mapCommitment(c, region)
			if err != nil {
				continue // skip malformed commitments
			}
			allCommitments = append(allCommitments, commitment)
		}

		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}

	return allCommitments, nil
}

// mapCommitment converts a GCE commitment to a cloudprovider.Commitment.
func mapCommitment(c gceCommitment, region string) (*cloudprovider.Commitment, error) {
	var expiresAt time.Time
	if c.EndTimestamp != "" {
		t, err := time.Parse(time.RFC3339, c.EndTimestamp)
		if err != nil {
			return nil, fmt.Errorf("parsing end timestamp %q: %w", c.EndTimestamp, err)
		}
		expiresAt = t
	}

	// Determine the instance family from the commitment type.
	instanceFamily := mapCommitmentTypeToFamily(c.Type)

	// Parse vCPU and memory from resources.
	vcpuCount := 0
	memoryGB := 0.0
	for _, r := range c.Resources {
		if strings.EqualFold(r.Type, "VCPU") {
			var amount int
			if _, err := fmt.Sscanf(r.Amount, "%d", &amount); err != nil {
				continue
			}
			vcpuCount = amount
		}
		if strings.EqualFold(r.Type, "MEMORY") {
			// GCP memory commitments are in MB.
			var amount float64
			if _, err := fmt.Sscanf(r.Amount, "%f", &amount); err != nil {
				continue
			}
			memoryGB = amount / 1024.0
		}
	}

	// Calculate the discount factor based on plan duration.
	// 1-year CUDs get ~37% discount, 3-year CUDs get ~55% discount.
	discountFactor := 0.63 // 1-year default
	if strings.Contains(c.Plan, "THIRTY_SIX") {
		discountFactor = 0.45
	}

	// Use family-specific pricing or N2 as a baseline.
	cp := gcpFamilyPricing["n2"]
	if familyPricing, ok := gcpFamilyPricing[instanceFamily]; ok {
		cp = familyPricing
	}

	onDemandCost := cp.cpuPerHour*float64(vcpuCount) + cp.memPerHour*memoryGB
	hourlyCost := onDemandCost * discountFactor

	return &cloudprovider.Commitment{
		ID:              c.ID,
		Type:            "cud",
		InstanceFamily:  instanceFamily,
		Region:          region,
		Count:           vcpuCount,
		HourlyCostUSD:   hourlyCost,
		OnDemandCostUSD: onDemandCost,
		UtilizationPct:  0, // utilization must be computed externally
		ExpiresAt:       expiresAt,
		Status:          strings.ToLower(c.Status),
	}, nil
}

// mapCommitmentTypeToFamily converts a GCE commitment type to a machine family prefix.
func mapCommitmentTypeToFamily(commitmentType string) string {
	switch strings.ToUpper(commitmentType) {
	case "COMPUTE_OPTIMIZED":
		return "c2"
	case "COMPUTE_OPTIMIZED_C2D":
		return "c2d"
	case "GENERAL_PURPOSE_N2":
		return "n2"
	case "GENERAL_PURPOSE_N2D":
		return "n2d"
	case "GENERAL_PURPOSE_E2":
		return "e2"
	case "GENERAL_PURPOSE_T2D":
		return "t2d"
	case "GENERAL_PURPOSE_T2A":
		return "t2a"
	case "GENERAL_PURPOSE":
		return "n1"
	case "MEMORY_OPTIMIZED":
		return "m2"
	case "ACCELERATOR_OPTIMIZED":
		return "a2"
	case "COMPUTE_OPTIMIZED_C3":
		return "c3"
	case "COMPUTE_OPTIMIZED_C3D":
		return "c3d"
	case "GENERAL_PURPOSE_N4":
		return "n4"
	case "COMPUTE_OPTIMIZED_C4":
		return "c4"
	case "MEMORY_OPTIMIZED_M3":
		return "m3"
	default:
		return "n1"
	}
}

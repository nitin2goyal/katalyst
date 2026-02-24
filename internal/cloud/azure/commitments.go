package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
	"github.com/koptimizer/koptimizer/pkg/familylock"
)

const reservationsAPIVersion = "2024-04-01"

// reservationOrderListResponse is the ARM response for listing reservation orders.
type reservationOrderListResponse struct {
	Value    []reservationOrderResource `json:"value"`
	NextLink string                     `json:"nextLink"`
}

// reservationOrderResource represents an Azure reservation order.
type reservationOrderResource struct {
	ID         string                        `json:"id"`
	Name       string                        `json:"name"`
	Type       string                        `json:"type"`
	Properties reservationOrderProperties    `json:"properties"`
}

// reservationOrderProperties holds the properties of a reservation order.
type reservationOrderProperties struct {
	DisplayName     string                    `json:"displayName"`
	RequestDateTime string                    `json:"requestDateTime"`
	CreatedDateTime string                    `json:"createdDateTime"`
	ExpiryDate      string                    `json:"expiryDate"`
	ExpiryDateTime  string                    `json:"expiryDateTime"`
	Term            string                    `json:"term"`
	ProvisionState  string                    `json:"provisioningState"`
	Reservations    []reservationReference    `json:"reservations"`
	BillingPlan     string                    `json:"billingPlan"`
}

// reservationReference is a reference to a reservation within an order.
type reservationReference struct {
	ID string `json:"id"`
}

// reservationDetailResponse is the ARM response for getting a reservation's details.
type reservationDetailResponse struct {
	ID         string                    `json:"id"`
	Name       string                    `json:"name"`
	Type       string                    `json:"type"`
	Location   string                    `json:"location"`
	Sku        reservationSku            `json:"sku"`
	Properties reservationProperties     `json:"properties"`
}

// reservationSku holds the SKU info for a reservation.
type reservationSku struct {
	Name string `json:"name"`
}

// reservationProperties holds the properties of a reservation.
type reservationProperties struct {
	ReservedResourceType string              `json:"reservedResourceType"`
	InstanceFlexibility  string              `json:"instanceFlexibility"`
	DisplayName          string              `json:"displayName"`
	AppliedScopes        []string            `json:"appliedScopes"`
	AppliedScopeType     string              `json:"appliedScopeType"`
	Quantity             int                 `json:"quantity"`
	ProvisionState       string              `json:"provisioningState"`
	EffectiveDateTime    string              `json:"effectiveDateTime"`
	ExpiryDate           string              `json:"expiryDate"`
	ExpiryDateTime       string              `json:"expiryDateTime"`
	Renew                bool                `json:"renew"`
	Utilization          *reservationUtil    `json:"utilization"`
	BillingCurrencyTotal *currencyAmount     `json:"billingCurrencyTotal"`
	PricingCurrencyTotal *currencyAmount     `json:"pricingCurrencyTotal"`
}

// reservationUtil holds utilization details for a reservation.
type reservationUtil struct {
	Trend      string            `json:"trend"`
	Aggregates []utilizationAgg  `json:"aggregates"`
}

// utilizationAgg is a utilization aggregation entry.
type utilizationAgg struct {
	Grain float64 `json:"grain"`
	Value float64 `json:"value"`
}

// currencyAmount holds a currency amount.
type currencyAmount struct {
	CurrencyCode string  `json:"currencyCode"`
	Amount       float64 `json:"amount"`
}

// reservationListResponse is the ARM response for listing reservations within an order.
type reservationListResponse struct {
	Value    []reservationDetailResponse `json:"value"`
	NextLink string                      `json:"nextLink"`
}

// getAzureReservations fetches Azure Reservations from the Reservations REST API.
func getAzureReservations(ctx context.Context, p *Provider) ([]*cloudprovider.Commitment, error) {
	// List all reservation orders accessible to this identity.
	orderURL := fmt.Sprintf("%s/providers/Microsoft.Capacity/reservationOrders?api-version=%s",
		armBaseURL, reservationsAPIVersion)

	var commitments []*cloudprovider.Commitment

	for orderURL != "" {
		resp, err := p.doARMRequest(ctx, "GET", orderURL, nil)
		if err != nil {
			return nil, fmt.Errorf("listing reservation orders: %w", err)
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("reading reservation orders response: %w", err)
		}

		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("reservation orders returned status %d: %s", resp.StatusCode, string(body))
		}

		var orderList reservationOrderListResponse
		if err := json.Unmarshal(body, &orderList); err != nil {
			return nil, fmt.Errorf("decoding reservation orders: %w", err)
		}

		for _, order := range orderList.Value {
			// For each order, list the reservations within it to get detailed info.
			orderReservations, err := listReservationsInOrder(ctx, p, order.Name)
			if err != nil {
				// Log error but continue with other orders.
				continue
			}

			for _, res := range orderReservations {
				commitment := reservationToCommitment(res, order)
				if commitment != nil {
					commitments = append(commitments, commitment)
				}
			}
		}

		orderURL = orderList.NextLink
	}

	return commitments, nil
}

// listReservationsInOrder lists all reservations within a reservation order.
func listReservationsInOrder(ctx context.Context, p *Provider, orderID string) ([]reservationDetailResponse, error) {
	listURL := fmt.Sprintf("%s/providers/Microsoft.Capacity/reservationOrders/%s/reservations?api-version=%s",
		armBaseURL, orderID, reservationsAPIVersion)

	var allReservations []reservationDetailResponse

	for listURL != "" {
		resp, err := p.doARMRequest(ctx, "GET", listURL, nil)
		if err != nil {
			return nil, fmt.Errorf("listing reservations in order %s: %w", orderID, err)
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("reading reservations response: %w", err)
		}

		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("reservations list returned status %d: %s", resp.StatusCode, string(body))
		}

		var listResp reservationListResponse
		if err := json.Unmarshal(body, &listResp); err != nil {
			return nil, fmt.Errorf("decoding reservations list: %w", err)
		}

		allReservations = append(allReservations, listResp.Value...)
		listURL = listResp.NextLink
	}

	return allReservations, nil
}

// reservationToCommitment converts a reservation detail to a Commitment.
func reservationToCommitment(res reservationDetailResponse, order reservationOrderResource) *cloudprovider.Commitment {
	// Only process VM reservations.
	if !strings.EqualFold(res.Properties.ReservedResourceType, "VirtualMachines") &&
		res.Properties.ReservedResourceType != "" {
		return nil
	}

	instanceType := res.Sku.Name
	instanceFamily, _ := familylock.ExtractFamily(instanceType)

	status := "active"
	provState := strings.ToLower(res.Properties.ProvisionState)
	if provState == "expired" || provState == "cancelled" || provState == "failed" {
		status = provState
	}

	// Parse expiry date.
	var expiresAt time.Time
	expiryStr := res.Properties.ExpiryDateTime
	if expiryStr == "" {
		expiryStr = res.Properties.ExpiryDate
	}
	if expiryStr == "" {
		expiryStr = order.Properties.ExpiryDateTime
	}
	if expiryStr == "" {
		expiryStr = order.Properties.ExpiryDate
	}
	if expiryStr != "" {
		// Try multiple time formats.
		for _, format := range []string{
			time.RFC3339,
			"2006-01-02T15:04:05Z",
			"2006-01-02T15:04:05",
			"2006-01-02",
		} {
			if t, err := time.Parse(format, expiryStr); err == nil {
				expiresAt = t
				break
			}
		}
	}

	// Extract utilization percentage.
	utilizationPct := 0.0
	if res.Properties.Utilization != nil && len(res.Properties.Utilization.Aggregates) > 0 {
		// Use the last aggregate value as the utilization percentage.
		utilizationPct = res.Properties.Utilization.Aggregates[len(res.Properties.Utilization.Aggregates)-1].Value
	}

	// Calculate hourly cost from the total billing amount.
	hourlyCost := 0.0
	if res.Properties.BillingCurrencyTotal != nil {
		totalAmount := res.Properties.BillingCurrencyTotal.Amount
		// Estimate hourly cost based on the reservation term.
		term := order.Properties.Term
		var termHours float64
		switch {
		case strings.Contains(term, "P3Y") || strings.Contains(term, "3Year"):
			termHours = 3 * 365.25 * 24
		case strings.Contains(term, "P5Y") || strings.Contains(term, "5Year"):
			termHours = 5 * 365.25 * 24
		default:
			// Default to 1 year
			termHours = 365.25 * 24
		}
		if termHours > 0 && totalAmount > 0 {
			hourlyCost = totalAmount / termHours
		}
	}

	region := res.Location

	return &cloudprovider.Commitment{
		ID:              res.Name,
		Type:            "reservation",
		InstanceFamily:  instanceFamily,
		InstanceType:    instanceType,
		Region:          region,
		Count:           res.Properties.Quantity,
		HourlyCostUSD:   hourlyCost,
		UtilizationPct:  utilizationPct,
		ExpiresAt:       expiresAt,
		Status:          status,
	}
}

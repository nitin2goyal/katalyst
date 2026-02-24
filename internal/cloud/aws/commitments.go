package aws

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/savingsplans"
	sptypes "github.com/aws/aws-sdk-go-v2/service/savingsplans/types"

	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
	"github.com/koptimizer/koptimizer/pkg/familylock"
)

// getReservedInstances fetches all active RIs from AWS.
func getReservedInstances(ctx context.Context, client *ec2.Client) ([]*cloudprovider.Commitment, error) {
	resp, err := client.DescribeReservedInstances(ctx, &ec2.DescribeReservedInstancesInput{})
	if err != nil {
		return nil, fmt.Errorf("describing reserved instances: %w", err)
	}

	var commitments []*cloudprovider.Commitment
	for _, ri := range resp.ReservedInstances {
		instanceType := string(ri.InstanceType)
		family, _ := familylock.ExtractFamily(instanceType)

		status := "active"
		if ri.State != "active" {
			status = string(ri.State)
		}

		var expiresAt time.Time
		if ri.End != nil {
			expiresAt = *ri.End
		}

		count := 1
		if ri.InstanceCount != nil {
			count = int(*ri.InstanceCount)
		}

		hourlyCost := 0.0
		if ri.FixedPrice != nil {
			// Convert total fixed (upfront) price to per-instance hourly rate.
			if ri.Duration != nil {
				hours := float64(*ri.Duration) / 3600
				if hours > 0 && count > 0 {
					hourlyCost = float64(*ri.FixedPrice) / hours / float64(count)
				}
			}
		}
		if ri.RecurringCharges != nil {
			for _, rc := range ri.RecurringCharges {
				if rc.Amount != nil && rc.Frequency == "Hourly" {
					hourlyCost += *rc.Amount
				}
			}
		}

		// AvailabilityZone is a zone like "us-east-1a"; extract region by trimming the trailing letter.
		region := stringVal(ri.AvailabilityZone)
		if len(region) > 0 && region[len(region)-1] >= 'a' && region[len(region)-1] <= 'z' {
			region = region[:len(region)-1]
		}

		// UsagePrice is the hourly on-demand equivalent rate; set OnDemandCostUSD
		// so downstream savings calculations don't require a separate pricing lookup.
		onDemandCost := 0.0
		if ri.UsagePrice != nil {
			onDemandCost = float64(*ri.UsagePrice)
		}

		commitments = append(commitments, &cloudprovider.Commitment{
			ID:              stringVal(ri.ReservedInstancesId),
			Type:            "reserved-instance",
			InstanceFamily:  family,
			InstanceType:    instanceType,
			Region:          region,
			Count:           count,
			HourlyCostUSD:   hourlyCost,
			OnDemandCostUSD: onDemandCost,
			UtilizationPct:  0, // Calculated later by utilization tracker
			ExpiresAt:       expiresAt,
			Status:          status,
		})
	}

	return commitments, nil
}

// getSavingsPlans fetches active Savings Plans from the AWS Savings Plans API.
func getSavingsPlans(ctx context.Context, spClient savingsPlansClient) ([]*cloudprovider.Commitment, error) {
	resp, err := spClient.DescribeSavingsPlans(ctx, &savingsplans.DescribeSavingsPlansInput{
		States: []sptypes.SavingsPlanState{sptypes.SavingsPlanStateActive},
	})
	if err != nil {
		return nil, fmt.Errorf("describing savings plans: %w", err)
	}

	var commitments []*cloudprovider.Commitment
	for _, sp := range resp.SavingsPlans {
		spType := "savings-plan"
		if sp.SavingsPlanType == sptypes.SavingsPlanTypeCompute {
			spType = "compute-savings-plan"
		} else if sp.SavingsPlanType == sptypes.SavingsPlanTypeEc2Instance {
			spType = "ec2-instance-savings-plan"
		}

		var expiresAt time.Time
		if sp.End != nil {
			expiresAt, _ = time.Parse(time.RFC3339, *sp.End)
		}

		hourlyCost := 0.0
		if sp.Commitment != nil {
			if _, err := fmt.Sscanf(*sp.Commitment, "%f", &hourlyCost); err != nil {
				hourlyCost = 0
			}
		}

		instanceFamily := ""
		region := ""
		// EC2 Instance Savings Plans are scoped to a family + region
		for _, filter := range sp.ProductTypes {
			_ = filter
		}
		if sp.Ec2InstanceFamily != nil {
			instanceFamily = *sp.Ec2InstanceFamily
		}
		if sp.Region != nil {
			region = *sp.Region
		}

		// Estimate on-demand equivalent cost for savings calculation.
		// Savings Plans typically provide ~30% discount on Compute, ~40% on EC2 Instance.
		onDemandEstimate := hourlyCost
		if spType == "compute-savings-plan" {
			onDemandEstimate = hourlyCost / 0.70 // ~30% discount
		} else if spType == "ec2-instance-savings-plan" {
			onDemandEstimate = hourlyCost / 0.60 // ~40% discount
		}

		commitments = append(commitments, &cloudprovider.Commitment{
			ID:              stringVal(sp.SavingsPlanId),
			Type:            spType,
			InstanceFamily:  instanceFamily,
			Region:          region,
			Count:           1,
			HourlyCostUSD:   hourlyCost,
			OnDemandCostUSD: onDemandEstimate,
			UtilizationPct:  0, // Calculated later by utilization tracker
			ExpiresAt:       expiresAt,
			Status:          string(sp.State),
		})
	}

	return commitments, nil
}

// savingsPlansClient interface for testability.
type savingsPlansClient interface {
	DescribeSavingsPlans(ctx context.Context, params *savingsplans.DescribeSavingsPlansInput, optFns ...func(*savingsplans.Options)) (*savingsplans.DescribeSavingsPlansOutput, error)
}

func stringVal(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// matchRIToNodeGroup checks if a reserved instance matches a node group's family.
func matchRIToNodeGroup(ri *cloudprovider.Commitment, nodeGroupFamily string) bool {
	if ri.InstanceFamily == "" {
		return false
	}
	return strings.EqualFold(ri.InstanceFamily, nodeGroupFamily)
}

package aws

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	astypes "github.com/aws/aws-sdk-go-v2/service/autoscaling/types"

	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
	"github.com/koptimizer/koptimizer/pkg/familylock"
)

// discoverASGs finds all EKS-managed Auto Scaling Groups.
func discoverASGs(ctx context.Context, client *autoscaling.Client) ([]*cloudprovider.NodeGroup, error) {
	var groups []*cloudprovider.NodeGroup

	const maxPages = 100 // safety limit to prevent unbounded pagination
	paginator := autoscaling.NewDescribeAutoScalingGroupsPaginator(client, &autoscaling.DescribeAutoScalingGroupsInput{})
	for pageNum := 0; paginator.HasMorePages() && pageNum < maxPages; pageNum++ {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("describing ASGs: %w", err)
		}

		for _, asg := range page.AutoScalingGroups {
			if !isEKSNodeGroup(asg) {
				continue
			}

			instanceType := getASGInstanceType(asg)
			family, _ := familylock.ExtractFamily(instanceType)

			labels := make(map[string]string)
			for _, tag := range asg.Tags {
				if tag.Key != nil && tag.Value != nil {
					labels[*tag.Key] = *tag.Value
				}
			}

			zone := ""
			if len(asg.AvailabilityZones) > 0 {
				zone = asg.AvailabilityZones[0]
			}

			ng := &cloudprovider.NodeGroup{
				ID:             aws.ToString(asg.AutoScalingGroupName),
				Name:           aws.ToString(asg.AutoScalingGroupName),
				InstanceType:   instanceType,
				InstanceFamily: family,
				CurrentCount:   len(asg.Instances),
				MinCount:       int(aws.ToInt32(asg.MinSize)),
				MaxCount:       int(aws.ToInt32(asg.MaxSize)),
				DesiredCount:   int(aws.ToInt32(asg.DesiredCapacity)),
				Zone:           zone,
				Labels:         labels,
			}
			groups = append(groups, ng)
		}
	}

	return groups, nil
}

// getASG retrieves a single ASG by name.
func getASG(ctx context.Context, client *autoscaling.Client, id string) (*cloudprovider.NodeGroup, error) {
	resp, err := client.DescribeAutoScalingGroups(ctx, &autoscaling.DescribeAutoScalingGroupsInput{
		AutoScalingGroupNames: []string{id},
	})
	if err != nil {
		return nil, fmt.Errorf("describing ASG %s: %w", id, err)
	}
	if len(resp.AutoScalingGroups) == 0 {
		return nil, fmt.Errorf("ASG not found: %s", id)
	}

	asg := resp.AutoScalingGroups[0]
	instanceType := getASGInstanceType(asg)
	family, _ := familylock.ExtractFamily(instanceType)

	labels := make(map[string]string)
	for _, tag := range asg.Tags {
		if tag.Key != nil && tag.Value != nil {
			labels[*tag.Key] = *tag.Value
		}
	}

	zone := ""
	if len(asg.AvailabilityZones) > 0 {
		zone = asg.AvailabilityZones[0]
	}

	return &cloudprovider.NodeGroup{
		ID:             aws.ToString(asg.AutoScalingGroupName),
		Name:           aws.ToString(asg.AutoScalingGroupName),
		InstanceType:   instanceType,
		InstanceFamily: family,
		CurrentCount:   len(asg.Instances),
		MinCount:       int(aws.ToInt32(asg.MinSize)),
		MaxCount:       int(aws.ToInt32(asg.MaxSize)),
		DesiredCount:   int(aws.ToInt32(asg.DesiredCapacity)),
		Zone:           zone,
		Labels:         labels,
	}, nil
}

// scaleASG sets the desired capacity of an ASG.
func scaleASG(ctx context.Context, client *autoscaling.Client, id string, desiredCount int) error {
	_, err := client.SetDesiredCapacity(ctx, &autoscaling.SetDesiredCapacityInput{
		AutoScalingGroupName: aws.String(id),
		DesiredCapacity:      aws.Int32(int32(desiredCount)),
	})
	if err != nil {
		return fmt.Errorf("scaling ASG %s to %d: %w", id, desiredCount, err)
	}
	return nil
}

// setASGMinCount sets the minimum size of an ASG.
func setASGMinCount(ctx context.Context, client *autoscaling.Client, id string, minCount int) error {
	_, err := client.UpdateAutoScalingGroup(ctx, &autoscaling.UpdateAutoScalingGroupInput{
		AutoScalingGroupName: aws.String(id),
		MinSize:              aws.Int32(int32(minCount)),
	})
	if err != nil {
		return fmt.Errorf("setting ASG %s min to %d: %w", id, minCount, err)
	}
	return nil
}

// setASGMaxCount sets the maximum size of an ASG.
func setASGMaxCount(ctx context.Context, client *autoscaling.Client, id string, maxCount int) error {
	_, err := client.UpdateAutoScalingGroup(ctx, &autoscaling.UpdateAutoScalingGroupInput{
		AutoScalingGroupName: aws.String(id),
		MaxSize:              aws.Int32(int32(maxCount)),
	})
	if err != nil {
		return fmt.Errorf("setting ASG %s max to %d: %w", id, maxCount, err)
	}
	return nil
}

// isEKSNodeGroup checks if an ASG is an EKS-managed node group.
func isEKSNodeGroup(asg astypes.AutoScalingGroup) bool {
	for _, tag := range asg.Tags {
		if tag.Key != nil && strings.HasPrefix(*tag.Key, "kubernetes.io/cluster/") {
			return true
		}
		if tag.Key != nil && *tag.Key == "eks:cluster-name" {
			return true
		}
	}
	return false
}

// getASGInstanceType extracts the primary instance type from an ASG.
// For mixed instances policies, returns the first override type. Use
// getASGInstanceTypes() if you need all types in the mix.
func getASGInstanceType(asg astypes.AutoScalingGroup) string {
	types := getASGInstanceTypes(asg)
	if len(types) > 0 {
		return types[0]
	}
	return "unknown"
}

// getASGInstanceTypes returns all instance types configured for an ASG,
// including all overrides in a mixed instances policy.
func getASGInstanceTypes(asg astypes.AutoScalingGroup) []string {
	var types []string
	// Check mixed instances policy first - collect ALL overrides
	if asg.MixedInstancesPolicy != nil &&
		asg.MixedInstancesPolicy.LaunchTemplate != nil &&
		asg.MixedInstancesPolicy.LaunchTemplate.Overrides != nil {
		for _, override := range asg.MixedInstancesPolicy.LaunchTemplate.Overrides {
			if override.InstanceType != nil {
				types = append(types, *override.InstanceType)
			}
		}
		if len(types) > 0 {
			return types
		}
	}
	// Fall back to running instances
	seen := make(map[string]bool)
	for _, inst := range asg.Instances {
		if inst.InstanceType != nil && *inst.InstanceType != "" && !seen[*inst.InstanceType] {
			types = append(types, *inst.InstanceType)
			seen[*inst.InstanceType] = true
		}
	}
	return types
}

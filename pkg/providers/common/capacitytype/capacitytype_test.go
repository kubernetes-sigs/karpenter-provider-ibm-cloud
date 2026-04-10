/*
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package capacitytype

import (
	"context"
	"testing"

	"github.com/IBM/vpc-go-sdk/vpcv1"
	corev1 "k8s.io/api/core/v1"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"
)

func TestResolveCapacityType(t *testing.T) {
	tests := []struct {
		name          string
		nodeClaim     *karpv1.NodeClaim
		instanceTypes []*cloudprovider.InstanceType
		want          string
	}{
		{
			name:          "nil requirements defaults to on-demand",
			nodeClaim:     &karpv1.NodeClaim{},
			instanceTypes: []*cloudprovider.InstanceType{},
			want:          karpv1.CapacityTypeOnDemand,
		},
		{
			name: "empty requirements defaults to on-demand",
			nodeClaim: &karpv1.NodeClaim{
				Spec: karpv1.NodeClaimSpec{
					Requirements: []karpv1.NodeSelectorRequirementWithMinValues{},
				},
			},
			instanceTypes: []*cloudprovider.InstanceType{},
			want:          karpv1.CapacityTypeOnDemand,
		},
		{
			name: "on-demand only requirement returns on-demand",
			nodeClaim: &karpv1.NodeClaim{
				Spec: karpv1.NodeClaimSpec{
					Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
						{
							Key:      karpv1.CapacityTypeLabelKey,
							Operator: corev1.NodeSelectorOpIn,
							Values:   []string{karpv1.CapacityTypeOnDemand},
						},
					},
				},
			},
			instanceTypes: []*cloudprovider.InstanceType{},
			want:          karpv1.CapacityTypeOnDemand,
		},
		{
			name: "spot allowed but no available spot offering returns on-demand",
			nodeClaim: &karpv1.NodeClaim{
				Spec: karpv1.NodeClaimSpec{
					Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
						{
							Key:      karpv1.CapacityTypeLabelKey,
							Operator: corev1.NodeSelectorOpIn,
							Values:   []string{karpv1.CapacityTypeSpot, karpv1.CapacityTypeOnDemand},
						},
					},
				},
			},
			instanceTypes: []*cloudprovider.InstanceType{
				{
					Name: "test-type",
					Offerings: cloudprovider.Offerings{
						{
							Requirements: scheduling.NewRequirements(
								scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, karpv1.CapacityTypeOnDemand),
								scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, "us-south-1"),
							),
							Price:     0.1,
							Available: true,
						},
					},
				},
			},
			want: karpv1.CapacityTypeOnDemand,
		},
		{
			name: "spot allowed with available spot offering returns spot",
			nodeClaim: &karpv1.NodeClaim{
				Spec: karpv1.NodeClaimSpec{
					Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
						{
							Key:      karpv1.CapacityTypeLabelKey,
							Operator: corev1.NodeSelectorOpIn,
							Values:   []string{karpv1.CapacityTypeSpot, karpv1.CapacityTypeOnDemand},
						},
					},
				},
			},
			instanceTypes: []*cloudprovider.InstanceType{
				{
					Name: "test-type",
					Offerings: cloudprovider.Offerings{
						{
							Requirements: scheduling.NewRequirements(
								scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, karpv1.CapacityTypeSpot),
								scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, "us-south-1"),
							),
							Price:     0.06,
							Available: true,
						},
					},
				},
			},
			want: karpv1.CapacityTypeSpot,
		},
		{
			name: "spot allowed but spot offering unavailable returns on-demand",
			nodeClaim: &karpv1.NodeClaim{
				Spec: karpv1.NodeClaimSpec{
					Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
						{
							Key:      karpv1.CapacityTypeLabelKey,
							Operator: corev1.NodeSelectorOpIn,
							Values:   []string{karpv1.CapacityTypeSpot},
						},
					},
				},
			},
			instanceTypes: []*cloudprovider.InstanceType{
				{
					Name: "test-type",
					Offerings: cloudprovider.Offerings{
						{
							Requirements: scheduling.NewRequirements(
								scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, karpv1.CapacityTypeSpot),
								scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, "us-south-1"),
							),
							Price:     0.06,
							Available: false, // unavailable (e.g. recently preempted)
						},
					},
				},
			},
			want: karpv1.CapacityTypeOnDemand,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveCapacityType(tt.nodeClaim, tt.instanceTypes)
			if got != tt.want {
				t.Errorf("ResolveCapacityType() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGetSupportedCapacityTypes(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name              string
		availabilityClass vpcv1.InstanceProfileAvailabilityClassIntf
		want              []string
	}{
		{
			name:              "nil availability class defaults to on-demand",
			availabilityClass: nil,
			want:              []string{karpv1.CapacityTypeOnDemand},
		},
		{
			name: "enum with standard only",
			availabilityClass: &vpcv1.InstanceProfileAvailabilityClassEnum{
				Values: []string{"standard"},
			},
			want: []string{karpv1.CapacityTypeOnDemand},
		},
		{
			name: "enum with spot only",
			availabilityClass: &vpcv1.InstanceProfileAvailabilityClassEnum{
				Values: []string{"spot"},
			},
			want: []string{karpv1.CapacityTypeSpot},
		},
		{
			name: "enum with standard and spot",
			availabilityClass: &vpcv1.InstanceProfileAvailabilityClassEnum{
				Values: []string{"standard", "spot"},
			},
			want: []string{karpv1.CapacityTypeOnDemand, karpv1.CapacityTypeSpot},
		},
		{
			name: "fixed with standard",
			availabilityClass: &vpcv1.InstanceProfileAvailabilityClassFixed{
				Value: strPtr("standard"),
			},
			want: []string{karpv1.CapacityTypeOnDemand},
		},
		{
			name: "fixed with spot",
			availabilityClass: &vpcv1.InstanceProfileAvailabilityClassFixed{
				Value: strPtr("spot"),
			},
			want: []string{karpv1.CapacityTypeSpot},
		},
		{
			name: "fixed with nil value defaults to on-demand",
			availabilityClass: &vpcv1.InstanceProfileAvailabilityClassFixed{
				Value: nil,
			},
			want: []string{karpv1.CapacityTypeOnDemand},
		},
		{
			name: "enum with empty values defaults to on-demand",
			availabilityClass: &vpcv1.InstanceProfileAvailabilityClassEnum{
				Values: []string{},
			},
			want: []string{karpv1.CapacityTypeOnDemand},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetSupportedCapacityTypes(ctx, tt.availabilityClass)
			if len(got) != len(tt.want) {
				t.Fatalf("GetSupportedCapacityTypes() returned %d items %v, want %d items %v", len(got), got, len(tt.want), tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("GetSupportedCapacityTypes()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func strPtr(s string) *string { return &s }

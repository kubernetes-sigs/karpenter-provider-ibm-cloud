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

package instancetype

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	mockpricing "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/pricing/mock"
)

func TestRankingUsesAvailableOfferingPricesWithoutRemoteReads(t *testing.T) {
	provider := &IBMInstanceTypeProvider{client: &MockIBMClient{}, pricingProvider: mockpricing.NewMockProvider(gomock.NewController(t))}
	capacity := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("16Gi")}
	cheap := &cloudprovider.InstanceType{Name: "regional-cheap", Capacity: capacity, Offerings: cloudprovider.Offerings{{Price: .10, Available: true}}}
	expensive := &cloudprovider.InstanceType{Name: "regional-expensive", Capacity: capacity, Offerings: cloudprovider.Offerings{{Price: .01, Available: false}, {Price: .20, Available: true}}}
	require.Equal(t, []*cloudprovider.InstanceType{cheap, expensive}, provider.RankInstanceTypes([]*cloudprovider.InstanceType{expensive, cheap}))
}

func constrainedCatalog(t *testing.T, quote func(string) (float64, error)) *IBMInstanceTypeProvider {
	t.Helper()
	ctrl := gomock.NewController(t)
	mockVPC, manager := newMockVPCManager(t, ctrl)
	profiles := []vpcv1.InstanceProfile{
		makeVPCProfile("bx4-2x8", 2, 8, "amd64"),
		makeVPCProfile("bx2-2x8", 2, 8, "amd64"),
		makeVPCProfile("bx2d-4x16", 4, 16, "amd64"),
		makeVPCProfile("bz2-4x16", 4, 16, "s390x"),
	}
	for i := range profiles {
		profiles[i].AvailabilityClass = &vpcv1.InstanceProfileAvailabilityClassEnum{Values: []string{"standard", "spot"}}
	}
	setupListProfilesMock(mockVPC, profiles)
	pricing := mockpricing.NewMockProvider(ctrl)
	pricing.EXPECT().GetPrice(gomock.Any(), gomock.Any(), "us-south-1").DoAndReturn(func(_ context.Context, name, _ string) (float64, error) {
		return quote(name)
	}).AnyTimes()
	return &IBMInstanceTypeProvider{
		client:           &MockIBMClient{},
		pricingProvider:  pricing,
		vpcClientManager: manager,
		zonesCache:       map[string][]string{"us-south": {"us-south-1"}},
		zonesCacheTime:   map[string]time.Time{"us-south": time.Now()},
	}
}

func TestListExplicitProfileIntersectsPoolRequirements(t *testing.T) {
	provider := constrainedCatalog(t, func(string) (float64, error) { return 0.20, nil })
	class := &v1alpha1.IBMNodeClass{Spec: v1alpha1.IBMNodeClassSpec{InstanceProfile: "bx2-2x8"}}
	instanceTypes, err := provider.List(context.Background(), class)
	require.NoError(t, err)
	require.Len(t, instanceTypes, 1)
	require.Equal(t, "bx2-2x8", instanceTypes[0].Name)
	broadPool := scheduling.NewRequirements(scheduling.NewRequirement("karpenter-ibm.sh/instance-family", corev1.NodeSelectorOpIn, "bx2", "bx2d", "bx4"))
	require.NoError(t, broadPool.Compatible(instanceTypes[0].Requirements, scheduling.AllowUndefinedWellKnownLabels))
	conflictingPool := scheduling.NewRequirements(scheduling.NewRequirement(corev1.LabelInstanceTypeStable, corev1.NodeSelectorOpIn, "bx4-2x8"))
	require.Error(t, conflictingPool.Compatible(instanceTypes[0].Requirements, scheduling.AllowUndefinedWellKnownLabels))

	class.Spec.InstanceProfile = "bx4-2x8"
	instanceTypes, err = provider.List(context.Background(), class)
	require.NoError(t, err)
	require.Len(t, instanceTypes, 1)
	require.Equal(t, "bx4-2x8", instanceTypes[0].Name)
	class.Spec.InstanceProfile = "missing-2x8"
	instanceTypes, err = provider.List(context.Background(), class)
	require.NoError(t, err)
	require.Empty(t, instanceTypes)
	instanceTypes, err = provider.List(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, instanceTypes, 4)
}

func TestListAppliesDynamicClassRequirements(t *testing.T) {
	provider := constrainedCatalog(t, func(name string) (float64, error) {
		if name == "bx2-2x8" {
			return 0.10, nil
		}
		return 0.20, nil
	})
	class := &v1alpha1.IBMNodeClass{Spec: v1alpha1.IBMNodeClassSpec{InstanceRequirements: &v1alpha1.InstanceTypeRequirements{Architecture: "amd64", MinimumCPU: 4, MinimumMemory: 16}}}
	instanceTypes, err := provider.List(context.Background(), class)
	require.NoError(t, err)
	require.Len(t, instanceTypes, 1)
	require.Equal(t, "bx2d-4x16", instanceTypes[0].Name)

	class.Spec.InstanceRequirements = &v1alpha1.InstanceTypeRequirements{Architecture: "amd64", MaximumHourlyPrice: "0.15"}
	instanceTypes, err = provider.List(context.Background(), class)
	require.NoError(t, err)
	require.Len(t, instanceTypes, 3)
	onDemand := scheduling.NewRequirements(scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, karpv1.CapacityTypeOnDemand))
	var onDemandTypes []string
	for _, instanceType := range instanceTypes {
		for _, offering := range instanceType.Offerings {
			require.LessOrEqual(t, offering.Price, 0.15)
		}
		if len(instanceType.Offerings.Compatible(onDemand).Available()) != 0 {
			onDemandTypes = append(onDemandTypes, instanceType.Name)
		}
	}
	require.Equal(t, []string{"bx2-2x8"}, onDemandTypes)

	class.Spec.InstanceRequirements.MaximumHourlyPrice = "0"
	instanceTypes, err = provider.List(context.Background(), class)
	require.NoError(t, err)
	require.Empty(t, instanceTypes)
}

func TestPriceCapRequiresValidQuote(t *testing.T) {
	for _, test := range []struct {
		name  string
		price float64
		err   error
	}{
		{name: "quote unavailable", err: errors.New("pricing unavailable")},
		{name: "NaN", price: math.NaN()},
		{name: "infinite", price: math.Inf(1)},
		{name: "negative", price: -1},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := constrainedCatalog(t, func(string) (float64, error) { return test.price, test.err })
			class := &v1alpha1.IBMNodeClass{Spec: v1alpha1.IBMNodeClassSpec{InstanceRequirements: &v1alpha1.InstanceTypeRequirements{MaximumHourlyPrice: "10"}}}
			instanceTypes, err := provider.List(context.Background(), class)
			require.NoError(t, err)
			require.Empty(t, instanceTypes)
		})
	}
}

func TestOfferingsRequirePositiveFiniteQuotesWithoutPriceCap(t *testing.T) {
	for _, test := range []struct {
		name  string
		price float64
		err   error
	}{
		{name: "unavailable", err: errors.New("quote unavailable")},
		{name: "zero"}, {name: "negative", price: -1},
		{name: "NaN", price: math.NaN()}, {name: "infinite", price: math.Inf(1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := constrainedCatalog(t, func(string) (float64, error) { return test.price, test.err })
			instanceTypes, err := provider.List(context.Background(), nil)
			require.NoError(t, err)
			require.Len(t, instanceTypes, 4)
			for _, instanceType := range instanceTypes {
				require.Empty(t, instanceType.Offerings.Available())
			}
		})
	}
}

func TestRequirementsFilteringPreservesCallerAndUnfilteredCatalog(t *testing.T) {
	provider := constrainedCatalog(t, func(string) (float64, error) { return 0.20, nil })
	class := &v1alpha1.IBMNodeClass{Spec: v1alpha1.IBMNodeClassSpec{InstanceProfile: "bx4-2x8"}}
	requirements := &v1alpha1.InstanceTypeRequirements{Architecture: "amd64", MinimumCPU: 4}
	instanceTypes, err := provider.FilterInstanceTypes(context.Background(), requirements, class)
	require.NoError(t, err)
	require.Len(t, instanceTypes, 1)
	require.Equal(t, "bx2d-4x16", instanceTypes[0].Name)
	require.Equal(t, "bx4-2x8", class.Spec.InstanceProfile)
	require.Nil(t, class.Spec.InstanceRequirements)
	instanceTypes, err = provider.List(context.Background(), class)
	require.NoError(t, err)
	require.Len(t, instanceTypes, 1)
	require.Equal(t, "bx4-2x8", instanceTypes[0].Name)
	instanceTypes, err = provider.List(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, instanceTypes, 4)
}

func TestPriceFilteredCatalogDoesNotMutateOfferings(t *testing.T) {
	provider := &IBMInstanceTypeProvider{}
	original := makeInstanceType("bx2-2x8", 2000, 8*1024*1024*1024, "amd64")
	original.Offerings = cloudprovider.Offerings{{Price: 0.10}, {Price: 0.20}}
	filtered, err := provider.filterCatalog([]*cloudprovider.InstanceType{original}, &v1alpha1.InstanceTypeRequirements{MaximumHourlyPrice: "0.15"})
	require.NoError(t, err)
	require.Len(t, filtered, 1)
	require.Len(t, filtered[0].Offerings, 1)
	require.Len(t, original.Offerings, 2)
}

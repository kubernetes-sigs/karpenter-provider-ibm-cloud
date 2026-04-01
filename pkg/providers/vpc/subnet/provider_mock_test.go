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
package subnet

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	v1alpha1 "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cache"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	mock_ibm "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm/mock"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/utils/vpcclient"
)

func newTestProvider(ctrl *gomock.Controller) (*provider, *mock_ibm.MockvpcClientInterface) {
	mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)
	vpcClient := ibm.NewVPCClientWithMock(mockVPC)
	manager := vpcclient.NewManagerWithMockClient(vpcClient)
	p := &provider{
		client:           &ibm.Client{},
		subnetCache:      cache.New(5 * time.Minute),
		vpcClientManager: manager,
	}
	return p, mockVPC
}

func makeSubnet(id, zone, cidr, status string, total, available int64) vpcv1.Subnet {
	return vpcv1.Subnet{
		ID:                        &id,
		Zone:                      &vpcv1.ZoneReference{Name: &zone},
		Ipv4CIDRBlock:             &cidr,
		Status:                    &status,
		TotalIpv4AddressCount:     &total,
		AvailableIpv4AddressCount: &available,
	}
}

func okResponse() *core.DetailedResponse {
	return &core.DetailedResponse{StatusCode: 200}
}

func TestListSubnets_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	p, mockVPC := newTestProvider(ctrl)
	defer p.subnetCache.Stop()

	s1 := makeSubnet("subnet-1", "us-south-1", "10.0.0.0/24", "available", 256, 200)
	s2 := makeSubnet("subnet-2", "us-south-2", "10.0.1.0/24", "available", 128, 50)

	mockVPC.EXPECT().
		ListSubnetsWithContext(gomock.Any(), gomock.Not(gomock.Nil())).
		Return(&vpcv1.SubnetCollection{Subnets: []vpcv1.Subnet{s1, s2}}, okResponse(), nil).
		Times(1)

	ctx := context.Background()
	result, err := p.ListSubnets(ctx, "vpc-1")

	require.NoError(t, err)
	require.Len(t, result, 2)

	assert.Equal(t, "subnet-1", result[0].ID)
	assert.Equal(t, "us-south-1", result[0].Zone)
	assert.Equal(t, "10.0.0.0/24", result[0].CIDR)
	assert.Equal(t, "available", result[0].State)
	assert.Equal(t, int32(256), result[0].TotalIPCount)
	assert.Equal(t, int32(200), result[0].AvailableIPs)
	assert.Equal(t, int32(56), result[0].UsedIPCount)

	assert.Equal(t, "subnet-2", result[1].ID)
	assert.Equal(t, int32(50), result[1].AvailableIPs)
}

func TestListSubnets_APIError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	p, mockVPC := newTestProvider(ctrl)
	defer p.subnetCache.Stop()

	mockVPC.EXPECT().
		ListSubnetsWithContext(gomock.Any(), gomock.Not(gomock.Nil())).
		Return(nil, okResponse(), fmt.Errorf("service unavailable")).
		Times(1)

	ctx := context.Background()
	result, err := p.ListSubnets(ctx, "vpc-1")

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "listing subnets")
}

func TestListSubnets_Caching(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	p, mockVPC := newTestProvider(ctrl)
	defer p.subnetCache.Stop()

	s1 := makeSubnet("subnet-1", "us-south-1", "10.0.0.0/24", "available", 256, 200)

	mockVPC.EXPECT().
		ListSubnetsWithContext(gomock.Any(), gomock.Not(gomock.Nil())).
		Return(&vpcv1.SubnetCollection{Subnets: []vpcv1.Subnet{s1}}, okResponse(), nil).
		Times(1)

	ctx := context.Background()

	result1, err := p.ListSubnets(ctx, "vpc-1")
	require.NoError(t, err)
	require.Len(t, result1, 1)

	result2, err := p.ListSubnets(ctx, "vpc-1")
	require.NoError(t, err)
	require.Len(t, result2, 1)

	assert.Equal(t, result1[0].ID, result2[0].ID)
}

func TestGetSubnet_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	p, mockVPC := newTestProvider(ctrl)
	defer p.subnetCache.Stop()

	s := makeSubnet("subnet-42", "us-south-3", "10.1.0.0/24", "available", 512, 400)

	subnetID := "subnet-42"
	mockVPC.EXPECT().
		GetSubnetWithContext(gomock.Any(), &vpcv1.GetSubnetOptions{ID: &subnetID}).
		Return(&s, okResponse(), nil).
		Times(1)

	ctx := context.Background()
	result, err := p.GetSubnet(ctx, "subnet-42")

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "subnet-42", result.ID)
	assert.Equal(t, "us-south-3", result.Zone)
	assert.Equal(t, "10.1.0.0/24", result.CIDR)
	assert.Equal(t, "available", result.State)
	assert.Equal(t, int32(512), result.TotalIPCount)
	assert.Equal(t, int32(400), result.AvailableIPs)
	assert.Equal(t, int32(112), result.UsedIPCount)
}

func TestGetSubnet_APIError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	p, mockVPC := newTestProvider(ctrl)
	defer p.subnetCache.Stop()

	missingID := "subnet-missing"
	mockVPC.EXPECT().
		GetSubnetWithContext(gomock.Any(), &vpcv1.GetSubnetOptions{ID: &missingID}).
		Return(nil, okResponse(), fmt.Errorf("not found")).
		Times(1)

	ctx := context.Background()
	result, err := p.GetSubnet(ctx, "subnet-missing")

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "getting subnet")
}

func TestGetSubnet_Caching(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	p, mockVPC := newTestProvider(ctrl)
	defer p.subnetCache.Stop()

	s := makeSubnet("subnet-cached", "us-south-1", "10.2.0.0/24", "available", 256, 100)

	cachedID := "subnet-cached"
	mockVPC.EXPECT().
		GetSubnetWithContext(gomock.Any(), &vpcv1.GetSubnetOptions{ID: &cachedID}).
		Return(&s, okResponse(), nil).
		Times(1)

	ctx := context.Background()

	result1, err := p.GetSubnet(ctx, "subnet-cached")
	require.NoError(t, err)
	require.NotNil(t, result1)

	result2, err := p.GetSubnet(ctx, "subnet-cached")
	require.NoError(t, err)
	require.NotNil(t, result2)

	assert.Equal(t, result1.ID, result2.ID)
}

func TestSelectSubnets_Balanced(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	p, mockVPC := newTestProvider(ctrl)
	defer p.subnetCache.Stop()

	s1 := makeSubnet("subnet-z1", "us-south-1", "10.0.0.0/24", "available", 256, 200)
	s2 := makeSubnet("subnet-z2", "us-south-2", "10.0.1.0/24", "available", 256, 180)
	s3 := makeSubnet("subnet-z1b", "us-south-1", "10.0.2.0/24", "available", 256, 150)

	mockVPC.EXPECT().
		ListSubnetsWithContext(gomock.Any(), gomock.Not(gomock.Nil())).
		Return(&vpcv1.SubnetCollection{Subnets: []vpcv1.Subnet{s1, s2, s3}}, okResponse(), nil).
		Times(1)

	ctx := context.Background()
	strategy := &v1alpha1.PlacementStrategy{
		ZoneBalance: "Balanced",
	}

	result, err := p.SelectSubnets(ctx, "vpc-1", strategy)

	require.NoError(t, err)
	require.Len(t, result, 2, "Balanced should pick one subnet per zone")

	zones := map[string]bool{}
	for _, s := range result {
		zones[s.Zone] = true
	}
	assert.True(t, zones["us-south-1"])
	assert.True(t, zones["us-south-2"])
}

func TestSelectSubnets_AvailabilityFirst(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	p, mockVPC := newTestProvider(ctrl)
	defer p.subnetCache.Stop()

	s1 := makeSubnet("subnet-1", "us-south-1", "10.0.0.0/24", "available", 256, 200)
	s2 := makeSubnet("subnet-2", "us-south-2", "10.0.1.0/24", "available", 256, 180)
	s3 := makeSubnet("subnet-3", "us-south-1", "10.0.2.0/24", "available", 256, 150)

	mockVPC.EXPECT().
		ListSubnetsWithContext(gomock.Any(), gomock.Not(gomock.Nil())).
		Return(&vpcv1.SubnetCollection{Subnets: []vpcv1.Subnet{s1, s2, s3}}, okResponse(), nil).
		Times(1)

	ctx := context.Background()
	strategy := &v1alpha1.PlacementStrategy{
		ZoneBalance: "AvailabilityFirst",
	}

	result, err := p.SelectSubnets(ctx, "vpc-1", strategy)

	require.NoError(t, err)
	require.Len(t, result, 3, "AvailabilityFirst should return all eligible subnets")

	returnedIDs := map[string]bool{}
	for _, s := range result {
		returnedIDs[s.ID] = true
	}
	assert.True(t, returnedIDs["subnet-1"])
	assert.True(t, returnedIDs["subnet-2"])
	assert.True(t, returnedIDs["subnet-3"])
}

func TestSelectSubnets_CostOptimized(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	p, mockVPC := newTestProvider(ctrl)
	defer p.subnetCache.Stop()

	s1 := makeSubnet("subnet-z1", "us-south-1", "10.0.0.0/24", "available", 256, 200)
	s2 := makeSubnet("subnet-z2", "us-south-2", "10.0.1.0/24", "available", 256, 180)
	s3 := makeSubnet("subnet-z3", "us-south-3", "10.0.2.0/24", "available", 256, 150)

	mockVPC.EXPECT().
		ListSubnetsWithContext(gomock.Any(), gomock.Not(gomock.Nil())).
		Return(&vpcv1.SubnetCollection{Subnets: []vpcv1.Subnet{s1, s2, s3}}, okResponse(), nil).
		Times(1)

	ctx := context.Background()
	strategy := &v1alpha1.PlacementStrategy{
		ZoneBalance: "CostOptimized",
	}

	result, err := p.SelectSubnets(ctx, "vpc-1", strategy)

	require.NoError(t, err)
	require.NotEmpty(t, result, "CostOptimized should select at least one subnet")
	require.LessOrEqual(t, len(result), 2, "CostOptimized should select at most 2 zones")

	zones := map[string]bool{}
	for _, s := range result {
		zones[s.Zone] = true
	}
	assert.LessOrEqual(t, len(zones), 2)
	assert.True(t, zones["us-south-1"], "us-south-1 has most available IPs and should be selected")
}

func TestSelectSubnets_AllPending(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	p, mockVPC := newTestProvider(ctrl)
	defer p.subnetCache.Stop()

	s1 := makeSubnet("subnet-1", "us-south-1", "10.0.0.0/24", "pending", 256, 200)
	s2 := makeSubnet("subnet-2", "us-south-2", "10.0.1.0/24", "pending", 256, 180)

	mockVPC.EXPECT().
		ListSubnetsWithContext(gomock.Any(), gomock.Not(gomock.Nil())).
		Return(&vpcv1.SubnetCollection{Subnets: []vpcv1.Subnet{s1, s2}}, okResponse(), nil).
		Times(1)

	ctx := context.Background()
	strategy := &v1alpha1.PlacementStrategy{
		ZoneBalance: "Balanced",
	}

	result, err := p.SelectSubnets(ctx, "vpc-1", strategy)

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "no eligible subnets found")
}

func TestSelectSubnets_TagFiltering(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	p, _ := newTestProvider(ctrl)
	defer p.subnetCache.Stop()

	// Tags are populated during conversion; the SDK subnet type itself does not
	// carry user tags as map fields. The conversion produces an empty Tags map,
	// so we need to set tags on the SubnetInfo after conversion. Because
	// SelectSubnets calls ListSubnets internally (which calls the VPC API and
	// converts), the SubnetInfo.Tags will be empty maps from the API response.
	//
	// To exercise the tag filtering path we must ensure the cache is primed with
	// SubnetInfo values that already have the expected tags set.

	s1ID := "subnet-tagged"
	s1Zone := "us-south-1"
	s1CIDR := "10.0.0.0/24"
	s2ID := "subnet-untagged"
	s2Zone := "us-south-2"
	s2CIDR := "10.0.1.0/24"

	// Pre-populate the cache with tagged SubnetInfo entries so SelectSubnets
	// picks them up via ListSubnets's cache path.
	tagged := SubnetInfo{
		ID:           s1ID,
		Zone:         s1Zone,
		CIDR:         s1CIDR,
		State:        "available",
		TotalIPCount: 256,
		AvailableIPs: 200,
		UsedIPCount:  56,
		Tags:         map[string]string{"env": "prod"},
	}
	untagged := SubnetInfo{
		ID:           s2ID,
		Zone:         s2Zone,
		CIDR:         s2CIDR,
		State:        "available",
		TotalIPCount: 256,
		AvailableIPs: 180,
		UsedIPCount:  76,
		Tags:         map[string]string{},
	}

	p.subnetCache.Set("vpc-subnets:vpc-1", []SubnetInfo{tagged, untagged})

	ctx := context.Background()
	strategy := &v1alpha1.PlacementStrategy{
		ZoneBalance: "AvailabilityFirst",
		SubnetSelection: &v1alpha1.SubnetSelectionCriteria{
			RequiredTags: map[string]string{"env": "prod"},
		},
	}

	result, err := p.SelectSubnets(ctx, "vpc-1", strategy)

	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, s1ID, result[0].ID)
}

func TestSelectSubnets_MinimumAvailableIPs(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	p, mockVPC := newTestProvider(ctrl)
	defer p.subnetCache.Stop()

	s1 := makeSubnet("subnet-large", "us-south-1", "10.0.0.0/24", "available", 256, 200)
	s2 := makeSubnet("subnet-small", "us-south-2", "10.0.1.0/24", "available", 256, 5)

	mockVPC.EXPECT().
		ListSubnetsWithContext(gomock.Any(), gomock.Not(gomock.Nil())).
		Return(&vpcv1.SubnetCollection{Subnets: []vpcv1.Subnet{s1, s2}}, okResponse(), nil).
		Times(1)

	ctx := context.Background()
	strategy := &v1alpha1.PlacementStrategy{
		ZoneBalance: "AvailabilityFirst",
		SubnetSelection: &v1alpha1.SubnetSelectionCriteria{
			MinimumAvailableIPs: 50,
		},
	}

	result, err := p.SelectSubnets(ctx, "vpc-1", strategy)

	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, "subnet-large", result[0].ID)
}

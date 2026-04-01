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
	"fmt"
	"testing"
	"time"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"

	v1alpha1 "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	mock_ibm "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm/mock"
	mock_pricing "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/pricing/mock"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/utils/vpcclient"
)

// helpers

func strPtr(s string) *string { return &s }
func int64Ptr(i int64) *int64 { return &i }

func makeInstanceType(name string, cpuMillis, memBytes int64, arch string) *cloudprovider.InstanceType {
	return &cloudprovider.InstanceType{
		Name: name,
		Capacity: corev1.ResourceList{
			corev1.ResourceCPU:    *resource.NewMilliQuantity(cpuMillis, resource.DecimalSI),
			corev1.ResourceMemory: *resource.NewQuantity(memBytes, resource.BinarySI),
			corev1.ResourcePods:   *resource.NewQuantity(110, resource.DecimalSI),
		},
		Requirements: scheduling.NewRequirements(
			scheduling.NewRequirement(corev1.LabelInstanceTypeStable, corev1.NodeSelectorOpIn, name),
			scheduling.NewRequirement(corev1.LabelArchStable, corev1.NodeSelectorOpIn, arch),
		),
	}
}

func makeVPCProfile(name string, cpu, memGB int64, arch string) vpcv1.InstanceProfile {
	return vpcv1.InstanceProfile{
		Name: strPtr(name),
		VcpuCount: &vpcv1.InstanceProfileVcpu{
			Value: int64Ptr(cpu),
		},
		Memory: &vpcv1.InstanceProfileMemory{
			Value: int64Ptr(memGB),
		},
		VcpuArchitecture: &vpcv1.InstanceProfileVcpuArchitecture{
			Value: strPtr(arch),
		},
	}
}

func newMockVPCManager(t *testing.T, ctrl *gomock.Controller) (*mock_ibm.MockvpcClientInterface, *vpcclient.Manager) {
	t.Helper()
	mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)
	vpcClient := ibm.NewVPCClientWithMock(mockVPC)
	manager := vpcclient.NewManagerWithMockClient(vpcClient)
	return mockVPC, manager
}

func setupListProfilesMock(mockVPC *mock_ibm.MockvpcClientInterface, profiles []vpcv1.InstanceProfile) {
	mockVPC.EXPECT().
		ListInstanceProfilesWithContext(gomock.Any(), gomock.Any()).
		Return(&vpcv1.InstanceProfileCollection{Profiles: profiles},
			&core.DetailedResponse{StatusCode: 200}, nil).
		AnyTimes()
}

// isRetryableError tests

func TestIsRetryableError_StatusCodes(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		statusCode int
		want       bool
	}{
		{"500 internal server error", errors.New("server error"), 500, true},
		{"502 bad gateway", errors.New("bad gw"), 502, true},
		{"503 service unavailable", errors.New("unavailable"), 503, true},
		{"504 gateway timeout", errors.New("gw timeout"), 504, true},
		{"522 cloudflare timeout", errors.New("cf"), 522, true},
		{"524 cloudflare timeout", errors.New("cf timeout"), 524, true},
		{"429 rate limited", errors.New("throttled"), 429, true},
		{"200 success", errors.New("some error"), 200, false},
		{"400 bad request", errors.New("bad request"), 400, false},
		{"404 not found", errors.New("not found"), 404, false},
		{"401 unauthorized", errors.New("unauthorized"), 401, false},
		{"403 forbidden", errors.New("forbidden"), 403, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isRetryableError(tt.err, tt.statusCode))
		})
	}
}

func TestIsRetryableError_ErrorMessages(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"connection refused", errors.New("connection refused"), true},
		{"connection reset", errors.New("connection reset by peer"), true},
		{"connection timed out", errors.New("connection timed out"), true},
		{"temporary failure", errors.New("temporary failure in name resolution"), true},
		{"eof", errors.New("unexpected eof"), true},
		{"broken pipe", errors.New("write: broken pipe"), true},
		{"no such host", errors.New("dial tcp: lookup foo: no such host"), true},
		{"internal server error string", errors.New("internal server error"), true},
		{"service unavailable string", errors.New("service unavailable"), true},
		{"gateway timeout string", errors.New("gateway timeout"), true},
		{"bad gateway string", errors.New("bad gateway"), true},
		{"cloudflare string", errors.New("cloudflare error 520"), true},
		{"timeout string", errors.New("timeout awaiting response"), true},
		{"deadline exceeded string", errors.New("deadline exceeded"), true},
		{"non-retryable: invalid parameter", errors.New("invalid parameter"), false},
		{"non-retryable: permission denied", errors.New("permission denied"), false},
		{"non-retryable: authentication failed", errors.New("authentication failed"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isRetryableError(tt.err, 0))
		})
	}
}

func TestIsRetryableError_NilError(t *testing.T) {
	assert.False(t, isRetryableError(nil, 0))
	assert.False(t, isRetryableError(nil, 500))
}

func TestIsRetryableError_ContextDeadlineExceeded(t *testing.T) {
	assert.True(t, isRetryableError(context.DeadlineExceeded, 0))
	assert.True(t, isRetryableError(fmt.Errorf("wrapped: %w", context.DeadlineExceeded), 0))
}

func TestIsRetryableError_WrappedErrors(t *testing.T) {
	assert.True(t, isRetryableError(fmt.Errorf("vpc call failed: %w", errors.New("connection refused")), 0))
	assert.False(t, isRetryableError(fmt.Errorf("outer: %w", errors.New("some custom error")), 0))
}

// getInstanceFamily / getInstanceSize tests

func TestGetInstanceFamily_Mock(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"bx2-4x16", "bx2"},
		{"cx3d-8x20", "cx3d"},
		{"mx2-2x16", "mx2"},
		{"gx2-16x128x2", "gx2"},
		{"balanced", "balanced"},
		{"", "balanced"},
		{"a-b", "a"},
		{"-leading", "balanced"},
		{"onlyprefix", "onlyprefix"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, getInstanceFamily(tt.input))
		})
	}
}

func TestGetInstanceSize_Mock(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"bx2-4x16", "4x16"},
		{"cx3d-8x20", "8x20"},
		{"mx2-2x16", "2x16"},
		{"gx2-16x128x2", "16x128x2"},
		{"balanced", "small"},
		{"", "small"},
		{"a-b", "b"},
		{"-leading", "leading"},
		{"nodash", "small"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, getInstanceSize(tt.input))
		})
	}
}

// rankInstanceTypes tests (private method, uses ExtendedInstanceType directly)

func TestRankInstanceTypes_ByPrice(t *testing.T) {
	provider := &IBMInstanceTypeProvider{}

	types := []*ExtendedInstanceType{
		{
			InstanceType: makeInstanceType("expensive", 4000, 16*1024*1024*1024, "amd64"),
			Architecture: "amd64",
			Price:        1.00,
		},
		{
			InstanceType: makeInstanceType("cheap", 4000, 16*1024*1024*1024, "amd64"),
			Architecture: "amd64",
			Price:        0.10,
		},
		{
			InstanceType: makeInstanceType("mid", 4000, 16*1024*1024*1024, "amd64"),
			Architecture: "amd64",
			Price:        0.50,
		},
	}

	ranked := provider.rankInstanceTypes(types)
	require.Len(t, ranked, 3)
	assert.Equal(t, "cheap", ranked[0].Name)
	assert.Equal(t, "mid", ranked[1].Name)
	assert.Equal(t, "expensive", ranked[2].Name)
}

func TestRankInstanceTypes_NoPricing_FallsBackToResourceSize(t *testing.T) {
	provider := &IBMInstanceTypeProvider{}

	types := []*ExtendedInstanceType{
		{
			InstanceType: makeInstanceType("large", 8000, 32*1024*1024*1024, "amd64"),
			Architecture: "amd64",
			Price:        0,
		},
		{
			InstanceType: makeInstanceType("small", 2000, 8*1024*1024*1024, "amd64"),
			Architecture: "amd64",
			Price:        0,
		},
	}

	ranked := provider.rankInstanceTypes(types)
	require.Len(t, ranked, 2)
	assert.Equal(t, "small", ranked[0].Name)
	assert.Equal(t, "large", ranked[1].Name)
}

func TestRankInstanceTypes_MixedPricingAndNoPricing(t *testing.T) {
	provider := &IBMInstanceTypeProvider{}

	types := []*ExtendedInstanceType{
		{
			InstanceType: makeInstanceType("no-price", 4000, 8*1024*1024*1024, "amd64"),
			Architecture: "amd64",
			Price:        0,
		},
		{
			InstanceType: makeInstanceType("has-price", 4000, 8*1024*1024*1024, "amd64"),
			Architecture: "amd64",
			Price:        0.10,
		},
	}

	ranked := provider.rankInstanceTypes(types)
	require.Len(t, ranked, 2)
	// Score for has-price: (0.10/4 + 0.10/~8.59)/2 ~ 0.0183
	// Score for no-price: 4 + ~8.59 = ~12.59
	// has-price should rank first
	assert.Equal(t, "has-price", ranked[0].Name)
}

// RankInstanceTypes public method tests (client=nil path)

func TestRankInstanceTypes_NilPricingProvider(t *testing.T) {
	provider := &IBMInstanceTypeProvider{
		pricingProvider: nil,
	}

	input := []*cloudprovider.InstanceType{
		makeInstanceType("bx2-8x32", 8000, 32*1024*1024*1024, "amd64"),
		makeInstanceType("bx2-2x8", 2000, 8*1024*1024*1024, "amd64"),
	}

	result := provider.RankInstanceTypes(input)
	require.Len(t, result, 2)
	assert.Equal(t, "bx2-2x8", result[0].Name)
	assert.Equal(t, "bx2-8x32", result[1].Name)
}

func TestRankInstanceTypes_EmptyInput(t *testing.T) {
	provider := &IBMInstanceTypeProvider{}
	assert.Empty(t, provider.RankInstanceTypes(nil))
	assert.Empty(t, provider.RankInstanceTypes([]*cloudprovider.InstanceType{}))
}

func TestRankInstanceTypes_SingleInstance(t *testing.T) {
	provider := &IBMInstanceTypeProvider{}
	input := []*cloudprovider.InstanceType{
		makeInstanceType("bx2-4x16", 4000, 16*1024*1024*1024, "amd64"),
	}

	result := provider.RankInstanceTypes(input)
	require.Len(t, result, 1)
	assert.Equal(t, "bx2-4x16", result[0].Name)
}

func TestRankInstanceTypes_ClientNilWithPricingProvider(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockPricing := mock_pricing.NewMockProvider(ctrl)
	// GetPrice should not be called because client is nil
	mockPricing.EXPECT().GetPrice(gomock.Any(), gomock.Any(), gomock.Any()).Return(0.15, nil).Times(0)

	provider := &IBMInstanceTypeProvider{
		client:          nil,
		pricingProvider: mockPricing,
	}

	input := []*cloudprovider.InstanceType{
		makeInstanceType("bx2-4x16", 4000, 16*1024*1024*1024, "amd64"),
		makeInstanceType("bx2-2x8", 2000, 8*1024*1024*1024, "amd64"),
	}

	result := provider.RankInstanceTypes(input)
	require.Len(t, result, 2)
	// client is nil so all prices default to 0.0, ranking by resource size
	assert.Equal(t, "bx2-2x8", result[0].Name)
	assert.Equal(t, "bx2-4x16", result[1].Name)
}

func TestRankInstanceTypes_EqualScores_PreservesAll(t *testing.T) {
	provider := &IBMInstanceTypeProvider{}

	input := []*cloudprovider.InstanceType{
		makeInstanceType("bx2-2x8", 2000, 8*1024*1024*1024, "amd64"),
		makeInstanceType("cx2-2x8", 2000, 8*1024*1024*1024, "amd64"),
	}

	result := provider.RankInstanceTypes(input)
	require.Len(t, result, 2)
	names := []string{result[0].Name, result[1].Name}
	assert.Contains(t, names, "bx2-2x8")
	assert.Contains(t, names, "cx2-2x8")
}

// calculateInstanceTypeScore edge case

func TestCalculateInstanceTypeScore_ZeroResources(t *testing.T) {
	it := &ExtendedInstanceType{
		InstanceType: &cloudprovider.InstanceType{
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("0"),
				corev1.ResourceMemory: resource.MustParse("0"),
			},
		},
		Price: 0,
	}
	assert.Equal(t, 0.0, calculateInstanceTypeScore(it))
}

// getArchitecture edge case

func TestGetArchitecture_MultipleValues(t *testing.T) {
	it := &cloudprovider.InstanceType{
		Requirements: scheduling.NewRequirements(
			scheduling.NewRequirement(corev1.LabelArchStable, corev1.NodeSelectorOpIn, "s390x", "amd64"),
		),
	}
	arch := getArchitecture(it)
	assert.Contains(t, []string{"s390x", "amd64"}, arch)
}

// convertVPCProfileToInstanceType validation tests

func TestConvertVPCProfileToInstanceType_NilName(t *testing.T) {
	provider := &IBMInstanceTypeProvider{}
	_, err := provider.convertVPCProfileToInstanceType(context.Background(), vpcv1.InstanceProfile{Name: nil}, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "instance profile name is nil")
}

func TestConvertVPCProfileToInstanceType_EmptyName(t *testing.T) {
	provider := &IBMInstanceTypeProvider{}
	empty := ""
	_, err := provider.convertVPCProfileToInstanceType(context.Background(), vpcv1.InstanceProfile{Name: &empty}, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "empty name")
}

func TestConvertVPCProfileToInstanceType_NilCPU(t *testing.T) {
	provider := &IBMInstanceTypeProvider{}
	profile := vpcv1.InstanceProfile{
		Name:      strPtr("bx2-4x16"),
		VcpuCount: nil,
		Memory:    &vpcv1.InstanceProfileMemory{Value: int64Ptr(16)},
	}
	_, err := provider.convertVPCProfileToInstanceType(context.Background(), profile, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no CPU count")
}

func TestConvertVPCProfileToInstanceType_NilMemory(t *testing.T) {
	provider := &IBMInstanceTypeProvider{}
	profile := vpcv1.InstanceProfile{
		Name:      strPtr("bx2-4x16"),
		VcpuCount: &vpcv1.InstanceProfileVcpu{Value: int64Ptr(4)},
		Memory:    nil,
	}
	_, err := provider.convertVPCProfileToInstanceType(context.Background(), profile, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no memory")
}

func TestConvertVPCProfileToInstanceType_NilClient(t *testing.T) {
	provider := &IBMInstanceTypeProvider{}
	profile := makeVPCProfile("bx2-4x16", 4, 16, "amd64")
	_, err := provider.convertVPCProfileToInstanceType(context.Background(), profile, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "IBM client not initialized")
}

// VPC mock integration tests (listFromVPC, Get, List, FilterInstanceTypes)

func TestListFromVPC_ConversionFailsWithoutClient(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockVPC, manager := newMockVPCManager(t, ctrl)
	setupListProfilesMock(mockVPC, []vpcv1.InstanceProfile{
		makeVPCProfile("bx2-4x16", 4, 16, "amd64"),
		makeVPCProfile("cx2-2x4", 2, 4, "amd64"),
	})

	provider := &IBMInstanceTypeProvider{
		vpcClientManager: manager,
		zonesCache:       map[string][]string{"us-south": {"us-south-1", "us-south-2"}},
		zonesCacheTime:   map[string]time.Time{"us-south": time.Now()},
	}

	// client is nil so convertVPCProfileToInstanceType fails for every profile,
	// resulting in zero successful conversions and an error return.
	_, err := provider.listFromVPC(context.Background(), nil)
	assert.Error(t, err)
}

func TestListFromVPC_VPCReturnsNonRetryableError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockVPC, manager := newMockVPCManager(t, ctrl)
	mockVPC.EXPECT().
		ListInstanceProfilesWithContext(gomock.Any(), gomock.Any()).
		Return(nil, &core.DetailedResponse{StatusCode: 403}, errors.New("forbidden")).
		AnyTimes()

	provider := &IBMInstanceTypeProvider{
		vpcClientManager: manager,
	}

	_, err := provider.listFromVPC(context.Background(), nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "listing VPC instance profiles")
}

func TestListFromVPC_VPCReturnsRetryableError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockVPC, manager := newMockVPCManager(t, ctrl)
	mockVPC.EXPECT().
		ListInstanceProfilesWithContext(gomock.Any(), gomock.Any()).
		Return(nil, &core.DetailedResponse{StatusCode: 503}, errors.New("service unavailable")).
		AnyTimes()

	provider := &IBMInstanceTypeProvider{
		vpcClientManager: manager,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err := provider.listFromVPC(ctx, nil)
	assert.Error(t, err)
}

func TestListFromVPC_NilProfileCollection(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockVPC, manager := newMockVPCManager(t, ctrl)
	mockVPC.EXPECT().
		ListInstanceProfilesWithContext(gomock.Any(), gomock.Any()).
		Return(&vpcv1.InstanceProfileCollection{Profiles: nil},
			&core.DetailedResponse{StatusCode: 200}, nil).
		AnyTimes()

	provider := &IBMInstanceTypeProvider{
		vpcClientManager: manager,
	}

	_, err := provider.listFromVPC(context.Background(), nil)
	assert.Error(t, err)
}

func TestGet_NilClient(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	_, manager := newMockVPCManager(t, ctrl)

	provider := &IBMInstanceTypeProvider{
		client:           nil,
		vpcClientManager: manager,
	}

	_, err := provider.Get(context.Background(), "bx2-4x16", nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "IBM client not initialized")
}

func TestFilterInstanceTypes_NilClientMock(t *testing.T) {
	provider := &IBMInstanceTypeProvider{client: nil}

	requirements := &v1alpha1.InstanceTypeRequirements{
		Architecture:  "amd64",
		MinimumCPU:    4,
		MinimumMemory: 16,
	}

	_, err := provider.FilterInstanceTypes(context.Background(), requirements, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "IBM client not initialized")
}

func TestList_NilClient(t *testing.T) {
	provider := &IBMInstanceTypeProvider{client: nil}
	_, err := provider.List(context.Background(), nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "IBM client not initialized")
}

// getZonesForRegion cache tests

func TestGetZonesForRegion_UsesCache(t *testing.T) {
	provider := &IBMInstanceTypeProvider{
		zonesCache:     map[string][]string{"us-south": {"us-south-1", "us-south-2", "us-south-3"}},
		zonesCacheTime: map[string]time.Time{"us-south": time.Now()},
	}

	zones, err := provider.getZonesForRegion(context.Background(), "us-south")
	require.NoError(t, err)
	assert.Equal(t, []string{"us-south-1", "us-south-2", "us-south-3"}, zones)
}

// Skipped: getZonesForRegion with nil client panics (nil pointer in GetVPCClient).
// That's an initialization bug, not a testable error path.

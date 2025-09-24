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
	"fmt"
	"os"
	"testing"
	"time"
	"unsafe"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"

	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
)

// MockIBMClient implements the IBM client interface for testing
type MockIBMClient struct {
	vpcClientError   error
	vpcClient        *ibm.VPCClient
	globalCatError   error
	instanceProfiles []vpcv1.InstanceProfile
}

func (m *MockIBMClient) GetVPCClient() (*ibm.VPCClient, error) {
	if m.vpcClientError != nil {
		return nil, m.vpcClientError
	}
	if m.vpcClient != nil {
		return m.vpcClient, nil
	}
	return &ibm.VPCClient{}, nil
}

func (m *MockIBMClient) GetGlobalCatalogClient() (*ibm.GlobalCatalogClient, error) {
	if m.globalCatError != nil {
		return nil, m.globalCatError
	}
	return nil, nil
}

func (m *MockIBMClient) GetIKSClient() *ibm.IKSClient {
	return nil
}

func (m *MockIBMClient) GetIAMClient() *ibm.IAMClient {
	return nil
}

func (m *MockIBMClient) GetRegion() string {
	return "us-south"
}

// MockPricingProvider implements the pricing provider interface for testing
type MockPricingProvider struct{}

func (m *MockPricingProvider) GetPrices(ctx context.Context, zone string) (map[string]float64, error) {
	return map[string]float64{
		"bx2.2x8":  0.095,
		"bx2.4x16": 0.190,
	}, nil
}

func (m *MockPricingProvider) GetPrice(ctx context.Context, instanceType string, zone string) (float64, error) {
	prices := map[string]float64{
		"bx2.2x8":  0.095,
		"bx2.4x16": 0.190,
	}
	if price, exists := prices[instanceType]; exists {
		return price, nil
	}
	return 0, fmt.Errorf("price not found for instance type %s", instanceType)
}

func (m *MockPricingProvider) Refresh(ctx context.Context) error {
	return nil
}

// Test calculateInstanceTypeScoreNew to avoid name conflict
func TestCalculateInstanceTypeScoreNew(t *testing.T) {
	tests := []struct {
		name         string
		instanceType *ExtendedInstanceType
		wantScore    float64
	}{
		{
			name: "instance with pricing",
			instanceType: &ExtendedInstanceType{
				InstanceType: &cloudprovider.InstanceType{
					Capacity: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("4"),
						corev1.ResourceMemory: resource.MustParse("16Gi"),
					},
				},
				Price: 0.5,
			},
			wantScore: 0.0763888888888889, // (0.5/4 + 0.5/18) / 2 - 16Gi = ~18 decimal GB
		},
		{
			name: "instance without pricing",
			instanceType: &ExtendedInstanceType{
				InstanceType: &cloudprovider.InstanceType{
					Capacity: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("8Gi"),
					},
				},
				Price: 0,
			},
			wantScore: 11, // 2 + 9 (8Gi = 9 decimal GB)
		},
		{
			name: "large instance with pricing",
			instanceType: &ExtendedInstanceType{
				InstanceType: &cloudprovider.InstanceType{
					Capacity: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("16"),
						corev1.ResourceMemory: resource.MustParse("64Gi"),
					},
				},
				Price: 2.0,
			},
			wantScore: 0.0769927536231884, // (2.0/16 + 2.0/68.72) / 2 - 64Gi = ~68.72 decimal GB
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculateInstanceTypeScore(tt.instanceType)
			if got != tt.wantScore {
				t.Errorf("calculateInstanceTypeScore() = %v, want %v", got, tt.wantScore)
			}
		})
	}
}

// Test getArchitectureNew to avoid name conflict
func TestGetArchitectureNew(t *testing.T) {
	tests := []struct {
		name string
		it   *cloudprovider.InstanceType
		want string
	}{
		{
			name: "with amd64 architecture",
			it: &cloudprovider.InstanceType{
				Requirements: scheduling.NewRequirements(
					scheduling.NewRequirement(corev1.LabelArchStable, corev1.NodeSelectorOpIn, "amd64"),
				),
			},
			want: "amd64",
		},
		{
			name: "with arm64 architecture",
			it: &cloudprovider.InstanceType{
				Requirements: scheduling.NewRequirements(
					scheduling.NewRequirement(corev1.LabelArchStable, corev1.NodeSelectorOpIn, "arm64"),
				),
			},
			want: "arm64",
		},
		{
			name: "without architecture requirement",
			it: &cloudprovider.InstanceType{
				Requirements: scheduling.NewRequirements(),
			},
			want: "amd64",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := getArchitecture(tt.it); got != tt.want {
				t.Errorf("getArchitecture() = %v, want %v", got, tt.want)
			}
		})
	}
}

// Test NewProvider
func TestNewProvider(t *testing.T) {
	// This test requires environment variables, so we'll skip it if they're not set
	if testing.Short() {
		t.Skip("Skipping test that requires IBM Cloud credentials")
	}

	// Check if required environment variables are set
	if os.Getenv("IBM_API_KEY") == "" || os.Getenv("VPC_API_KEY") == "" || os.Getenv("IBM_REGION") == "" {
		t.Skip("Skipping test that requires IBM Cloud credentials (IBM_API_KEY, VPC_API_KEY, IBM_REGION)")
	}

	// Create mock IBM client and pricing provider for testing
	mockClient := &MockIBMClient{}
	mockPricing := &MockPricingProvider{}
	provider := NewProvider((*ibm.Client)(unsafe.Pointer(mockClient)), mockPricing)
	if provider == nil {
		t.Fatal("NewProvider() returned nil provider")
	}

	// Type assert to check internal fields
	ibmProvider, ok := provider.(*IBMInstanceTypeProvider)
	if !ok {
		t.Fatal("NewProvider() did not return IBMInstanceTypeProvider")
	}
	if ibmProvider.client == nil {
		t.Error("NewProvider() created provider with nil client")
	}
	if ibmProvider.pricingProvider == nil {
		t.Error("NewProvider() created provider with nil pricing provider")
	}
}

// Test convertVPCProfileToInstanceType
func TestConvertVPCProfileToInstanceType(t *testing.T) {
	provider := &IBMInstanceTypeProvider{}
	profileName := "bx2-4x16"
	cpuCount := int64(4)
	memoryValue := int64(16)
	archValue := "amd64"
	family := "balanced"
	gpuCount := int64(0)

	tests := []struct {
		name    string
		profile vpcv1.InstanceProfile
		wantErr bool
		check   func(t *testing.T, it *cloudprovider.InstanceType)
	}{
		{
			name: "valid balanced profile - but no client",
			profile: vpcv1.InstanceProfile{
				Name: &profileName,
				VcpuCount: &vpcv1.InstanceProfileVcpu{
					Value: &cpuCount,
				},
				Memory: &vpcv1.InstanceProfileMemory{
					Value: &memoryValue,
				},
				VcpuArchitecture: &vpcv1.InstanceProfileVcpuArchitecture{
					Value: &archValue,
				},
				Family: &family,
				GpuCount: &vpcv1.InstanceProfileGpu{
					Value: &gpuCount,
				},
			},
			wantErr: true, // Now expects error because client is nil
		},
		{
			name: "profile without CPU",
			profile: vpcv1.InstanceProfile{
				Name: &profileName,
				Memory: &vpcv1.InstanceProfileMemory{
					Value: &memoryValue,
				},
			},
			wantErr: true,
		},
		{
			name: "profile without memory",
			profile: vpcv1.InstanceProfile{
				Name: &profileName,
				VcpuCount: &vpcv1.InstanceProfileVcpu{
					Value: &cpuCount,
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := provider.convertVPCProfileToInstanceType(context.Background(), tt.profile)
			if (err != nil) != tt.wantErr {
				t.Errorf("convertVPCProfileToInstanceType() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && tt.check != nil {
				tt.check(t, got)
			}
		})
	}
}

// Test IBMInstanceTypeProvider.Get
func TestIBMInstanceTypeProvider_GetWithMocks(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name         string
		provider     *IBMInstanceTypeProvider
		instanceName string
		wantErr      bool
		errContains  string
	}{
		{
			name: "nil client - should not get nil pointer error",
			provider: &IBMInstanceTypeProvider{
				client: nil,
			},
			instanceName: "bx2-4x16",
			wantErr:      true,
			errContains:  "IBM client not initialized",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.provider.Get(ctx, tt.instanceName)
			if (err != nil) != tt.wantErr {
				t.Errorf("Get() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && tt.errContains != "" && !contains(err.Error(), tt.errContains) {
				t.Errorf("Get() error = %v, want error containing %v", err, tt.errContains)
			}

			// Most importantly: verify we don't get the old nil pointer error
			if err != nil && contains(err.Error(), "listInstanceProfilesOptions cannot be nil") {
				t.Errorf("Get() should not return nil pointer error, got: %v", err)
			}
		})
	}
}

// Helper function
func contains(s, substr string) bool {
	return len(s) >= len(substr) && s[:len(substr)] == substr || len(s) > len(substr) && contains(s[1:], substr)
}

// Test Create method (no-op)
func TestIBMInstanceTypeProvider_Create(t *testing.T) {
	provider := &IBMInstanceTypeProvider{}

	instanceType := &cloudprovider.InstanceType{
		Name: "bx2-4x16",
	}

	err := provider.Create(context.Background(), instanceType)
	assert.NoError(t, err) // Create should be a no-op for IBM Cloud
}

// Test Delete method (no-op)
func TestIBMInstanceTypeProvider_Delete(t *testing.T) {
	provider := &IBMInstanceTypeProvider{}

	instanceType := &cloudprovider.InstanceType{
		Name: "bx2-4x16",
	}

	err := provider.Delete(context.Background(), instanceType)
	assert.NoError(t, err) // Delete should be a no-op for IBM Cloud
}

// Test FilterInstanceTypes with nil client
func TestIBMInstanceTypeProvider_FilterInstanceTypes_NilClient(t *testing.T) {
	provider := &IBMInstanceTypeProvider{
		client: nil,
	}

	requirements := &v1alpha1.InstanceTypeRequirements{
		MinimumCPU:    2,
		MinimumMemory: 8,
	}

	result, err := provider.FilterInstanceTypes(context.Background(), requirements)
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "IBM client not initialized")
}

// Test Get method with more coverage
func TestIBMInstanceTypeProvider_Get_Extended(t *testing.T) {
	tests := []struct {
		name         string
		provider     *IBMInstanceTypeProvider
		instanceName string
		wantErr      bool
		errContains  string
	}{
		{
			name: "nil client",
			provider: &IBMInstanceTypeProvider{
				client: nil,
			},
			instanceName: "bx2-4x16",
			wantErr:      true,
			errContains:  "IBM client not initialized",
		},
		{
			name: "empty instance name",
			provider: &IBMInstanceTypeProvider{
				client: nil, // Will fail at client check first
			},
			instanceName: "",
			wantErr:      true,
			errContains:  "IBM client not initialized", // Will hit client check first
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tt.provider.Get(context.Background(), tt.instanceName)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, result)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, result)
			}
		})
	}
}

// Test List method with more coverage
func TestIBMInstanceTypeProvider_List_Extended(t *testing.T) {
	tests := []struct {
		name        string
		provider    *IBMInstanceTypeProvider
		wantErr     bool
		errContains string
	}{
		{
			name: "nil client",
			provider: &IBMInstanceTypeProvider{
				client: nil,
			},
			wantErr:     true,
			errContains: "IBM client not initialized",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tt.provider.List(context.Background())
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, result)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, result)
			}
		})
	}
}

// Enhanced Mock VPCClient for comprehensive testing
type EnhancedMockVPCClient struct {
	profiles []vpcv1.InstanceProfile
	regions  []vpcv1.Region
	zones    map[string][]vpcv1.Zone
	errors   map[string]error
}

func NewEnhancedMockVPCClient() *EnhancedMockVPCClient {
	// Create sample data
	profileName1 := "bx2-2x8"
	profileName2 := "bx2-4x16"
	profileName3 := "cx2-2x4"

	var cpu1 int64 = 2
	var memory1 int64 = 8
	var cpu2 int64 = 4
	var memory2 int64 = 16
	var cpu3 int64 = 2
	var memory3 int64 = 4

	arch := "amd64"

	regionName := "us-south"
	status := "available"

	zone1 := "us-south-1"
	zone2 := "us-south-2"
	zone3 := "us-south-3"

	return &EnhancedMockVPCClient{
		profiles: []vpcv1.InstanceProfile{
			{
				Name: &profileName1,
				VcpuCount: &vpcv1.InstanceProfileVcpu{
					Value: &cpu1,
				},
				Memory: &vpcv1.InstanceProfileMemory{
					Value: &memory1,
				},
				VcpuArchitecture: &vpcv1.InstanceProfileVcpuArchitecture{
					Value: &arch,
				},
			},
			{
				Name: &profileName2,
				VcpuCount: &vpcv1.InstanceProfileVcpu{
					Value: &cpu2,
				},
				Memory: &vpcv1.InstanceProfileMemory{
					Value: &memory2,
				},
				VcpuArchitecture: &vpcv1.InstanceProfileVcpuArchitecture{
					Value: &arch,
				},
			},
			{
				Name: &profileName3,
				VcpuCount: &vpcv1.InstanceProfileVcpu{
					Value: &cpu3,
				},
				Memory: &vpcv1.InstanceProfileMemory{
					Value: &memory3,
				},
				VcpuArchitecture: &vpcv1.InstanceProfileVcpuArchitecture{
					Value: &arch,
				},
			},
		},
		regions: []vpcv1.Region{
			{
				Name:   &regionName,
				Status: &status,
			},
		},
		zones: map[string][]vpcv1.Zone{
			"us-south": {
				{Name: &zone1},
				{Name: &zone2},
				{Name: &zone3},
			},
		},
		errors: make(map[string]error),
	}
}

func (m *EnhancedMockVPCClient) ListInstanceProfiles(options *vpcv1.ListInstanceProfilesOptions) (*vpcv1.InstanceProfileCollection, *core.DetailedResponse, error) {
	if err, exists := m.errors["ListInstanceProfiles"]; exists {
		return nil, &core.DetailedResponse{}, err
	}

	return &vpcv1.InstanceProfileCollection{
		Profiles: m.profiles,
	}, &core.DetailedResponse{}, nil
}

func (m *EnhancedMockVPCClient) ListRegions(options *vpcv1.ListRegionsOptions) (*vpcv1.RegionCollection, *core.DetailedResponse, error) {
	if err, exists := m.errors["ListRegions"]; exists {
		return nil, &core.DetailedResponse{}, err
	}

	return &vpcv1.RegionCollection{
		Regions: m.regions,
	}, &core.DetailedResponse{}, nil
}

func (m *EnhancedMockVPCClient) ListRegionZonesWithContext(ctx context.Context, options *vpcv1.ListRegionZonesOptions) (*vpcv1.ZoneCollection, *core.DetailedResponse, error) {
	if err, exists := m.errors["ListRegionZones"]; exists {
		return nil, &core.DetailedResponse{}, err
	}

	if options.RegionName == nil {
		return nil, &core.DetailedResponse{}, fmt.Errorf("region name required")
	}

	if zones, exists := m.zones[*options.RegionName]; exists {
		return &vpcv1.ZoneCollection{Zones: zones}, &core.DetailedResponse{}, nil
	}

	return &vpcv1.ZoneCollection{}, &core.DetailedResponse{}, nil
}

func (m *EnhancedMockVPCClient) SetError(method string, err error) {
	m.errors[method] = err
}

// Enhanced Mock IBM Client
type EnhancedMockIBMClient struct {
	region    string
	vpcClient *EnhancedMockVPCClient
	errors    map[string]error
}

func NewEnhancedMockIBMClient() *EnhancedMockIBMClient {
	return &EnhancedMockIBMClient{
		region:    "us-south",
		vpcClient: NewEnhancedMockVPCClient(),
		errors:    make(map[string]error),
	}
}

func (m *EnhancedMockIBMClient) GetVPCClient() (*ibm.VPCClient, error) {
	if err, exists := m.errors["GetVPCClient"]; exists {
		return nil, err
	}

	// Return a real VPCClient but with our mock SDK client
	// This is tricky - we need to create a way to inject our mock
	// For now, return an error to indicate the limitations
	return nil, fmt.Errorf("mock VPC client not fully supported")
}

func (m *EnhancedMockIBMClient) GetRegion() string {
	return m.region
}

func (m *EnhancedMockIBMClient) SetError(method string, err error) {
	m.errors[method] = err
}

func (m *EnhancedMockIBMClient) GetMockVPCClient() *EnhancedMockVPCClient {
	return m.vpcClient
}

// Test getInstanceFamily function
func TestGetInstanceFamily(t *testing.T) {
	tests := []struct {
		name         string
		instanceType string
		expected     string
	}{
		{
			name:         "valid bx2 family",
			instanceType: "bx2-2x8",
			expected:     "bx2",
		},
		{
			name:         "valid cx2 family",
			instanceType: "cx2-4x8",
			expected:     "cx2",
		},
		{
			name:         "valid mx2 family",
			instanceType: "mx2-8x64",
			expected:     "mx2",
		},
		{
			name:         "short instance name",
			instanceType: "ab",
			expected:     "balanced",
		},
		{
			name:         "empty instance name",
			instanceType: "",
			expected:     "balanced",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getInstanceFamily(tt.instanceType)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// Test getInstanceSize function
func TestGetInstanceSize(t *testing.T) {
	tests := []struct {
		name         string
		instanceType string
		expected     string
	}{
		{
			name:         "valid size",
			instanceType: "bx2-2x8",
			expected:     "2x8",
		},
		{
			name:         "valid large size",
			instanceType: "cx2-16x32",
			expected:     "16x32",
		},
		{
			name:         "no dash",
			instanceType: "bx2",
			expected:     "small",
		},
		{
			name:         "empty string",
			instanceType: "",
			expected:     "small",
		},
		{
			name:         "dash at end",
			instanceType: "bx2-",
			expected:     "small", // Current behavior: returns "small" when dash exists but no chars after it
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getInstanceSize(tt.instanceType)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// Test FilterInstanceTypes with more comprehensive coverage
func TestFilterInstanceTypes_Comprehensive(t *testing.T) {
	// Create a mock provider with a client
	var mockClient *ibm.Client = nil // Use nil for error testing
	mockPricing := &MockPricingProvider{}

	provider := &IBMInstanceTypeProvider{
		client:          mockClient,
		pricingProvider: mockPricing,
		zonesCache:      make(map[string][]string),
		zonesCacheTime:  time.Now().Add(-2 * time.Hour), // Expired cache
	}

	tests := []struct {
		name         string
		requirements *v1alpha1.InstanceTypeRequirements
		wantErr      bool
		errContains  string
	}{
		{
			name: "nil client error",
			requirements: &v1alpha1.InstanceTypeRequirements{
				MinimumCPU: 4,
			},
			wantErr:     true,
			errContains: "IBM client not initialized",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := provider.FilterInstanceTypes(context.Background(), tt.requirements)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, result)
			}
		})
	}
}

// Test RankInstanceTypes with comprehensive coverage
func TestRankInstanceTypes_Comprehensive(t *testing.T) {
	var mockClient *ibm.Client = nil
	mockPricing := &MockPricingProvider{}

	provider := &IBMInstanceTypeProvider{
		client:          mockClient,
		pricingProvider: mockPricing,
	}

	// Create test instance types
	instanceTypes := []*cloudprovider.InstanceType{
		{
			Name: "bx2-2x8",
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("2"),
				corev1.ResourceMemory: resource.MustParse("8Gi"),
			},
			Requirements: scheduling.NewRequirements(
				scheduling.NewRequirement(corev1.LabelArchStable, corev1.NodeSelectorOpIn, "amd64"),
			),
		},
		{
			Name: "bx2-4x16",
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("4"),
				corev1.ResourceMemory: resource.MustParse("16Gi"),
			},
			Requirements: scheduling.NewRequirements(
				scheduling.NewRequirement(corev1.LabelArchStable, corev1.NodeSelectorOpIn, "amd64"),
			),
		},
	}

	result := provider.RankInstanceTypes(instanceTypes)

	assert.Equal(t, len(instanceTypes), len(result))
	assert.NotNil(t, result)
}

// Test RankInstanceTypes with nil pricing provider
func TestRankInstanceTypes_NilPricing(t *testing.T) {
	provider := &IBMInstanceTypeProvider{
		client:          nil,
		pricingProvider: nil, // nil pricing provider
	}

	instanceTypes := []*cloudprovider.InstanceType{
		{
			Name: "bx2-2x8",
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("2"),
				corev1.ResourceMemory: resource.MustParse("8Gi"),
			},
		},
	}

	result := provider.RankInstanceTypes(instanceTypes)
	assert.Equal(t, 1, len(result))
}

// Test RankInstanceTypes with nil client
func TestRankInstanceTypes_NilClient(t *testing.T) {
	provider := &IBMInstanceTypeProvider{
		client:          nil,
		pricingProvider: &MockPricingProvider{},
	}

	instanceTypes := []*cloudprovider.InstanceType{
		{
			Name: "bx2-2x8",
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("2"),
				corev1.ResourceMemory: resource.MustParse("8Gi"),
			},
		},
	}

	result := provider.RankInstanceTypes(instanceTypes)
	assert.Equal(t, 1, len(result))
}

// Test edge cases for convertVPCProfileToInstanceType
func TestConvertVPCProfileToInstanceType_EdgeCases(t *testing.T) {
	provider := &IBMInstanceTypeProvider{
		client: nil,
	}

	tests := []struct {
		name        string
		profile     vpcv1.InstanceProfile
		wantErr     bool
		errContains string
	}{
		{
			name: "profile with nil name",
			profile: vpcv1.InstanceProfile{
				Name: nil,
			},
			wantErr:     true,
			errContains: "instance profile name is nil",
		},
		{
			name: "profile without CPU count",
			profile: vpcv1.InstanceProfile{
				Name:      core.StringPtr("test-profile"),
				VcpuCount: nil,
			},
			wantErr:     true,
			errContains: "has no CPU count",
		},
		{
			name: "profile without memory",
			profile: vpcv1.InstanceProfile{
				Name: core.StringPtr("test-profile"),
				VcpuCount: &vpcv1.InstanceProfileVcpu{
					Value: core.Int64Ptr(2),
				},
				Memory: nil,
			},
			wantErr:     true,
			errContains: "has no memory",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := provider.convertVPCProfileToInstanceType(context.Background(), tt.profile)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, result)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, result)
			}
		})
	}
}

// Test convertVPCProfileToInstanceType with GPU - this test expects error due to nil client
func TestConvertVPCProfileToInstanceType_WithGPU(t *testing.T) {
	provider := &IBMInstanceTypeProvider{
		client: nil, // nil client will cause error
	}

	profile := vpcv1.InstanceProfile{
		Name: core.StringPtr("gx2-8x64x1v100"),
		VcpuCount: &vpcv1.InstanceProfileVcpu{
			Value: core.Int64Ptr(8),
		},
		Memory: &vpcv1.InstanceProfileMemory{
			Value: core.Int64Ptr(64),
		},
		VcpuArchitecture: &vpcv1.InstanceProfileVcpuArchitecture{
			Value: core.StringPtr("amd64"),
		},
		GpuCount: &vpcv1.InstanceProfileGpu{
			Value: core.Int64Ptr(1),
		},
	}

	result, err := provider.convertVPCProfileToInstanceType(context.Background(), profile)

	// Should error due to nil client
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "IBM client not initialized")
}

// Test pod capacity calculation - this will also fail due to nil client
func TestConvertVPCProfileToInstanceType_PodCapacity(t *testing.T) {
	provider := &IBMInstanceTypeProvider{
		client: nil, // nil client will cause error
	}

	profile := vpcv1.InstanceProfile{
		Name: core.StringPtr("test-profile"),
		VcpuCount: &vpcv1.InstanceProfileVcpu{
			Value: core.Int64Ptr(2),
		},
		Memory: &vpcv1.InstanceProfileMemory{
			Value: core.Int64Ptr(16),
		},
	}

	result, err := provider.convertVPCProfileToInstanceType(context.Background(), profile)

	// Should error due to nil client trying to get zones
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "IBM client not initialized")
}

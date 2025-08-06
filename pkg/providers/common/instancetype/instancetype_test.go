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
	"unsafe"

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
type MockIBMClient struct{}

func (m *MockIBMClient) GetVPCClient() (*ibm.VPCClient, error) {
	return nil, nil
}

func (m *MockIBMClient) GetGlobalCatalogClient() (*ibm.GlobalCatalogClient, error) {
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
		name     string
		provider *IBMInstanceTypeProvider
		wantErr  bool
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


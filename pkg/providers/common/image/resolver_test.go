/*
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

package image

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/go-openapi/strfmt"
	"github.com/stretchr/testify/assert"

	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
)

// MockVPCSDKClient implements the vpcClientInterface for testing
type MockVPCSDKClient struct {
	getImageFunc   func(ctx context.Context, options *vpcv1.GetImageOptions) (*vpcv1.Image, *core.DetailedResponse, error)
	listImagesFunc func(ctx context.Context, options *vpcv1.ListImagesOptions) (*vpcv1.ImageCollection, *core.DetailedResponse, error)
}

func (m *MockVPCSDKClient) GetImageWithContext(ctx context.Context, options *vpcv1.GetImageOptions) (*vpcv1.Image, *core.DetailedResponse, error) {
	if m.getImageFunc != nil {
		return m.getImageFunc(ctx, options)
	}
	return nil, &core.DetailedResponse{}, fmt.Errorf("image not found")
}

func (m *MockVPCSDKClient) ListImagesWithContext(ctx context.Context, options *vpcv1.ListImagesOptions) (*vpcv1.ImageCollection, *core.DetailedResponse, error) {
	if m.listImagesFunc != nil {
		return m.listImagesFunc(ctx, options)
	}
	return &vpcv1.ImageCollection{Images: []vpcv1.Image{}}, &core.DetailedResponse{}, nil
}

// Implement all required interface methods with no-op implementations
func (m *MockVPCSDKClient) CreateInstanceWithContext(context.Context, *vpcv1.CreateInstanceOptions) (*vpcv1.Instance, *core.DetailedResponse, error) {
	return nil, &core.DetailedResponse{}, fmt.Errorf("not implemented")
}

func (m *MockVPCSDKClient) DeleteInstanceWithContext(context.Context, *vpcv1.DeleteInstanceOptions) (*core.DetailedResponse, error) {
	return &core.DetailedResponse{}, fmt.Errorf("not implemented")
}

func (m *MockVPCSDKClient) GetInstanceWithContext(context.Context, *vpcv1.GetInstanceOptions) (*vpcv1.Instance, *core.DetailedResponse, error) {
	return nil, &core.DetailedResponse{}, fmt.Errorf("not implemented")
}

func (m *MockVPCSDKClient) ListInstancesWithContext(context.Context, *vpcv1.ListInstancesOptions) (*vpcv1.InstanceCollection, *core.DetailedResponse, error) {
	return nil, &core.DetailedResponse{}, fmt.Errorf("not implemented")
}

func (m *MockVPCSDKClient) UpdateInstanceWithContext(context.Context, *vpcv1.UpdateInstanceOptions) (*vpcv1.Instance, *core.DetailedResponse, error) {
	return nil, &core.DetailedResponse{}, fmt.Errorf("not implemented")
}

func (m *MockVPCSDKClient) ListSubnetsWithContext(context.Context, *vpcv1.ListSubnetsOptions) (*vpcv1.SubnetCollection, *core.DetailedResponse, error) {
	return nil, &core.DetailedResponse{}, fmt.Errorf("not implemented")
}

func (m *MockVPCSDKClient) GetSubnetWithContext(context.Context, *vpcv1.GetSubnetOptions) (*vpcv1.Subnet, *core.DetailedResponse, error) {
	return nil, &core.DetailedResponse{}, fmt.Errorf("not implemented")
}

func (m *MockVPCSDKClient) GetVPCWithContext(context.Context, *vpcv1.GetVPCOptions) (*vpcv1.VPC, *core.DetailedResponse, error) {
	return nil, &core.DetailedResponse{}, fmt.Errorf("not implemented")
}

func (m *MockVPCSDKClient) ListInstanceProfilesWithContext(context.Context, *vpcv1.ListInstanceProfilesOptions) (*vpcv1.InstanceProfileCollection, *core.DetailedResponse, error) {
	return nil, &core.DetailedResponse{}, fmt.Errorf("not implemented")
}

func (m *MockVPCSDKClient) ListSecurityGroupsWithContext(context.Context, *vpcv1.ListSecurityGroupsOptions) (*vpcv1.SecurityGroupCollection, *core.DetailedResponse, error) {
	return nil, &core.DetailedResponse{}, fmt.Errorf("not implemented")
}

// Volume methods
func (m *MockVPCSDKClient) ListVolumesWithContext(context.Context, *vpcv1.ListVolumesOptions) (*vpcv1.VolumeCollection, *core.DetailedResponse, error) {
	return nil, &core.DetailedResponse{}, fmt.Errorf("not implemented")
}

func (m *MockVPCSDKClient) DeleteVolumeWithContext(context.Context, *vpcv1.DeleteVolumeOptions) (*core.DetailedResponse, error) {
	return &core.DetailedResponse{}, fmt.Errorf("not implemented")
}

// Virtual Network Interface methods
func (m *MockVPCSDKClient) ListVirtualNetworkInterfacesWithContext(context.Context, *vpcv1.ListVirtualNetworkInterfacesOptions) (*vpcv1.VirtualNetworkInterfaceCollection, *core.DetailedResponse, error) {
	return nil, &core.DetailedResponse{}, fmt.Errorf("not implemented")
}

func (m *MockVPCSDKClient) DeleteVirtualNetworkInterfacesWithContext(context.Context, *vpcv1.DeleteVirtualNetworkInterfacesOptions) (*vpcv1.VirtualNetworkInterface, *core.DetailedResponse, error) {
	return nil, &core.DetailedResponse{}, fmt.Errorf("not implemented")
}

// Load Balancer methods
func (m *MockVPCSDKClient) GetLoadBalancerWithContext(context.Context, *vpcv1.GetLoadBalancerOptions) (*vpcv1.LoadBalancer, *core.DetailedResponse, error) {
	return nil, &core.DetailedResponse{}, fmt.Errorf("not implemented")
}

func (m *MockVPCSDKClient) ListLoadBalancerPoolsWithContext(context.Context, *vpcv1.ListLoadBalancerPoolsOptions) (*vpcv1.LoadBalancerPoolCollection, *core.DetailedResponse, error) {
	return nil, &core.DetailedResponse{}, fmt.Errorf("not implemented")
}

func (m *MockVPCSDKClient) GetLoadBalancerPoolWithContext(context.Context, *vpcv1.GetLoadBalancerPoolOptions) (*vpcv1.LoadBalancerPool, *core.DetailedResponse, error) {
	return nil, &core.DetailedResponse{}, fmt.Errorf("not implemented")
}

func (m *MockVPCSDKClient) CreateLoadBalancerPoolMemberWithContext(context.Context, *vpcv1.CreateLoadBalancerPoolMemberOptions) (*vpcv1.LoadBalancerPoolMember, *core.DetailedResponse, error) {
	return nil, &core.DetailedResponse{}, fmt.Errorf("not implemented")
}

func (m *MockVPCSDKClient) DeleteLoadBalancerPoolMemberWithContext(context.Context, *vpcv1.DeleteLoadBalancerPoolMemberOptions) (*core.DetailedResponse, error) {
	return &core.DetailedResponse{}, fmt.Errorf("not implemented")
}

func (m *MockVPCSDKClient) GetLoadBalancerPoolMemberWithContext(context.Context, *vpcv1.GetLoadBalancerPoolMemberOptions) (*vpcv1.LoadBalancerPoolMember, *core.DetailedResponse, error) {
	return nil, &core.DetailedResponse{}, fmt.Errorf("not implemented")
}

func (m *MockVPCSDKClient) ListLoadBalancerPoolMembersWithContext(context.Context, *vpcv1.ListLoadBalancerPoolMembersOptions) (*vpcv1.LoadBalancerPoolMemberCollection, *core.DetailedResponse, error) {
	return nil, &core.DetailedResponse{}, fmt.Errorf("not implemented")
}

func (m *MockVPCSDKClient) UpdateLoadBalancerPoolMemberWithContext(context.Context, *vpcv1.UpdateLoadBalancerPoolMemberOptions) (*vpcv1.LoadBalancerPoolMember, *core.DetailedResponse, error) {
	return nil, &core.DetailedResponse{}, fmt.Errorf("not implemented")
}

func (m *MockVPCSDKClient) UpdateLoadBalancerPoolWithContext(context.Context, *vpcv1.UpdateLoadBalancerPoolOptions) (*vpcv1.LoadBalancerPool, *core.DetailedResponse, error) {
	return nil, &core.DetailedResponse{}, fmt.Errorf("not implemented")
}


func TestNewResolver(t *testing.T) {
	// Create a mock SDK client that implements the interface
	mockSDKClient := &MockVPCSDKClient{}
	vpcClient := ibm.NewVPCClientWithMock(mockSDKClient)
	region := "us-south"
	
	resolver := NewResolver(vpcClient, region)
	
	assert.NotNil(t, resolver)
	assert.Equal(t, region, resolver.region)
	assert.Equal(t, vpcClient, resolver.vpcClient)
}

func TestResolveImage_WithImageID(t *testing.T) {
	tests := []struct {
		name        string
		imageID     string
		mockFunc    func(ctx context.Context, options *vpcv1.GetImageOptions) (*vpcv1.Image, *core.DetailedResponse, error)
		expected    string
		expectError bool
	}{
		{
			name:    "valid image ID",
			imageID: "r006-12345678-1234-1234-1234-123456789012",
			mockFunc: func(ctx context.Context, options *vpcv1.GetImageOptions) (*vpcv1.Image, *core.DetailedResponse, error) {
				imageID := *options.ID
				return &vpcv1.Image{
					ID:   &imageID,
					Name: stringPtr("ubuntu-20-04"),
				}, &core.DetailedResponse{}, nil
			},
			expected:    "r006-12345678-1234-1234-1234-123456789012",
			expectError: false,
		},
		{
			name:    "image ID not found",
			imageID: "r006-12345678-1234-1234-1234-123456789012",
			mockFunc: func(ctx context.Context, options *vpcv1.GetImageOptions) (*vpcv1.Image, *core.DetailedResponse, error) {
				return nil, &core.DetailedResponse{}, fmt.Errorf("image not found")
			},
			expected:    "",
			expectError: true,
		},
		{
			name:        "empty image identifier",
			imageID:     "",
			expected:    "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockSDKClient := &MockVPCSDKClient{
				getImageFunc: tt.mockFunc,
			}
			vpcClient := ibm.NewVPCClientWithMock(mockSDKClient)
			resolver := NewResolver(vpcClient, "us-south")

			result, err := resolver.ResolveImage(context.Background(), tt.imageID)

			if tt.expectError {
				assert.Error(t, err)
				assert.Equal(t, tt.expected, result)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestResolveImage_WithImageName(t *testing.T) {
	tests := []struct {
		name        string
		imageName   string
		mockFunc    func(ctx context.Context, options *vpcv1.ListImagesOptions) (*vpcv1.ImageCollection, *core.DetailedResponse, error)
		expected    string
		expectError bool
	}{
		{
			name:      "exact name match",
			imageName: "ubuntu-20-04",
			mockFunc: func(ctx context.Context, options *vpcv1.ListImagesOptions) (*vpcv1.ImageCollection, *core.DetailedResponse, error) {
				now := time.Now()
				return &vpcv1.ImageCollection{
					Images: []vpcv1.Image{
						{
							ID:        stringPtr("r006-12345678-1234-1234-1234-123456789012"),
							Name:      stringPtr("ubuntu-20-04"),
							CreatedAt: createStrfmtDateTime(now),
						},
					},
				}, &core.DetailedResponse{}, nil
			},
			expected:    "r006-12345678-1234-1234-1234-123456789012",
			expectError: false,
		},
		{
			name:      "pattern match with multiple images",
			imageName: "ubuntu",
			mockFunc: func(ctx context.Context, options *vpcv1.ListImagesOptions) (*vpcv1.ImageCollection, *core.DetailedResponse, error) {
				now := time.Now()
				return &vpcv1.ImageCollection{
					Images: []vpcv1.Image{
						{
							ID:        stringPtr("r006-12345678-1234-1234-1234-123456789012"),
							Name:      stringPtr("ubuntu-18-04"),
							CreatedAt: createStrfmtDateTime(now.Add(-time.Hour)),
						},
						{
							ID:        stringPtr("r006-87654321-4321-4321-4321-210987654321"),
							Name:      stringPtr("ubuntu-20-04"),
							CreatedAt: createStrfmtDateTime(now), // Most recent
						},
					},
				}, &core.DetailedResponse{}, nil
			},
			expected:    "r006-87654321-4321-4321-4321-210987654321", // Most recent
			expectError: false,
		},
		{
			name:      "no matching images",
			imageName: "nonexistent",
			mockFunc: func(ctx context.Context, options *vpcv1.ListImagesOptions) (*vpcv1.ImageCollection, *core.DetailedResponse, error) {
				return &vpcv1.ImageCollection{Images: []vpcv1.Image{}}, &core.DetailedResponse{}, nil
			},
			expected:    "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockSDKClient := &MockVPCSDKClient{
				listImagesFunc: tt.mockFunc,
			}
			vpcClient := ibm.NewVPCClientWithMock(mockSDKClient)
			resolver := NewResolver(vpcClient, "us-south")

			result, err := resolver.ResolveImage(context.Background(), tt.imageName)

			if tt.expectError {
				assert.Error(t, err)
				assert.Equal(t, tt.expected, result)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestListAvailableImages(t *testing.T) {
	tests := []struct {
		name         string
		nameFilter   string
		mockFunc     func(ctx context.Context, options *vpcv1.ListImagesOptions) (*vpcv1.ImageCollection, *core.DetailedResponse, error)
		expectedLen  int
		expectError  bool
	}{
		{
			name:       "successful listing",
			nameFilter: "ubuntu",
			mockFunc: func(ctx context.Context, options *vpcv1.ListImagesOptions) (*vpcv1.ImageCollection, *core.DetailedResponse, error) {
				now := time.Now()
				return &vpcv1.ImageCollection{
					Images: []vpcv1.Image{
						{
							ID:        stringPtr("r006-12345678-1234-1234-1234-123456789012"),
							Name:      stringPtr("ubuntu-20-04"),
							CreatedAt: createStrfmtDateTime(now),
							Status:    stringPtr("available"),
							OperatingSystem: &vpcv1.OperatingSystem{
								Name: stringPtr("ubuntu-20-04-amd64"),
							},
						},
						{
							ID:        stringPtr("r006-87654321-4321-4321-4321-210987654321"),
							Name:      stringPtr("ubuntu-18-04"),
							CreatedAt: createStrfmtDateTime(now.Add(-time.Hour)),
							Status:    stringPtr("available"),
							OperatingSystem: &vpcv1.OperatingSystem{
								Name: stringPtr("ubuntu-18-04-amd64"),
							},
						},
					},
				}, &core.DetailedResponse{}, nil
			},
			expectedLen: 2,
			expectError: false,
		},
		{
			name:       "API error",
			nameFilter: "ubuntu",
			mockFunc: func(ctx context.Context, options *vpcv1.ListImagesOptions) (*vpcv1.ImageCollection, *core.DetailedResponse, error) {
				return nil, &core.DetailedResponse{}, fmt.Errorf("API error")
			},
			expectedLen: 0,
			expectError: true,
		},
		{
			name:       "empty results",
			nameFilter: "nonexistent",
			mockFunc: func(ctx context.Context, options *vpcv1.ListImagesOptions) (*vpcv1.ImageCollection, *core.DetailedResponse, error) {
				return &vpcv1.ImageCollection{Images: []vpcv1.Image{}}, &core.DetailedResponse{}, nil
			},
			expectedLen: 0,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockSDKClient := &MockVPCSDKClient{
				listImagesFunc: tt.mockFunc,
			}
			vpcClient := ibm.NewVPCClientWithMock(mockSDKClient)
			resolver := NewResolver(vpcClient, "us-south")

			result, err := resolver.ListAvailableImages(context.Background(), tt.nameFilter)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, result)
			} else {
				assert.NoError(t, err)
				assert.Len(t, result, tt.expectedLen)
			}
		})
	}
}

func TestIsImageID(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "valid image ID",
			input:    "r006-12345678-1234-1234-1234-123456789abc",
			expected: true,
		},
		{
			name:     "valid image ID with different region",
			input:    "r014-87654321-4321-4321-4321-cba987654321",
			expected: true,
		},
		{
			name:     "image name",
			input:    "ubuntu-20-04-minimal",
			expected: false,
		},
		{
			name:     "short string",
			input:    "r006",
			expected: false,
		},
		{
			name:     "doesn't start with r",
			input:    "a006-12345678-1234-1234-1234-123456789abc",
			expected: false,
		},
		{
			name:     "invalid region format",
			input:    "rabc-12345678-1234-1234-1234-123456789abc",
			expected: false,
		},
		{
			name:     "too few UUID parts",
			input:    "r006-12345678",
			expected: false,
		},
		{
			name:     "empty string",
			input:    "",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isImageID(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestMatchesImageName(t *testing.T) {
	tests := []struct {
		name      string
		imageName string
		pattern   string
		expected  bool
	}{
		{
			name:      "exact match",
			imageName: "ubuntu-20-04",
			pattern:   "ubuntu-20-04",
			expected:  true,
		},
		{
			name:      "contains match",
			imageName: "ibm-ubuntu-20-04-minimal-amd64-2",
			pattern:   "ubuntu",
			expected:  true,
		},
		{
			name:      "case insensitive",
			imageName: "IBM-Ubuntu-20-04",
			pattern:   "ubuntu",
			expected:  true,
		},
		{
			name:      "wildcard prefix",
			imageName: "ibm-ubuntu-20-04-minimal",
			pattern:   "*ubuntu*",
			expected:  true,
		},
		{
			name:      "wildcard suffix",
			imageName: "ubuntu-20-04-minimal",
			pattern:   "ubuntu*",
			expected:  true,
		},
		{
			name:      "wildcard prefix only",
			imageName: "ibm-ubuntu-20-04",
			pattern:   "*04",
			expected:  true,
		},
		{
			name:      "no match",
			imageName: "centos-7-9",
			pattern:   "ubuntu",
			expected:  false,
		},
		{
			name:      "wildcard no match",
			imageName: "centos-7-9",
			pattern:   "*ubuntu*",
			expected:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := matchesImageName(tt.imageName, tt.pattern)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestImageSorting(t *testing.T) {
	now := time.Now()
	
	mockSDKClient := &MockVPCSDKClient{
		listImagesFunc: func(ctx context.Context, options *vpcv1.ListImagesOptions) (*vpcv1.ImageCollection, *core.DetailedResponse, error) {
			return &vpcv1.ImageCollection{
				Images: []vpcv1.Image{
					{
						ID:        stringPtr("r006-old"),
						Name:      stringPtr("ubuntu-18-04"),
						CreatedAt: createStrfmtDateTime(now.Add(-2 * time.Hour)),
					},
					{
						ID:        stringPtr("r006-newest"),
						Name:      stringPtr("ubuntu-20-04"),
						CreatedAt: createStrfmtDateTime(now),
					},
					{
						ID:        stringPtr("r006-middle"),
						Name:      stringPtr("ubuntu-19-04"),
						CreatedAt: createStrfmtDateTime(now.Add(-time.Hour)),
					},
				},
			}, &core.DetailedResponse{}, nil
		},
	}
	vpcClient := ibm.NewVPCClientWithMock(mockSDKClient)
	resolver := NewResolver(vpcClient, "us-south")

	// Test that the resolver returns the most recent image
	result, err := resolver.ResolveImage(context.Background(), "ubuntu")
	assert.NoError(t, err)
	assert.Equal(t, "r006-newest", result)
}

func TestEdgeCases(t *testing.T) {
	t.Run("empty image name in ResolveImage", func(t *testing.T) {
		mockSDKClient := &MockVPCSDKClient{}
		vpcClient := ibm.NewVPCClientWithMock(mockSDKClient)
		resolver := NewResolver(vpcClient, "us-south")

		result, err := resolver.ResolveImage(context.Background(), "")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "cannot be empty")
		assert.Empty(t, result)
	})

	t.Run("image with nil ID", func(t *testing.T) {
		mockSDKClient := &MockVPCSDKClient{
			listImagesFunc: func(ctx context.Context, options *vpcv1.ListImagesOptions) (*vpcv1.ImageCollection, *core.DetailedResponse, error) {
				now := time.Now()
				return &vpcv1.ImageCollection{
					Images: []vpcv1.Image{
						{
							ID:        nil, // nil ID
							Name:      stringPtr("ubuntu-20-04"),
							CreatedAt: createStrfmtDateTime(now),
						},
					},
				}, &core.DetailedResponse{}, nil
			},
		}
		vpcClient := ibm.NewVPCClientWithMock(mockSDKClient)
		resolver := NewResolver(vpcClient, "us-south")

		result, err := resolver.ResolveImage(context.Background(), "ubuntu")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "has no ID")
		assert.Empty(t, result)
	})

	t.Run("pattern matching edge cases", func(t *testing.T) {
		tests := []struct {
			pattern   string
			imageName string
			expected  bool
		}{
			{"*", "ubuntu-20-04", true},        // Single wildcard
			{"", "ubuntu-20-04", true},         // Empty pattern matches everything via contains check
		}

		for _, tt := range tests {
			result := matchesImageName(tt.imageName, tt.pattern)
			assert.Equal(t, tt.expected, result, "Pattern %s should match %s: %v", tt.pattern, tt.imageName, tt.expected)
		}
	})
}

// createStrfmtDateTime converts time.Time to *strfmt.DateTime for IBM VPC SDK compatibility
func createStrfmtDateTime(t time.Time) *strfmt.DateTime {
	dt := strfmt.DateTime(t)
	return &dt
}
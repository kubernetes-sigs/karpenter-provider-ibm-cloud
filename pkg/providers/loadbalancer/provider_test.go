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

package loadbalancer

import (
	"context"
	"testing"

	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
)

// MockVPCClient is a mock implementation of the VPC client for testing
type MockVPCClient struct {
	mock.Mock
}

func (m *MockVPCClient) GetLoadBalancer(ctx context.Context, loadBalancerID string) (*vpcv1.LoadBalancer, error) {
	args := m.Called(ctx, loadBalancerID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*vpcv1.LoadBalancer), args.Error(1)
}

func (m *MockVPCClient) ListLoadBalancerPools(ctx context.Context, loadBalancerID string) (*vpcv1.LoadBalancerPoolCollection, error) {
	args := m.Called(ctx, loadBalancerID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*vpcv1.LoadBalancerPoolCollection), args.Error(1)
}

func (m *MockVPCClient) CreateLoadBalancerPoolMember(ctx context.Context, loadBalancerID, poolID string, target vpcv1.LoadBalancerPoolMemberTargetPrototypeIntf, port int64, weight int64) (*vpcv1.LoadBalancerPoolMember, error) {
	args := m.Called(ctx, loadBalancerID, poolID, target, port, weight)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*vpcv1.LoadBalancerPoolMember), args.Error(1)
}

func (m *MockVPCClient) DeleteLoadBalancerPoolMember(ctx context.Context, loadBalancerID, poolID, memberID string) error {
	args := m.Called(ctx, loadBalancerID, poolID, memberID)
	return args.Error(0)
}

func (m *MockVPCClient) GetLoadBalancerPoolMember(ctx context.Context, loadBalancerID, poolID, memberID string) (*vpcv1.LoadBalancerPoolMember, error) {
	args := m.Called(ctx, loadBalancerID, poolID, memberID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*vpcv1.LoadBalancerPoolMember), args.Error(1)
}

func (m *MockVPCClient) ListLoadBalancerPoolMembers(ctx context.Context, loadBalancerID, poolID string) (*vpcv1.LoadBalancerPoolMemberCollection, error) {
	args := m.Called(ctx, loadBalancerID, poolID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*vpcv1.LoadBalancerPoolMemberCollection), args.Error(1)
}

func (m *MockVPCClient) GetLoadBalancerPool(ctx context.Context, loadBalancerID, poolID string) (*vpcv1.LoadBalancerPool, error) {
	args := m.Called(ctx, loadBalancerID, poolID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*vpcv1.LoadBalancerPool), args.Error(1)
}

func (m *MockVPCClient) UpdateLoadBalancerPool(ctx context.Context, loadBalancerID, poolID string, updates map[string]interface{}) (*vpcv1.LoadBalancerPool, error) {
	args := m.Called(ctx, loadBalancerID, poolID, updates)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*vpcv1.LoadBalancerPool), args.Error(1)
}

func TestLoadBalancerProvider_RegisterInstance(t *testing.T) {
	tests := []struct {
		name        string
		nodeClass   *v1alpha1.IBMNodeClass
		instanceID  string
		instanceIP  string
		setupMocks  func(*MockVPCClient)
		expectError bool
	}{
		{
			name: "successful registration with single target",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{
					LoadBalancerIntegration: &v1alpha1.LoadBalancerIntegration{
						Enabled: true,
						TargetGroups: []v1alpha1.LoadBalancerTarget{
							{
								LoadBalancerID: "r010-12345678-1234-5678-9abc-def012345678",
								PoolName:       "web-servers",
								Port:           80,
								Weight:         ptr(int32(50)),
							},
						},
					},
				},
			},
			instanceID: "instance-123",
			instanceIP: "10.0.0.100",
			setupMocks: func(m *MockVPCClient) {
				poolID := "pool-123"
				memberID := "member-123"

				// Mock ListLoadBalancerPools
				pools := &vpcv1.LoadBalancerPoolCollection{
					Pools: []vpcv1.LoadBalancerPool{
						{
							ID:   &poolID,
							Name: ptr("web-servers"),
						},
					},
				}
				m.On("ListLoadBalancerPools", mock.Anything, "r010-12345678-1234-5678-9abc-def012345678").Return(pools, nil)

				// Mock CreateLoadBalancerPoolMember
				member := &vpcv1.LoadBalancerPoolMember{
					ID: &memberID,
				}
				m.On("CreateLoadBalancerPoolMember", mock.Anything, "r010-12345678-1234-5678-9abc-def012345678", poolID, mock.Anything, int64(80), int64(50)).Return(member, nil)
			},
			expectError: false,
		},
		{
			name: "integration disabled",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{
					LoadBalancerIntegration: &v1alpha1.LoadBalancerIntegration{
						Enabled: false,
					},
				},
			},
			instanceID:  "instance-123",
			instanceIP:  "10.0.0.100",
			setupMocks:  func(m *MockVPCClient) {},
			expectError: false,
		},
		{
			name: "no load balancer integration",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{},
			},
			instanceID:  "instance-123",
			instanceIP:  "10.0.0.100",
			setupMocks:  func(m *MockVPCClient) {},
			expectError: false,
		},
		{
			name: "pool not found error",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{
					LoadBalancerIntegration: &v1alpha1.LoadBalancerIntegration{
						Enabled: true,
						TargetGroups: []v1alpha1.LoadBalancerTarget{
							{
								LoadBalancerID: "r010-12345678-1234-5678-9abc-def012345678",
								PoolName:       "nonexistent-pool",
								Port:           80,
							},
						},
					},
				},
			},
			instanceID: "instance-123",
			instanceIP: "10.0.0.100",
			setupMocks: func(m *MockVPCClient) {
				// Mock ListLoadBalancerPools returning empty list
				pools := &vpcv1.LoadBalancerPoolCollection{
					Pools: []vpcv1.LoadBalancerPool{},
				}
				m.On("ListLoadBalancerPools", mock.Anything, "r010-12345678-1234-5678-9abc-def012345678").Return(pools, nil)
			},
			expectError: false, // The function continues and succeeds even if one target fails
		},
		{
			name: "multiple target groups",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{
					LoadBalancerIntegration: &v1alpha1.LoadBalancerIntegration{
						Enabled: true,
						TargetGroups: []v1alpha1.LoadBalancerTarget{
							{
								LoadBalancerID: "r010-12345678-1234-5678-9abc-def012345678",
								PoolName:       "web-servers",
								Port:           80,
								Weight:         ptr(int32(50)),
							},
							{
								LoadBalancerID: "r010-87654321-4321-8765-cba9-fed210987654",
								PoolName:       "api-servers",
								Port:           443,
								Weight:         ptr(int32(50)),
							},
						},
					},
				},
			},
			instanceID: "instance-123",
			instanceIP: "10.0.0.100",
			setupMocks: func(m *MockVPCClient) {
				poolID1 := "pool-123"
				poolID2 := "pool-456"
				memberID1 := "member-123"
				memberID2 := "member-456"

				// Mock first load balancer
				pools1 := &vpcv1.LoadBalancerPoolCollection{
					Pools: []vpcv1.LoadBalancerPool{
						{
							ID:   &poolID1,
							Name: ptr("web-servers"),
						},
					},
				}
				m.On("ListLoadBalancerPools", mock.Anything, "r010-12345678-1234-5678-9abc-def012345678").Return(pools1, nil)

				member1 := &vpcv1.LoadBalancerPoolMember{
					ID: &memberID1,
				}
				m.On("CreateLoadBalancerPoolMember", mock.Anything, "r010-12345678-1234-5678-9abc-def012345678", poolID1, mock.Anything, int64(80), int64(50)).Return(member1, nil)

				// Mock second load balancer
				pools2 := &vpcv1.LoadBalancerPoolCollection{
					Pools: []vpcv1.LoadBalancerPool{
						{
							ID:   &poolID2,
							Name: ptr("api-servers"),
						},
					},
				}
				m.On("ListLoadBalancerPools", mock.Anything, "r010-87654321-4321-8765-cba9-fed210987654").Return(pools2, nil)

				member2 := &vpcv1.LoadBalancerPoolMember{
					ID: &memberID2,
				}
				m.On("CreateLoadBalancerPoolMember", mock.Anything, "r010-87654321-4321-8765-cba9-fed210987654", poolID2, mock.Anything, int64(443), int64(50)).Return(member2, nil)
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &MockVPCClient{}
			tt.setupMocks(mockClient)

			// Create provider with mock client
			provider := NewLoadBalancerProvider(mockClient, logr.Discard())

			ctx := context.Background()
			err := provider.RegisterInstance(ctx, tt.nodeClass, tt.instanceID, tt.instanceIP)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			mockClient.AssertExpectations(t)
		})
	}
}

func TestLoadBalancerProvider_DeregisterInstance(t *testing.T) {
	tests := []struct {
		name        string
		nodeClass   *v1alpha1.IBMNodeClass
		instanceID  string
		setupMocks  func(*MockVPCClient)
		expectError bool
	}{
		{
			name: "successful deregistration",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{
					LoadBalancerIntegration: &v1alpha1.LoadBalancerIntegration{
						Enabled:        true,
						AutoDeregister: ptr(true),
						TargetGroups: []v1alpha1.LoadBalancerTarget{
							{
								LoadBalancerID: "r010-12345678-1234-5678-9abc-def012345678",
								PoolName:       "web-servers",
								Port:           80,
							},
						},
					},
				},
			},
			instanceID: "instance-123",
			setupMocks: func(m *MockVPCClient) {
				poolID := "pool-123"
				memberID := "member-123"

				// Mock ListLoadBalancerPools
				pools := &vpcv1.LoadBalancerPoolCollection{
					Pools: []vpcv1.LoadBalancerPool{
						{
							ID:   &poolID,
							Name: ptr("web-servers"),
						},
					},
				}
				m.On("ListLoadBalancerPools", mock.Anything, "r010-12345678-1234-5678-9abc-def012345678").Return(pools, nil)

				// Mock ListLoadBalancerPoolMembers
				members := &vpcv1.LoadBalancerPoolMemberCollection{
					Members: []vpcv1.LoadBalancerPoolMember{
						{
							ID: &memberID,
							Target: &vpcv1.LoadBalancerPoolMemberTarget{
								ID: ptr("instance-123"),
							},
						},
					},
				}
				m.On("ListLoadBalancerPoolMembers", mock.Anything, "r010-12345678-1234-5678-9abc-def012345678", poolID).Return(members, nil)

				// Mock DeleteLoadBalancerPoolMember
				m.On("DeleteLoadBalancerPoolMember", mock.Anything, "r010-12345678-1234-5678-9abc-def012345678", poolID, memberID).Return(nil)
			},
			expectError: false,
		},
		{
			name: "auto deregister disabled",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{
					LoadBalancerIntegration: &v1alpha1.LoadBalancerIntegration{
						Enabled:        true,
						AutoDeregister: ptr(false),
						TargetGroups: []v1alpha1.LoadBalancerTarget{
							{
								LoadBalancerID: "r010-12345678-1234-5678-9abc-def012345678",
								PoolName:       "web-servers",
								Port:           80,
							},
						},
					},
				},
			},
			instanceID:  "instance-123",
			setupMocks:  func(m *MockVPCClient) {},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &MockVPCClient{}
			tt.setupMocks(mockClient)

			provider := NewLoadBalancerProvider(mockClient, logr.Discard())

			ctx := context.Background()
			err := provider.DeregisterInstance(ctx, tt.nodeClass, tt.instanceID)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			mockClient.AssertExpectations(t)
		})
	}
}

func TestLoadBalancerProvider_ValidateLoadBalancerConfiguration(t *testing.T) {
	tests := []struct {
		name        string
		nodeClass   *v1alpha1.IBMNodeClass
		setupMocks  func(*MockVPCClient)
		expectError bool
	}{
		{
			name: "valid configuration",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{
					LoadBalancerIntegration: &v1alpha1.LoadBalancerIntegration{
						Enabled: true,
						TargetGroups: []v1alpha1.LoadBalancerTarget{
							{
								LoadBalancerID: "r010-12345678-1234-5678-9abc-def012345678",
								PoolName:       "web-servers",
								Port:           80,
							},
						},
					},
				},
			},
			setupMocks: func(m *MockVPCClient) {
				// Mock GetLoadBalancer
				lb := &vpcv1.LoadBalancer{
					ID:   ptr("r010-12345678-1234-5678-9abc-def012345678"),
					Name: ptr("test-lb"),
				}
				m.On("GetLoadBalancer", mock.Anything, "r010-12345678-1234-5678-9abc-def012345678").Return(lb, nil)

				// Mock ListLoadBalancerPools
				poolID := "pool-123"
				pools := &vpcv1.LoadBalancerPoolCollection{
					Pools: []vpcv1.LoadBalancerPool{
						{
							ID:   &poolID,
							Name: ptr("web-servers"),
						},
					},
				}
				m.On("ListLoadBalancerPools", mock.Anything, "r010-12345678-1234-5678-9abc-def012345678").Return(pools, nil)
			},
			expectError: false,
		},
		{
			name: "integration disabled",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{},
			},
			setupMocks:  func(m *MockVPCClient) {},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &MockVPCClient{}
			tt.setupMocks(mockClient)

			provider := NewLoadBalancerProvider(mockClient, logr.Discard())

			ctx := context.Background()
			err := provider.ValidateLoadBalancerConfiguration(ctx, tt.nodeClass)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			mockClient.AssertExpectations(t)
		})
	}
}

// Helper function to create pointers
func ptr[T any](val T) *T {
	return &val
}

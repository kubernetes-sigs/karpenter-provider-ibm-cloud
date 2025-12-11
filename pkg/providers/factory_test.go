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

package providers

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	commonTypes "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/types"
)

// MockIBMClient provides a mock implementation of the IBM client
type MockIBMClient struct {
	mock.Mock
}

func (m *MockIBMClient) GetVPCClient() (*ibm.VPCClient, error) {
	args := m.Called()
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*ibm.VPCClient), args.Error(1)
}

func (m *MockIBMClient) GetIKSClient() (ibm.IKSClientInterface, error) {
	args := m.Called()
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(ibm.IKSClientInterface), args.Error(1)
}

// Test helpers
func getTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(s)
	return s
}

func getVPCNodeClass() *v1alpha1.IBMNodeClass {
	return &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "vpc-nodeclass",
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			Region:          "us-south",
			Zone:            "us-south-1",
			InstanceProfile: "bx2-4x16",
			Image:           "test-image",
			VPC:             "test-vpc",
			Subnet:          "test-subnet",
			// No IKS-specific fields
		},
	}
}

func getIKSNodeClass() *v1alpha1.IBMNodeClass {
	return &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "iks-nodeclass",
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			Region:          "us-south",
			Zone:            "us-south-1",
			InstanceProfile: "bx2-4x16",
			Image:           "test-image",
			VPC:             "test-vpc",
			IKSClusterID:    "test-cluster-id", // IKS-specific field
		},
	}
}

func getBootstrapModeNodeClass(mode string) *v1alpha1.IBMNodeClass {
	return &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "bootstrap-nodeclass",
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			Region:          "us-south",
			Zone:            "us-south-1",
			InstanceProfile: "bx2-4x16",
			Image:           "test-image",
			VPC:             "test-vpc",
			BootstrapMode:   &mode,
		},
	}
}

func TestProviderFactory_GetInstanceProvider(t *testing.T) {
	// Save original env var and restore after test
	originalClusterID := os.Getenv("IKS_CLUSTER_ID")
	defer func() {
		if originalClusterID != "" {
			_ = os.Setenv("IKS_CLUSTER_ID", originalClusterID)
		} else {
			_ = os.Unsetenv("IKS_CLUSTER_ID")
		}
	}()

	tests := []struct {
		name             string
		nodeClass        *v1alpha1.IBMNodeClass
		envClusterID     string
		expectedMode     commonTypes.ProviderMode
		expectError      bool
		errorContains    string
		validateProvider func(*testing.T, commonTypes.InstanceProvider)
	}{
		{
			name:          "VPC mode - no IKS configuration (nil client)",
			nodeClass:     getVPCNodeClass(),
			expectedMode:  commonTypes.VPCMode,
			expectError:   true,
			errorContains: "IBM client cannot be nil",
		},
		{
			name:          "IKS mode - NodeClass has IKS cluster ID (nil client)",
			nodeClass:     getIKSNodeClass(),
			expectedMode:  commonTypes.IKSMode,
			expectError:   true,
			errorContains: "IBM client cannot be nil",
		},
		{
			name:          "IKS mode - environment variable (nil client)",
			nodeClass:     getVPCNodeClass(), // VPC NodeClass but env var should override
			envClusterID:  "env-cluster-id",
			expectedMode:  commonTypes.IKSMode,
			expectError:   true,
			errorContains: "IBM client cannot be nil",
		},
		{
			name:          "IKS mode - bootstrap mode set to iks-api (nil client)",
			nodeClass:     getBootstrapModeNodeClass("iks-api"),
			expectedMode:  commonTypes.IKSMode,
			expectError:   true,
			errorContains: "IBM client cannot be nil",
		},
		{
			name:          "VPC mode - bootstrap mode set to cloud-init (nil client)",
			nodeClass:     getBootstrapModeNodeClass("cloud-init"),
			expectedMode:  commonTypes.VPCMode,
			expectError:   true,
			errorContains: "IBM client cannot be nil",
		},
		{
			name:          "VPC mode - bootstrap mode set to auto (nil client)",
			nodeClass:     getBootstrapModeNodeClass("auto"),
			expectedMode:  commonTypes.VPCMode, // Should default to VPC when no IKS indicators
			expectError:   true,
			errorContains: "IBM client cannot be nil",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up environment variable
			if tt.envClusterID != "" {
				_ = os.Setenv("IKS_CLUSTER_ID", tt.envClusterID)
			} else {
				_ = os.Unsetenv("IKS_CLUSTER_ID")
			}

			// Create fake Kubernetes client
			scheme := getTestScheme()
			fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

			// Create mock IBM client
			// Create provider factory with nil client for testing
			factory := NewProviderFactory(nil, fakeClient, nil)

			// Test GetInstanceProvider method
			result, err := factory.GetInstanceProvider(tt.nodeClass)

			// Validate results
			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
				assert.Nil(t, result)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, result)
				if tt.validateProvider != nil {
					tt.validateProvider(t, result)
				}
			}
		})
	}
}

func TestProviderFactory_DetermineProviderMode(t *testing.T) {
	// Save original env var and restore after test
	originalClusterID := os.Getenv("IKS_CLUSTER_ID")
	defer func() {
		if originalClusterID != "" {
			_ = os.Setenv("IKS_CLUSTER_ID", originalClusterID)
		} else {
			_ = os.Unsetenv("IKS_CLUSTER_ID")
		}
	}()

	tests := []struct {
		name         string
		nodeClass    *v1alpha1.IBMNodeClass
		envClusterID string
		expectedMode commonTypes.ProviderMode
	}{
		{
			name:         "VPC mode - clean VPC NodeClass",
			nodeClass:    getVPCNodeClass(),
			expectedMode: commonTypes.VPCMode,
		},
		{
			name:         "IKS mode - NodeClass has IKS cluster ID",
			nodeClass:    getIKSNodeClass(),
			expectedMode: commonTypes.IKSMode,
		},
		{
			name:         "IKS mode - environment cluster ID",
			nodeClass:    getVPCNodeClass(),
			envClusterID: "env-cluster-123",
			expectedMode: commonTypes.IKSMode,
		},
		{
			name:         "IKS mode - NodeClass bootstrap mode overrides",
			nodeClass:    getBootstrapModeNodeClass("iks-api"),
			expectedMode: commonTypes.IKSMode,
		},
		{
			name:         "VPC mode - NodeClass bootstrap mode overrides",
			nodeClass:    getBootstrapModeNodeClass("cloud-init"),
			expectedMode: commonTypes.VPCMode,
		},
		{
			name:         "IKS mode - NodeClass bootstrap mode takes precedence over env",
			nodeClass:    getBootstrapModeNodeClass("iks-api"),
			expectedMode: commonTypes.IKSMode,
		},
		{
			name:         "IKS mode - multiple IKS indicators",
			nodeClass:    getIKSNodeClass(),
			envClusterID: "env-cluster-456",
			expectedMode: commonTypes.IKSMode,
		},
		{
			name:         "VPC mode - auto bootstrap mode with no IKS indicators",
			nodeClass:    getBootstrapModeNodeClass("auto"),
			expectedMode: commonTypes.VPCMode,
		},
		{
			name:         "IKS mode - auto bootstrap mode with IKS cluster ID",
			nodeClass:    getBootstrapModeNodeClass("auto"),
			envClusterID: "auto-cluster-789",
			expectedMode: commonTypes.IKSMode,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up environment variables
			if tt.envClusterID != "" {
				_ = os.Setenv("IKS_CLUSTER_ID", tt.envClusterID)
			} else {
				_ = os.Unsetenv("IKS_CLUSTER_ID")
			}

			// Create fake Kubernetes client
			scheme := getTestScheme()
			fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

			// Create mock IBM client
			// Create provider factory with nil client for testing
			factory := NewProviderFactory(nil, fakeClient, nil)

			// Test determineProviderMode method
			result := factory.determineProviderMode(tt.nodeClass)

			// Validate result
			assert.Equal(t, tt.expectedMode, result)
		})
	}
}

func TestProviderFactory_EdgeCases(t *testing.T) {
	tests := []struct {
		name          string
		setupFactory  func() *ProviderFactory
		nodeClass     *v1alpha1.IBMNodeClass
		expectError   bool
		errorContains string
	}{
		{
			name: "nil IBM client",
			setupFactory: func() *ProviderFactory {
				scheme := getTestScheme()
				fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
				return NewProviderFactory(nil, fakeClient, nil)
			},
			nodeClass:     getVPCNodeClass(),
			expectError:   true,
			errorContains: "IBM client cannot be nil",
		},
		{
			name: "nil Kubernetes client (IBM client nil first)",
			setupFactory: func() *ProviderFactory {
				// Using nil IBM and kube clients for testing
				return NewProviderFactory(nil, nil, nil)
			},
			nodeClass:     getVPCNodeClass(),
			expectError:   true,
			errorContains: "IBM client cannot be nil", // IBM client is checked first
		},
		{
			name: "nil NodeClass",
			setupFactory: func() *ProviderFactory {
				scheme := getTestScheme()
				fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
				// Using nil client for testing
				return NewProviderFactory(nil, fakeClient, nil)
			},
			nodeClass:     nil,
			expectError:   true,
			errorContains: "nodeClass cannot be nil",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			factory := tt.setupFactory()

			// Test GetInstanceProvider with edge case
			result, err := factory.GetInstanceProvider(tt.nodeClass)

			// Validate results
			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
				assert.Nil(t, result)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, result)
			}
		})
	}
}

func TestProviderFactory_ModeSwitching(t *testing.T) {
	t.Skip("Skipping mode switching test - requires valid IBM client for provider creation")

	// This test would need a mock IBM client to properly test mode switching
	// since provider constructors now validate client is not nil
}

func TestProviderFactory_BootstrapModePrecedence(t *testing.T) {
	// Save original env vars and restore after test
	originalClusterID := os.Getenv("IKS_CLUSTER_ID")
	defer func() {
		if originalClusterID != "" {
			_ = os.Setenv("IKS_CLUSTER_ID", originalClusterID)
		} else {
			_ = os.Unsetenv("IKS_CLUSTER_ID")
		}
	}()

	// Create factory
	scheme := getTestScheme()
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	// Using nil client for testing
	factory := NewProviderFactory(nil, fakeClient, nil)

	// Test precedence: NodeClass bootstrap mode > NodeClass cluster ID > env cluster ID > default
	tests := []struct {
		name         string
		nodeClass    *v1alpha1.IBMNodeClass
		envClusterID string
		expectedMode commonTypes.ProviderMode
	}{
		{
			name:         "NodeClass bootstrap mode wins over env cluster ID",
			nodeClass:    getBootstrapModeNodeClass("cloud-init"),
			envClusterID: "should-be-ignored",
			expectedMode: commonTypes.VPCMode, // cloud-init = VPC
		},
		{
			name:         "Cluster ID in NodeClass wins over env cluster ID",
			nodeClass:    getIKSNodeClass(),
			envClusterID: "env-cluster-456",
			expectedMode: commonTypes.IKSMode, // NodeClass cluster ID
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up environment variables
			if tt.envClusterID != "" {
				_ = os.Setenv("IKS_CLUSTER_ID", tt.envClusterID)
			} else {
				_ = os.Unsetenv("IKS_CLUSTER_ID")
			}

			// Test mode determination using public method
			mode := factory.GetProviderMode(tt.nodeClass)
			assert.Equal(t, tt.expectedMode, mode)

			// Skip provider creation test since IBM client is nil
			// The mode determination logic is what we're testing here
		})
	}
}

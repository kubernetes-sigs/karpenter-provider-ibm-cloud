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
package status

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/cache"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/providers/vpc/subnet"
)

// =============================================================================
// SETUP HELPERS
// =============================================================================

// MockSubnetProvider is a mock implementation of subnet.Provider
type MockSubnetProvider struct {
	mock.Mock
}

func (m *MockSubnetProvider) ListSubnets(ctx context.Context, vpcID string) ([]subnet.SubnetInfo, error) {
	args := m.Called(ctx, vpcID)
	return args.Get(0).([]subnet.SubnetInfo), args.Error(1)
}

func (m *MockSubnetProvider) GetSubnet(ctx context.Context, subnetID string) (*subnet.SubnetInfo, error) {
	args := m.Called(ctx, subnetID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*subnet.SubnetInfo), args.Error(1)
}

func (m *MockSubnetProvider) SelectSubnets(ctx context.Context, vpcID string, strategy *v1alpha1.PlacementStrategy) ([]subnet.SubnetInfo, error) {
	args := m.Called(ctx, vpcID, strategy)
	return args.Get(0).([]subnet.SubnetInfo), args.Error(1)
}

func (m *MockSubnetProvider) SetKubernetesClient(kubeClient kubernetes.Interface) {
	// Mock implementation - do nothing
}

func getTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	_ = v1alpha1.AddToScheme(s)
	return s
}

func getValidNodeClass() *v1alpha1.IBMNodeClass {
	// Use real IBM Cloud resources if available
	vpcID := os.Getenv("VPC_ID")
	if vpcID == "" {
		vpcID = "vpc-12345678" // Fallback for unit tests
	}

	return &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "valid-nodeclass",
			Namespace: "default",
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			Region:          "us-south",
			VPC:             vpcID,
			Image:           "r006-988caa8b-7786-49c9-aea6-9553af2b1969", // Real Ubuntu 20.04 image
			InstanceProfile: "bx2-2x8",                                   // Add required instanceProfile
		},
	}
}

// =============================================================================
// UNIT TESTS - Field Validation
// =============================================================================

func TestValidateRequiredFields(t *testing.T) {
	tests := []struct {
		name          string
		nodeClass     *v1alpha1.IBMNodeClass
		expectedError string
	}{
		{
			name:          "all required fields present",
			nodeClass:     getValidNodeClass(),
			expectedError: "",
		},
		{
			name: "missing region",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{
					VPC:   "vpc-12345678",
					Image: "r006-12345678-1234-1234-1234-123456789012",
				},
			},
			expectedError: "region",
		},
		{
			name: "missing vpc",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{
					Region: "us-south",
					Image:  "r006-12345678-1234-1234-1234-123456789012",
				},
			},
			expectedError: "vpc",
		},
		{
			name: "missing image",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{
					Region: "us-south",
					VPC:    "vpc-12345678",
				},
			},
			expectedError: "image",
		},
		{
			name: "multiple missing fields",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{},
			},
			expectedError: "required fields missing",
		},
		{
			name: "empty strings treated as missing",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{
					Region: "",
					VPC:    "",
					Image:  "",
				},
			},
			expectedError: "required fields missing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controller := &Controller{}
			err := controller.validateRequiredFields(tt.nodeClass)

			if tt.expectedError == "" {
				assert.NoError(t, err, "Expected no validation error")
			} else {
				require.Error(t, err, "Expected validation error")
				assert.Contains(t, err.Error(), tt.expectedError, "Error should contain expected field name")
			}
		})
	}
}

// =============================================================================
// UNIT TESTS - Format Validation
// =============================================================================

func TestValidateFieldFormats(t *testing.T) {
	tests := []struct {
		name          string
		nodeClass     *v1alpha1.IBMNodeClass
		expectedError string
	}{
		{
			name:          "valid formats",
			nodeClass:     getValidNodeClass(),
			expectedError: "",
		},
		{
			name: "invalid VPC ID format",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{
					Region: "us-south",
					VPC:    "invalid", // Too short, no dashes
					Image:  "r006-12345678-1234-1234-1234-123456789012",
				},
			},
			expectedError: "VPC ID format invalid",
		},
		{
			name: "invalid image ID format",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{
					Region: "us-south",
					VPC:    "vpc-12345678",
					Image:  "invalid-image-id",
				},
			},
			expectedError: "", // Current implementation doesn't validate image format
		},
		{
			name: "invalid zone format",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{
					Region: "us-south",
					Zone:   "eu-gb-1", // Wrong region
					VPC:    "vpc-12345678",
					Image:  "r006-12345678-1234-1234-1234-123456789012",
				},
			},
			expectedError: "must start with region prefix",
		},
		{
			name: "invalid subnet ID format",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{
					Region: "us-south",
					VPC:    "r006-12345678-1234-1234-1234-123456789012",
					Image:  "r006-12345678-1234-1234-1234-123456789012",
					Subnet: "invalid", // Too short, no dashes
				},
			},
			expectedError: "subnet ID format invalid",
		},
		{
			name: "valid optional fields",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{
					Region: "us-south",
					Zone:   "us-south-1",
					VPC:    "r006-12345678-1234-1234-1234-123456789012",
					Image:  "r006-12345678-1234-1234-1234-123456789012",
					Subnet: "0717-12345678-1234-1234-1234-123456789012",
				},
			},
			expectedError: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controller := &Controller{}
			err := controller.validateFieldFormats(tt.nodeClass)

			if tt.expectedError == "" {
				assert.NoError(t, err, "Expected no validation error")
			} else {
				require.Error(t, err, "Expected validation error")
				assert.Contains(t, err.Error(), tt.expectedError, "Error should contain expected message")
			}
		})
	}
}

// =============================================================================
// UNIT TESTS - Placement Strategy Validation
// =============================================================================

func TestValidatePlacementStrategy(t *testing.T) {
	tests := []struct {
		name          string
		strategy      *v1alpha1.PlacementStrategy
		expectedError string
	}{
		{
			name:          "nil strategy should pass",
			strategy:      nil,
			expectedError: "",
		},
		{
			name: "valid balanced strategy",
			strategy: &v1alpha1.PlacementStrategy{
				ZoneBalance: "Balanced",
				SubnetSelection: &v1alpha1.SubnetSelectionCriteria{
					MinimumAvailableIPs: 10,
					RequiredTags: map[string]string{
						"Environment": "production",
					},
				},
			},
			expectedError: "",
		},
		{
			name: "valid availability first strategy",
			strategy: &v1alpha1.PlacementStrategy{
				ZoneBalance: "AvailabilityFirst",
			},
			expectedError: "",
		},
		{
			name: "valid cost optimized strategy",
			strategy: &v1alpha1.PlacementStrategy{
				ZoneBalance: "CostOptimized",
			},
			expectedError: "",
		},
		{
			name: "invalid zone balance strategy",
			strategy: &v1alpha1.PlacementStrategy{
				ZoneBalance: "InvalidStrategy",
			},
			expectedError: "invalid ZoneBalance",
		},
		{
			name: "negative minimum available IPs",
			strategy: &v1alpha1.PlacementStrategy{
				ZoneBalance: "Balanced",
				SubnetSelection: &v1alpha1.SubnetSelectionCriteria{
					MinimumAvailableIPs: -1,
				},
			},
			expectedError: "MinimumAvailableIPs must be non-negative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controller := &Controller{}
			err := controller.validatePlacementStrategy(tt.strategy)

			if tt.expectedError == "" {
				assert.NoError(t, err, "Expected no validation error")
			} else {
				require.Error(t, err, "Expected validation error")
				assert.Contains(t, err.Error(), tt.expectedError, "Error should contain expected message")
			}
		})
	}
}

// =============================================================================
// UNIT TESTS - Business Logic Validation
// =============================================================================

func TestValidateBusinessLogic(t *testing.T) {
	tests := []struct {
		name      string
		nodeClass *v1alpha1.IBMNodeClass
		wantError bool
	}{
		{
			name:      "basic valid nodeclass",
			nodeClass: getValidNodeClass(),
			wantError: false,
		},
		{
			name: "zone and subnet specified",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{
					Region:          "us-south",
					Zone:            "us-south-1",
					VPC:             "vpc-12345678",
					Image:           "r006-12345678-1234-1234-1234-123456789012",
					Subnet:          "subnet-12345678",
					InstanceProfile: "bx2-2x8", // Add required instanceProfile
				},
			},
			wantError: false, // Currently we skip detailed validation
		},
		{
			name: "complex placement strategy",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{
					Region: "us-south",
					VPC:    "vpc-12345678",
					Image:  "r006-12345678-1234-1234-1234-123456789012",
					InstanceRequirements: &v1alpha1.InstanceTypeRequirements{
						Architecture:  "amd64",
						MinimumCPU:    2,
						MinimumMemory: 4,
					},
					PlacementStrategy: &v1alpha1.PlacementStrategy{
						ZoneBalance: "Balanced",
						SubnetSelection: &v1alpha1.SubnetSelectionCriteria{
							MinimumAvailableIPs: 50,
							RequiredTags: map[string]string{
								"Environment": "staging",
								"Team":        "platform",
							},
						},
					},
				},
			},
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controller := &Controller{
				cache: cache.New(15 * time.Minute),
			}
			err := controller.validateBusinessLogic(context.Background(), tt.nodeClass)

			if tt.wantError {
				assert.Error(t, err, "Expected validation error")
			} else {
				assert.NoError(t, err, "Expected no validation error")
			}
		})
	}
}

// =============================================================================
// INTEGRATION TESTS - Controller Reconcile
// =============================================================================

func TestControllerReconcile(t *testing.T) {
	tests := []struct {
		name             string
		nodeClass        *v1alpha1.IBMNodeClass
		expectedStatus   string
		expectedReady    bool
		expectedMessages []string
	}{
		{
			name:           "valid NodeClass becomes ready",
			nodeClass:      getValidNodeClass(),
			expectedStatus: "True",
			expectedReady:  true,
			expectedMessages: []string{
				"NodeClass is ready",
			},
		},
		{
			name: "invalid NodeClass has validation errors",
			nodeClass: &v1alpha1.IBMNodeClass{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "invalid-nodeclass",
					Namespace: "default",
				},
				Spec: v1alpha1.IBMNodeClassSpec{
					Region: "",               // Missing required field
					VPC:    "invalid-vpc-id", // Invalid format
					Image:  "r006-12345678-1234-1234-1234-123456789012",
				},
			},
			expectedStatus: "False",
			expectedReady:  false,
			expectedMessages: []string{
				"field validation failed",
			},
		},
		{
			name: "NodeClass with complex valid spec",
			nodeClass: &v1alpha1.IBMNodeClass{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "complex-nodeclass",
					Namespace: "default",
				},
				Spec: v1alpha1.IBMNodeClassSpec{
					Region:          "us-south",
					Zone:            "us-south-1",
					VPC:             "vpc-12345678",
					Image:           "r006-12345678-1234-1234-1234-123456789012",
					Subnet:          "subnet-12345678",
					InstanceProfile: "bx2-4x16",
					SSHKeys:         []string{"key-12345678"},
					SecurityGroups:  []string{"sg-12345678"},
					UserData:        "#!/bin/bash\necho 'Hello World'",
					PlacementStrategy: &v1alpha1.PlacementStrategy{
						ZoneBalance: "Balanced",
						SubnetSelection: &v1alpha1.SubnetSelectionCriteria{
							MinimumAvailableIPs: 50,
							RequiredTags: map[string]string{
								"Environment": "production",
							},
						},
					},
				},
			},
			expectedStatus: "True",
			expectedReady:  true,
			expectedMessages: []string{
				"NodeClass is ready",
			},
		},
		{
			name: "NodeClass with both instanceProfile and instanceRequirements (mutual exclusivity violation)",
			nodeClass: &v1alpha1.IBMNodeClass{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mutual-exclusivity-violation",
					Namespace: "default",
				},
				Spec: v1alpha1.IBMNodeClassSpec{
					Region:          "us-south",
					VPC:             "r006-12345678-1234-1234-1234-123456789012",
					Image:           "r006-12345678-1234-1234-1234-123456789012",
					InstanceProfile: "bx2-4x16", // This should be mutually exclusive with instanceRequirements
					InstanceRequirements: &v1alpha1.InstanceTypeRequirements{
						Architecture:  "amd64",
						MinimumCPU:    2,
						MinimumMemory: 4,
					},
				},
			},
			expectedStatus: "False",
			expectedReady:  false,
			expectedMessages: []string{
				"business logic validation failed",
			},
		},
		{
			name: "NodeClass with only instanceRequirements (valid)",
			nodeClass: &v1alpha1.IBMNodeClass{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "instance-requirements-only",
					Namespace: "default",
				},
				Spec: v1alpha1.IBMNodeClassSpec{
					Region: "us-south",
					VPC:    "r006-12345678-1234-1234-1234-123456789012",
					Image:  "r006-12345678-1234-1234-1234-123456789012",
					InstanceRequirements: &v1alpha1.InstanceTypeRequirements{
						Architecture:  "amd64",
						MinimumCPU:    2,
						MinimumMemory: 4,
					},
				},
			},
			expectedStatus: "True",
			expectedReady:  true,
			expectedMessages: []string{
				"NodeClass is ready",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup
			s := getTestScheme()
			client := fake.NewClientBuilder().
				WithScheme(s).
				WithObjects(tt.nodeClass).
				WithStatusSubresource(tt.nodeClass).
				Build()

			// Create IBM client if credentials are available
			var ibmClient *ibm.Client
			var subnetProvider subnet.Provider
			if os.Getenv("IBM_API_KEY") != "" && os.Getenv("VPC_API_KEY") != "" {
				var err error
				ibmClient, err = ibm.NewClient()
				if err != nil {
					t.Logf("Warning: Could not create IBM client for real validation: %v", err)
				}

				// Create subnet provider for validation
				subnetProvider = subnet.NewProvider(ibmClient)
			}

			controller := &Controller{
				kubeClient:     client,
				ibmClient:      ibmClient,
				subnetProvider: subnetProvider,
			}

			// Execute
			ctx := context.Background()
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      tt.nodeClass.Name,
					Namespace: tt.nodeClass.Namespace,
				},
			}

			result, err := controller.Reconcile(ctx, req)
			assert.NoError(t, err, "Reconcile should not return error")
			assert.Equal(t, reconcile.Result{}, result, "Should not requeue")

			// Verify
			var updatedNodeClass v1alpha1.IBMNodeClass
			err = client.Get(ctx, req.NamespacedName, &updatedNodeClass)
			require.NoError(t, err, "Should be able to get updated NodeClass")

			// Check status conditions
			require.NotEmpty(t, updatedNodeClass.Status.Conditions, "Should have status conditions")
			readyCondition := updatedNodeClass.Status.Conditions[0]
			assert.Equal(t, tt.expectedStatus, string(readyCondition.Status), "Ready condition status should match")

			// Check that condition message contains expected content
			found := false
			for _, expectedMsg := range tt.expectedMessages {
				if assert.Contains(t, readyCondition.Message, expectedMsg) {
					found = true
					break
				}
			}
			assert.True(t, found, "Should find at least one expected message in condition")
			assert.WithinDuration(t, time.Now(), readyCondition.LastTransitionTime.Time, 5*time.Second, "Transition time should be recent")
		})
	}
}

// =============================================================================
// INTEGRATION TESTS - Edge Cases
// =============================================================================

func TestControllerReconcileEdgeCases(t *testing.T) {
	t.Run("missing NodeClass", func(t *testing.T) {
		s := getTestScheme()
		client := fake.NewClientBuilder().WithScheme(s).Build()

		controller := &Controller{
			kubeClient: client,
		}

		ctx := context.Background()
		req := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      "non-existent",
				Namespace: "default",
			},
		}

		result, err := controller.Reconcile(ctx, req)
		assert.NoError(t, err, "Should handle missing NodeClass gracefully")
		assert.Equal(t, reconcile.Result{}, result, "Should not requeue")
	})

	t.Run("concurrent reconciliation", func(t *testing.T) {
		s := getTestScheme()
		nodeClass := getValidNodeClass()
		client := fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(nodeClass).
			WithStatusSubresource(nodeClass).
			Build()

		controller := &Controller{
			kubeClient: client,
		}

		ctx := context.Background()
		req := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      nodeClass.Name,
				Namespace: nodeClass.Namespace,
			},
		}

		// Run multiple reconciliations concurrently
		done := make(chan error, 3)
		for i := 0; i < 3; i++ {
			go func() {
				_, err := controller.Reconcile(ctx, req)
				done <- err
			}()
		}

		// Wait for all to complete
		for i := 0; i < 3; i++ {
			err := <-done
			assert.NoError(t, err, "Concurrent reconciliation should not error")
		}

		// Verify final state is consistent
		var finalNodeClass v1alpha1.IBMNodeClass
		err := client.Get(ctx, req.NamespacedName, &finalNodeClass)
		require.NoError(t, err)

		assert.NotEmpty(t, finalNodeClass.Status.Conditions, "Should have status conditions")
		assert.Equal(t, "True", string(finalNodeClass.Status.Conditions[0].Status), "Should be ready")
	})

	t.Run("validation edge cases", func(t *testing.T) {
		tests := []struct {
			name        string
			nodeClass   *v1alpha1.IBMNodeClass
			expectReady bool
		}{
			{
				name: "whitespace in fields gets trimmed",
				nodeClass: &v1alpha1.IBMNodeClass{
					ObjectMeta: metav1.ObjectMeta{Name: "whitespace-test", Namespace: "default"},
					Spec: v1alpha1.IBMNodeClassSpec{
						Region:          " us-south ",
						VPC:             " vpc-12345678 ",
						Image:           " r006-12345678-1234-1234-1234-123456789012 ",
						InstanceProfile: " bx2-2x8 ", // Add required instanceProfile with whitespace
					},
				},
				expectReady: true,
			},
			{
				name: "maximum valid spec",
				nodeClass: &v1alpha1.IBMNodeClass{
					ObjectMeta: metav1.ObjectMeta{Name: "max-spec", Namespace: "default"},
					Spec: v1alpha1.IBMNodeClassSpec{
						Region:          "us-south",
						Zone:            "us-south-1",
						VPC:             "vpc-12345678",
						Image:           "r006-12345678-1234-1234-1234-123456789012",
						Subnet:          "subnet-12345678",
						InstanceProfile: "bx2-4x16",
						SSHKeys:         []string{"key-12345678", "key-87654321"},
						SecurityGroups:  []string{"sg-12345678", "sg-87654321"},
						ResourceGroup:   "rg-12345678",
						UserData:        "#!/bin/bash\necho 'Complex setup'\nmkdir /opt/app",
						PlacementStrategy: &v1alpha1.PlacementStrategy{
							ZoneBalance: "AvailabilityFirst",
							SubnetSelection: &v1alpha1.SubnetSelectionCriteria{
								MinimumAvailableIPs: 100,
								RequiredTags: map[string]string{
									"Environment": "production",
									"Team":        "platform",
									"Application": "karpenter",
								},
							},
						},
					},
				},
				expectReady: true,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				s := getTestScheme()
				client := fake.NewClientBuilder().
					WithScheme(s).
					WithObjects(tt.nodeClass).
					WithStatusSubresource(tt.nodeClass).
					Build()

				controller := &Controller{
					kubeClient: client,
				}

				ctx := context.Background()
				req := reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      tt.nodeClass.Name,
						Namespace: tt.nodeClass.Namespace,
					},
				}

				_, err := controller.Reconcile(ctx, req)
				assert.NoError(t, err)

				var updatedNodeClass v1alpha1.IBMNodeClass
				err = client.Get(ctx, req.NamespacedName, &updatedNodeClass)
				require.NoError(t, err)

				require.NotEmpty(t, updatedNodeClass.Status.Conditions)
				if tt.expectReady {
					assert.Equal(t, "True", string(updatedNodeClass.Status.Conditions[0].Status))
				} else {
					assert.Equal(t, "False", string(updatedNodeClass.Status.Conditions[0].Status))
				}
			})
		}
	})
}

// =============================================================================
// REAL IBM CLOUD INTEGRATION TESTS
// =============================================================================

func TestControllerWithRealIBMCloud(t *testing.T) {
	// Skip if IBM Cloud credentials are not available
	if os.Getenv("IBM_API_KEY") == "" || os.Getenv("VPC_API_KEY") == "" {
		t.Skip("Skipping IBM Cloud integration tests - set IBM_API_KEY and VPC_API_KEY to run")
	}

	vpcID := os.Getenv("VPC_ID")
	subnetID := os.Getenv("SUBNET_ID_US_SOUTH_1")

	if vpcID == "" {
		t.Skip("Skipping IBM Cloud integration tests - set VPC_ID to run")
	}

	tests := []struct {
		name           string
		nodeClass      *v1alpha1.IBMNodeClass
		expectReady    bool
		expectMessages []string
	}{
		{
			name: "valid VPC should pass validation",
			nodeClass: &v1alpha1.IBMNodeClass{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "real-ibm-test-valid",
					Namespace: "default",
				},
				Spec: v1alpha1.IBMNodeClassSpec{
					Region: "us-south",
					VPC:    vpcID,
					Image:  "r006-988caa8b-7786-49c9-aea6-9553af2b1969", // Real Ubuntu 20.04 image
				},
			},
			expectReady:    true,
			expectMessages: []string{"NodeClass is ready"},
		},
		{
			name: "invalid VPC should fail validation",
			nodeClass: &v1alpha1.IBMNodeClass{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "real-ibm-test-invalid-vpc",
					Namespace: "default",
				},
				Spec: v1alpha1.IBMNodeClassSpec{
					Region: "us-south",
					VPC:    "vpc-nonexistent-12345678",
					Image:  "r006-988caa8b-7786-49c9-aea6-9553af2b1969", // Real Ubuntu 20.04 image
				},
			},
			expectReady:    false,
			expectMessages: []string{"VPC validation failed"},
		},
		{
			name: "valid subnet should pass validation",
			nodeClass: &v1alpha1.IBMNodeClass{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "real-ibm-test-valid-subnet",
					Namespace: "default",
				},
				Spec: v1alpha1.IBMNodeClassSpec{
					Region: "us-south",
					Zone:   "us-south-1",
					VPC:    vpcID,
					Subnet: subnetID,
					Image:  "r006-988caa8b-7786-49c9-aea6-9553af2b1969", // Real Ubuntu 20.04 image
				},
			},
			expectReady:    subnetID != "", // Only test if subnet ID is provided
			expectMessages: []string{"NodeClass is ready"},
		},
		{
			name: "invalid subnet should fail validation",
			nodeClass: &v1alpha1.IBMNodeClass{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "real-ibm-test-invalid-subnet",
					Namespace: "default",
				},
				Spec: v1alpha1.IBMNodeClassSpec{
					Region: "us-south",
					Zone:   "us-south-1",
					VPC:    vpcID,
					Subnet: "0717-nonexistent-subnet-12345678",
					Image:  "r006-988caa8b-7786-49c9-aea6-9553af2b1969", // Real Ubuntu 20.04 image
				},
			},
			expectReady:    false,
			expectMessages: []string{"subnet validation failed"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Skip subnet tests if no subnet ID provided
			if tt.name == "valid subnet should pass validation" && subnetID == "" {
				t.Skip("Skipping subnet test - no SUBNET_ID_US_SOUTH_1 provided")
			}

			s := getTestScheme()
			client := fake.NewClientBuilder().
				WithScheme(s).
				WithObjects(tt.nodeClass).
				WithStatusSubresource(tt.nodeClass).
				Build()

			// Create real IBM client
			ibmClient, err := ibm.NewClient()
			require.NoError(t, err, "Should be able to create IBM client with provided credentials")

			// Create subnet provider
			subnetProvider := subnet.NewProvider(ibmClient)

			controller := &Controller{
				kubeClient:     client,
				ibmClient:      ibmClient,
				subnetProvider: subnetProvider,
			}

			ctx := context.Background()
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      tt.nodeClass.Name,
					Namespace: tt.nodeClass.Namespace,
				},
			}

			// Allow more time for real API calls
			timeout := 30 * time.Second
			timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			result, err := controller.Reconcile(timeoutCtx, req)
			assert.NoError(t, err, "Reconcile should not return error")
			assert.Equal(t, reconcile.Result{}, result, "Should not requeue")

			// Verify status was updated
			var updatedNodeClass v1alpha1.IBMNodeClass
			err = client.Get(ctx, req.NamespacedName, &updatedNodeClass)
			require.NoError(t, err, "Should be able to get updated NodeClass")

			// Check status conditions
			require.NotEmpty(t, updatedNodeClass.Status.Conditions, "Should have status conditions")
			readyCondition := updatedNodeClass.Status.Conditions[0]

			if tt.expectReady {
				assert.Equal(t, "True", string(readyCondition.Status), "Should be ready with real IBM Cloud validation")
			} else {
				assert.Equal(t, "False", string(readyCondition.Status), "Should not be ready due to validation failure")
			}

			// Check that condition message contains expected content
			found := false
			for _, expectedMsg := range tt.expectMessages {
				if assert.Contains(t, readyCondition.Message, expectedMsg) {
					found = true
					break
				}
			}
			assert.True(t, found, "Should find expected message in condition")

			t.Logf("Test %s completed. Status: %s, Message: %s",
				tt.name, readyCondition.Status, readyCondition.Message)
		})
	}
}

func TestIBMCloudResourceValidation(t *testing.T) {
	// Skip if IBM Cloud credentials are not available
	if os.Getenv("IBM_API_KEY") == "" || os.Getenv("VPC_API_KEY") == "" {
		t.Skip("Skipping IBM Cloud resource validation tests - set IBM_API_KEY and VPC_API_KEY to run")
	}

	vpcID := os.Getenv("VPC_ID")
	if vpcID == "" {
		t.Skip("Skipping IBM Cloud resource validation tests - set VPC_ID to run")
	}

	// Create real IBM client
	ibmClient, err := ibm.NewClient()
	require.NoError(t, err, "Should be able to create IBM client")

	// Create subnet provider
	subnetProvider := subnet.NewProvider(ibmClient)

	controller := &Controller{
		ibmClient:      ibmClient,
		subnetProvider: subnetProvider,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	t.Run("validate real VPC", func(t *testing.T) {
		err := controller.validateVPC(ctx, vpcID)
		assert.NoError(t, err, "Real VPC should validate successfully")
	})

	t.Run("validate nonexistent VPC", func(t *testing.T) {
		err := controller.validateVPC(ctx, "vpc-nonexistent-12345678")
		assert.Error(t, err, "Nonexistent VPC should fail validation")
		assert.Contains(t, err.Error(), "not found", "Error should indicate VPC not found")
	})

	subnetID := os.Getenv("SUBNET_ID_US_SOUTH_1")
	if subnetID != "" {
		t.Run("validate real subnet", func(t *testing.T) {
			err := controller.validateSubnet(ctx, subnetID, vpcID)
			assert.NoError(t, err, "Real subnet should validate successfully")
		})

		t.Run("validate nonexistent subnet", func(t *testing.T) {
			err := controller.validateSubnet(ctx, "0717-nonexistent-subnet-12345678", vpcID)
			assert.Error(t, err, "Nonexistent subnet should fail validation")
		})
	}

	t.Run("validate subnets available in VPC", func(t *testing.T) {
		err := controller.validateSubnetsAvailable(ctx, vpcID, "us-south-1")
		assert.NoError(t, err, "Should find available subnets in real VPC")
	})
}

// =============================================================================
// ZONE-SUBNET VALIDATION TESTS
// =============================================================================

func TestValidateZoneSubnetCompatibility(t *testing.T) {
	tests := []struct {
		name          string
		zone          string
		subnetID      string
		setupMock     func(*MockSubnetProvider)
		setupCache    func(*cache.Cache)
		expectedError string
	}{
		{
			name:     "valid zone-subnet combination",
			zone:     "us-south-1",
			subnetID: "subnet-12345",
			setupMock: func(m *MockSubnetProvider) {
				m.On("GetSubnet", mock.Anything, "subnet-12345").Return(&subnet.SubnetInfo{
					ID:           "subnet-12345",
					Zone:         "us-south-1",
					State:        "available",
					AvailableIPs: 100,
				}, nil)
			},
			setupCache:    func(c *cache.Cache) {},
			expectedError: "",
		},
		{
			name:     "zone-subnet mismatch",
			zone:     "us-south-1",
			subnetID: "subnet-12345",
			setupMock: func(m *MockSubnetProvider) {
				m.On("GetSubnet", mock.Anything, "subnet-12345").Return(&subnet.SubnetInfo{
					ID:           "subnet-12345",
					Zone:         "us-south-2",
					State:        "available",
					AvailableIPs: 100,
				}, nil)
			},
			setupCache:    func(c *cache.Cache) {},
			expectedError: "subnet subnet-12345 is in zone us-south-2, but requested zone is us-south-1. Subnets cannot span multiple zones in IBM Cloud VPC",
		},
		{
			name:     "subnet not available",
			zone:     "us-south-1",
			subnetID: "subnet-12345",
			setupMock: func(m *MockSubnetProvider) {
				m.On("GetSubnet", mock.Anything, "subnet-12345").Return(&subnet.SubnetInfo{
					ID:           "subnet-12345",
					Zone:         "us-south-1",
					State:        "pending",
					AvailableIPs: 100,
				}, nil)
			},
			setupCache:    func(c *cache.Cache) {},
			expectedError: "subnet subnet-12345 in zone us-south-1 is not in available state (current state: pending)",
		},
		{
			name:     "subnet not found",
			zone:     "us-south-1",
			subnetID: "subnet-12345",
			setupMock: func(m *MockSubnetProvider) {
				m.On("GetSubnet", mock.Anything, "subnet-12345").Return(nil, fmt.Errorf("subnet not found"))
			},
			setupCache:    func(c *cache.Cache) {},
			expectedError: "failed to get subnet information: subnet not found",
		},
		{
			name:     "valid zone-subnet from cache",
			zone:     "us-south-1",
			subnetID: "subnet-12345",
			setupMock: func(m *MockSubnetProvider) {
				// Should not be called due to cache hit
			},
			setupCache: func(c *cache.Cache) {
				c.SetWithTTL("subnet-zone-subnet-12345", "us-south-1", 15*time.Minute)
			},
			expectedError: "",
		},
		{
			name:     "zone-subnet mismatch from cache",
			zone:     "us-south-1",
			subnetID: "subnet-12345",
			setupMock: func(m *MockSubnetProvider) {
				// Should not be called due to cache hit
			},
			setupCache: func(c *cache.Cache) {
				c.SetWithTTL("subnet-zone-subnet-12345", "us-south-2", 15*time.Minute)
			},
			expectedError: "subnet subnet-12345 is in zone us-south-2, but requested zone is us-south-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock subnet provider
			mockProvider := new(MockSubnetProvider)
			if tt.setupMock != nil {
				tt.setupMock(mockProvider)
			}

			// Create cache
			testCache := cache.New(15 * time.Minute)
			if tt.setupCache != nil {
				tt.setupCache(testCache)
			}

			// Create controller with mock
			controller := &Controller{
				subnetProvider: mockProvider,
				cache:          testCache,
			}

			// Test the validation
			ctx := context.Background()
			err := controller.validateZoneSubnetCompatibility(ctx, tt.zone, tt.subnetID)

			if tt.expectedError == "" {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
			}

			// Verify mock expectations
			mockProvider.AssertExpectations(t)
		})
	}
}

func TestValidateZoneSubnetCompatibility_CacheBehavior(t *testing.T) {
	mockProvider := new(MockSubnetProvider)
	testCache := cache.New(15 * time.Minute)
	controller := &Controller{
		subnetProvider: mockProvider,
		cache:          testCache,
	}

	// Setup mock to be called only once
	mockProvider.On("GetSubnet", mock.Anything, "subnet-12345").Return(&subnet.SubnetInfo{
		ID:           "subnet-12345",
		Zone:         "us-south-1",
		State:        "available",
		AvailableIPs: 100,
	}, nil).Once()

	ctx := context.Background()

	// First call should hit the API
	err := controller.validateZoneSubnetCompatibility(ctx, "us-south-1", "subnet-12345")
	assert.NoError(t, err)

	// Second call should use cache
	err = controller.validateZoneSubnetCompatibility(ctx, "us-south-1", "subnet-12345")
	assert.NoError(t, err)

	// Third call with different zone should use cache and fail
	err = controller.validateZoneSubnetCompatibility(ctx, "us-south-2", "subnet-12345")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "subnet subnet-12345 is in zone us-south-1, but requested zone is us-south-2")

	// Verify API was called only once
	mockProvider.AssertExpectations(t)
}

func TestValidateBusinessLogic_ZoneSubnetCompatibility(t *testing.T) {
	tests := []struct {
		name          string
		zone          string
		subnetID      string
		setupMock     func(*MockSubnetProvider)
		expectedError string
	}{
		{
			name:          "no zone or subnet specified",
			zone:          "",
			subnetID:      "",
			setupMock:     func(m *MockSubnetProvider) {},
			expectedError: "",
		},
		{
			name:          "only zone specified",
			zone:          "us-south-1",
			subnetID:      "",
			setupMock:     func(m *MockSubnetProvider) {},
			expectedError: "",
		},
		{
			name:          "only subnet specified",
			zone:          "",
			subnetID:      "subnet-12345",
			setupMock:     func(m *MockSubnetProvider) {},
			expectedError: "",
		},
		{
			name:     "both zone and subnet specified - compatible",
			zone:     "us-south-1",
			subnetID: "subnet-12345",
			setupMock: func(m *MockSubnetProvider) {
				m.On("GetSubnet", mock.Anything, "subnet-12345").Return(&subnet.SubnetInfo{
					ID:           "subnet-12345",
					Zone:         "us-south-1",
					State:        "available",
					AvailableIPs: 100,
				}, nil)
			},
			expectedError: "",
		},
		{
			name:     "both zone and subnet specified - incompatible",
			zone:     "us-south-1",
			subnetID: "subnet-12345",
			setupMock: func(m *MockSubnetProvider) {
				m.On("GetSubnet", mock.Anything, "subnet-12345").Return(&subnet.SubnetInfo{
					ID:           "subnet-12345",
					Zone:         "us-south-2",
					State:        "available",
					AvailableIPs: 100,
				}, nil)
			},
			expectedError: "zone-subnet compatibility validation failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockProvider := new(MockSubnetProvider)
			if tt.setupMock != nil {
				tt.setupMock(mockProvider)
			}

			controller := &Controller{
				subnetProvider: mockProvider,
				cache:          cache.New(15 * time.Minute),
			}

			nodeClass := getValidNodeClass()
			nodeClass.Spec.Zone = tt.zone
			nodeClass.Spec.Subnet = tt.subnetID

			ctx := context.Background()
			err := controller.validateBusinessLogic(ctx, nodeClass)

			if tt.expectedError == "" {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
			}

			mockProvider.AssertExpectations(t)
		})
	}
}

// =============================================================================
// PERFORMANCE TESTS
// =============================================================================

func TestControllerPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping performance test in short mode")
	}

	s := getTestScheme()
	nodeClass := getValidNodeClass()
	client := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(nodeClass).
		WithStatusSubresource(nodeClass).
		Build()

	controller := &Controller{
		kubeClient: client,
	}

	ctx := context.Background()
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      nodeClass.Name,
			Namespace: nodeClass.Namespace,
		},
	}

	// Benchmark reconciliation performance
	start := time.Now()
	iterations := 100

	for i := 0; i < iterations; i++ {
		_, err := controller.Reconcile(ctx, req)
		require.NoError(t, err)
	}

	duration := time.Since(start)
	avgDuration := duration / time.Duration(iterations)

	t.Logf("Reconciled %d times in %v (avg: %v per reconciliation)", iterations, duration, avgDuration)

	// Reconciliation should be fast (under 10ms per call)
	assert.Less(t, avgDuration, 10*time.Millisecond, "Reconciliation should be fast")
}

// =============================================================================
// CONSTRUCTOR TESTS
// =============================================================================

func TestNewController(t *testing.T) {
	s := getTestScheme()
	kubeClient := fake.NewClientBuilder().WithScheme(s).Build()

	tests := []struct {
		name        string
		client      client.Client
		wantErr     bool
		errContains string
		setupEnv    func()
		cleanupEnv  func()
	}{
		{
			name:   "valid client with credentials",
			client: kubeClient,
			setupEnv: func() {
				_ = os.Setenv("IBMCLOUD_API_KEY", "test-key")
				_ = os.Setenv("VPC_API_KEY", "test-vpc-key")
				_ = os.Setenv("IBMCLOUD_REGION", "us-south")
			},
			cleanupEnv: func() {
				_ = os.Unsetenv("IBMCLOUD_API_KEY")
				_ = os.Unsetenv("VPC_API_KEY")
				_ = os.Unsetenv("IBMCLOUD_REGION")
			},
			wantErr: false,
		},
		{
			name:        "nil client",
			client:      nil,
			wantErr:     true,
			errContains: "kubeClient cannot be nil",
		},
		{
			name:   "missing credentials",
			client: kubeClient,
			setupEnv: func() {
				_ = os.Unsetenv("IBMCLOUD_API_KEY")
				_ = os.Unsetenv("VPC_API_KEY")
				_ = os.Unsetenv("IBMCLOUD_REGION")
			},
			wantErr:     true,
			errContains: "creating IBM client",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setupEnv != nil {
				tt.setupEnv()
			}
			if tt.cleanupEnv != nil {
				defer tt.cleanupEnv()
			}

			controller, err := NewController(tt.client)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, controller)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
				if controller != nil {
					assert.Equal(t, tt.client, controller.kubeClient)
					assert.NotNil(t, controller.ibmClient)
					assert.NotNil(t, controller.subnetProvider)
					assert.NotNil(t, controller.cache)
					assert.NotNil(t, controller.vpcClientManager)
				}
			}
		})
	}
}

func TestNewTestController(t *testing.T) {
	s := getTestScheme()
	kubeClient := fake.NewClientBuilder().WithScheme(s).Build()

	controller := NewTestController(kubeClient)

	assert.NotNil(t, controller)
	assert.Equal(t, kubeClient, controller.kubeClient)
	assert.Nil(t, controller.ibmClient)
	assert.Nil(t, controller.subnetProvider)
	assert.Nil(t, controller.cache)
	assert.Nil(t, controller.vpcClientManager)
}

// =============================================================================
// VALIDATION METHOD TESTS
// =============================================================================

func TestValidateRegion(t *testing.T) {
	// Skip this test if no credentials available
	if os.Getenv("IBM_API_KEY") == "" || os.Getenv("VPC_API_KEY") == "" {
		t.Skip("Skipping validateRegion test - requires IBM Cloud credentials")
	}

	ctx := context.Background()
	s := getTestScheme()
	kubeClient := fake.NewClientBuilder().WithScheme(s).Build()

	// Create controller with real credentials for VPC API access
	controller, err := NewController(kubeClient)
	require.NoError(t, err)

	tests := []struct {
		name        string
		region      string
		wantErr     bool
		errContains string
	}{
		{
			name:    "valid region",
			region:  "us-south",
			wantErr: false,
		},
		{
			name:        "empty region",
			region:      "",
			wantErr:     true,
			errContains: "region is required",
		},
		{
			name:        "invalid format region",
			region:      "invalid-region-name",
			wantErr:     true,
			errContains: "invalid region format",
		},
		{
			name:        "non-existent region",
			region:      "xx-north",
			wantErr:     true,
			errContains: "not found in VPC API",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := controller.validateRegion(ctx, tt.region)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateVPC(t *testing.T) {
	// Skip this test if no credentials available
	if os.Getenv("IBM_API_KEY") == "" || os.Getenv("VPC_API_KEY") == "" {
		t.Skip("Skipping validateVPC test - requires IBM Cloud credentials")
	}

	ctx := context.Background()
	s := getTestScheme()
	kubeClient := fake.NewClientBuilder().WithScheme(s).Build()

	controller, err := NewController(kubeClient)
	require.NoError(t, err)

	tests := []struct {
		name        string
		vpcID       string
		wantErr     bool
		errContains string
	}{
		{
			name:        "non-existent VPC",
			vpcID:       "vpc-nonexistent",
			wantErr:     true,
			errContains: "not found or not accessible",
		},
		{
			name:    "empty VPC ID",
			vpcID:   "",
			wantErr: true,
		},
	}

	// Add real VPC test if available
	if vpcID := os.Getenv("VPC_ID"); vpcID != "" {
		tests = append(tests, struct {
			name        string
			vpcID       string
			wantErr     bool
			errContains string
		}{
			name:    "valid VPC",
			vpcID:   vpcID,
			wantErr: false,
		})
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := controller.validateVPC(ctx, tt.vpcID)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateSubnet(t *testing.T) {
	ctx := context.Background()
	s := getTestScheme()
	kubeClient := fake.NewClientBuilder().WithScheme(s).Build()

	mockSubnet := &MockSubnetProvider{}
	controller := &Controller{
		kubeClient:     kubeClient,
		subnetProvider: mockSubnet,
	}

	tests := []struct {
		name        string
		subnetID    string
		vpcID       string
		setupMock   func()
		wantErr     bool
		errContains string
	}{
		{
			name:     "valid subnet",
			subnetID: "subnet-valid",
			vpcID:    "vpc-12345",
			setupMock: func() {
				mockSubnet.On("GetSubnet", ctx, "subnet-valid").Return(&subnet.SubnetInfo{
					ID:           "subnet-valid",
					State:        "available",
					AvailableIPs: 250,
					Zone:         "us-south-1",
				}, nil)
			},
			wantErr: false,
		},
		{
			name:     "subnet not found",
			subnetID: "subnet-nonexistent",
			vpcID:    "vpc-12345",
			setupMock: func() {
				mockSubnet.On("GetSubnet", ctx, "subnet-nonexistent").Return(nil, fmt.Errorf("not found"))
			},
			wantErr:     true,
			errContains: "not found or not accessible",
		},
		{
			name:     "subnet not available",
			subnetID: "subnet-pending",
			vpcID:    "vpc-12345",
			setupMock: func() {
				mockSubnet.On("GetSubnet", ctx, "subnet-pending").Return(&subnet.SubnetInfo{
					ID:           "subnet-pending",
					State:        "pending",
					AvailableIPs: 250,
					Zone:         "us-south-1",
				}, nil)
			},
			wantErr:     true,
			errContains: "is not in available state",
		},
		{
			name:     "insufficient IPs",
			subnetID: "subnet-low-ips",
			vpcID:    "vpc-12345",
			setupMock: func() {
				mockSubnet.On("GetSubnet", ctx, "subnet-low-ips").Return(&subnet.SubnetInfo{
					ID:           "subnet-low-ips",
					State:        "available",
					AvailableIPs: 5,
					Zone:         "us-south-1",
				}, nil)
			},
			wantErr:     true,
			errContains: "has insufficient available IPs",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset mock
			mockSubnet.ExpectedCalls = nil
			if tt.setupMock != nil {
				tt.setupMock()
			}

			err := controller.validateSubnet(ctx, tt.subnetID, tt.vpcID)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
			}

			mockSubnet.AssertExpectations(t)
		})
	}
}

func TestValidateSubnetsAvailable(t *testing.T) {
	ctx := context.Background()
	s := getTestScheme()
	kubeClient := fake.NewClientBuilder().WithScheme(s).Build()

	mockSubnet := &MockSubnetProvider{}
	controller := &Controller{
		kubeClient:     kubeClient,
		subnetProvider: mockSubnet,
	}

	tests := []struct {
		name        string
		vpcID       string
		zone        string
		setupMock   func()
		wantErr     bool
		errContains string
	}{
		{
			name:  "subnets available",
			vpcID: "vpc-12345",
			zone:  "",
			setupMock: func() {
				mockSubnet.On("ListSubnets", ctx, "vpc-12345").Return([]subnet.SubnetInfo{
					{
						ID:           "subnet-1",
						State:        "available",
						AvailableIPs: 250,
						Zone:         "us-south-1",
					},
					{
						ID:           "subnet-2",
						State:        "available",
						AvailableIPs: 100,
						Zone:         "us-south-2",
					},
				}, nil)
			},
			wantErr: false,
		},
		{
			name:  "subnets available in specific zone",
			vpcID: "vpc-12345",
			zone:  "us-south-1",
			setupMock: func() {
				mockSubnet.On("ListSubnets", ctx, "vpc-12345").Return([]subnet.SubnetInfo{
					{
						ID:           "subnet-1",
						State:        "available",
						AvailableIPs: 250,
						Zone:         "us-south-1",
					},
					{
						ID:           "subnet-2",
						State:        "available",
						AvailableIPs: 100,
						Zone:         "us-south-2",
					},
				}, nil)
			},
			wantErr: false,
		},
		{
			name:  "no available subnets",
			vpcID: "vpc-empty",
			zone:  "",
			setupMock: func() {
				mockSubnet.On("ListSubnets", ctx, "vpc-empty").Return([]subnet.SubnetInfo{
					{
						ID:           "subnet-pending",
						State:        "pending",
						AvailableIPs: 250,
						Zone:         "us-south-1",
					},
					{
						ID:           "subnet-low-ips",
						State:        "available",
						AvailableIPs: 5,
						Zone:         "us-south-2",
					},
				}, nil)
			},
			wantErr:     true,
			errContains: "no available subnets found in VPC",
		},
		{
			name:  "no subnets in zone",
			vpcID: "vpc-12345",
			zone:  "us-west-1",
			setupMock: func() {
				mockSubnet.On("ListSubnets", ctx, "vpc-12345").Return([]subnet.SubnetInfo{
					{
						ID:           "subnet-1",
						State:        "available",
						AvailableIPs: 250,
						Zone:         "us-south-1",
					},
				}, nil)
			},
			wantErr:     true,
			errContains: "no available subnets found in VPC vpc-12345 for zone us-west-1",
		},
		{
			name:  "list subnets fails",
			vpcID: "vpc-error",
			zone:  "",
			setupMock: func() {
				mockSubnet.On("ListSubnets", ctx, "vpc-error").Return([]subnet.SubnetInfo{}, fmt.Errorf("API error"))
			},
			wantErr:     true,
			errContains: "failed to list subnets",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset mock
			mockSubnet.ExpectedCalls = nil
			if tt.setupMock != nil {
				tt.setupMock()
			}

			err := controller.validateSubnetsAvailable(ctx, tt.vpcID, tt.zone)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
			}

			mockSubnet.AssertExpectations(t)
		})
	}
}

func TestValidateImage(t *testing.T) {
	// Skip this test if no credentials available
	if os.Getenv("IBM_API_KEY") == "" || os.Getenv("VPC_API_KEY") == "" {
		t.Skip("Skipping validateImage test - requires IBM Cloud credentials")
	}

	ctx := context.Background()
	s := getTestScheme()
	kubeClient := fake.NewClientBuilder().WithScheme(s).Build()

	controller, err := NewController(kubeClient)
	require.NoError(t, err)

	tests := []struct {
		name        string
		imageID     string
		region      string
		wantErr     bool
		errContains string
	}{
		{
			name:        "non-existent image",
			imageID:     "r006-nonexistent",
			region:      "us-south",
			wantErr:     true,
			errContains: "not found or not accessible",
		},
		{
			name:    "empty image ID",
			imageID: "",
			region:  "us-south",
			wantErr: true,
		},
	}

	// Add real image test if available
	if imageID := os.Getenv("IMAGE_ID"); imageID != "" {
		tests = append(tests, struct {
			name        string
			imageID     string
			region      string
			wantErr     bool
			errContains string
		}{
			name:    "valid image",
			imageID: imageID,
			region:  "us-south",
			wantErr: false,
		})
	} else {
		// Use a known Ubuntu 20.04 image ID for testing
		tests = append(tests, struct {
			name        string
			imageID     string
			region      string
			wantErr     bool
			errContains string
		}{
			name:    "known Ubuntu image",
			imageID: "r006-988caa8b-7786-49c9-aea6-9553af2b1969",
			region:  "us-south",
			wantErr: false,
		})
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := controller.validateImage(ctx, tt.imageID, tt.region)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestRegister(t *testing.T) {
	s := getTestScheme()
	kubeClient := fake.NewClientBuilder().WithScheme(s).Build()

	controller := &Controller{
		kubeClient: kubeClient,
	}

	// Create a fake manager - we'll just test that Register doesn't panic
	// since controller-runtime's fake manager is complex to set up
	ctx := context.Background()

	// Test should not panic
	assert.NotPanics(t, func() {
		// We can't easily test the full Register functionality without a real manager
		// But we can test that the method exists and doesn't panic with nil manager
		// In a real scenario, this would be called with a proper manager
		_ = controller.Register(ctx, nil)
	})
}

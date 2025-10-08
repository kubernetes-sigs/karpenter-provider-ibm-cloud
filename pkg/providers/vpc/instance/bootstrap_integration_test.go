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

package instance

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
)

func TestBootstrapIntegration(t *testing.T) {
	// Set test API key to satisfy the requirement
	_ = os.Setenv("IBMCLOUD_API_KEY", "test-api-key")

	tests := []struct {
		name          string
		nodeClass     *v1alpha1.IBMNodeClass
		expectManual  bool
		expectDynamic bool
	}{
		{
			name: "Manual userData provided - should use as-is",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{
					Region:   "us-south",
					UserData: "#!/bin/bash\necho 'Manual bootstrap script'",
				},
			},
			expectManual:  true,
			expectDynamic: false,
		},
		{
			name: "No userData provided - should generate dynamic bootstrap",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{
					Region: "us-south",
					// UserData is empty - should trigger automatic bootstrap
				},
			},
			expectManual:  false,
			expectDynamic: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock IBM client
			mockClient := &ibm.Client{} // In real test, this would be properly mocked

			// Create fake kube client
			kubeClient := fake.NewClientBuilder().Build()

			// Create VPC instance provider
			provider, err := NewVPCInstanceProvider(mockClient, kubeClient)
			assert.NoError(t, err)

			vpcProvider := provider.(*VPCInstanceProvider)

			// Test bootstrap user data generation
			userData, err := vpcProvider.generateBootstrapUserData(
				context.Background(),
				tt.nodeClass,
				types.NamespacedName{Name: "test-node", Namespace: "default"},
			)

			if tt.expectManual {
				// Should return the manual userData with BOOTSTRAP_* variables injected if present
				assert.NoError(t, err)
				assert.Contains(t, userData, "Manual bootstrap script")
				// Verify original content is preserved
				assert.Contains(t, userData, "#!/bin/bash")
			}

			if tt.expectDynamic {
				// Should fail without kubernetes client in test environment
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "failed to")
			}
		})
	}
}

func TestBootstrapBehavior(t *testing.T) {
	// Set test API key to satisfy the requirement
	_ = os.Setenv("IBMCLOUD_API_KEY", "test-api-key")

	// Create mock IBM client
	mockClient := &ibm.Client{}
	kubeClient := fake.NewClientBuilder().Build()

	provider, err := NewVPCInstanceProvider(mockClient, kubeClient)
	assert.NoError(t, err)

	vpcProvider := provider.(*VPCInstanceProvider)

	t.Run("Manual userData takes precedence", func(t *testing.T) {
		nodeClass := &v1alpha1.IBMNodeClass{
			Spec: v1alpha1.IBMNodeClassSpec{
				Region:   "us-south",
				UserData: "#!/bin/bash\necho 'Custom bootstrap'",
			},
		}

		userData, err := vpcProvider.generateBootstrapUserData(
			context.Background(),
			nodeClass,
			types.NamespacedName{Name: "test", Namespace: "default"},
		)

		assert.NoError(t, err)
		// Verify original custom bootstrap content is preserved
		assert.Contains(t, userData, "Custom bootstrap")
		assert.Contains(t, userData, "#!/bin/bash")
	})
}

func TestBootstrapProviderLazyInitialization(t *testing.T) {
	// Set test API key to satisfy the requirement
	_ = os.Setenv("IBMCLOUD_API_KEY", "test-api-key")

	mockClient := &ibm.Client{}
	kubeClient := fake.NewClientBuilder().Build()

	provider, err := NewVPCInstanceProvider(mockClient, kubeClient)
	assert.NoError(t, err)

	vpcProvider := provider.(*VPCInstanceProvider)

	// Initially, bootstrap provider should be nil
	assert.Nil(t, vpcProvider.bootstrapProvider)
	assert.Nil(t, vpcProvider.k8sClient)

	// After calling generateBootstrapUserData with empty userData,
	// it should attempt to initialize (but fail in test environment)
	nodeClass := &v1alpha1.IBMNodeClass{
		Spec: v1alpha1.IBMNodeClassSpec{
			Region: "us-south",
			// Empty userData triggers bootstrap provider initialization
		},
	}

	_, err = vpcProvider.generateBootstrapUserData(
		context.Background(),
		nodeClass,
		types.NamespacedName{Name: "test", Namespace: "default"},
	)

	// In test environment without kubernetes client, this will fail
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create kubernetes client")
}

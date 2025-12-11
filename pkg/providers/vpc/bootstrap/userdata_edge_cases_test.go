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

package bootstrap

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	fakeClient "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
)

func TestVPCBootstrapProvider_GetUserData_ErrorHandling(t *testing.T) {
	client := &ibm.Client{}

	t.Run("Nil NodeClass", func(t *testing.T) {
		//nolint:staticcheck // SA1019: NewSimpleClientset is deprecated but NewClientset requires generated apply configurations
		k8sClient := fake.NewSimpleClientset()
		kubeClient := fakeClient.NewClientBuilder().Build()
		provider := NewVPCBootstrapProvider(client, k8sClient, kubeClient)

		ctx := context.Background()
		nodeClaim := types.NamespacedName{Name: "test-node", Namespace: "default"}

		_, err := provider.GetUserData(ctx, nil, nodeClaim)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "nodeClass cannot be nil")
	})

	t.Run("Empty NodeClaim", func(t *testing.T) {
		//nolint:staticcheck // SA1019: NewSimpleClientset is deprecated but NewClientset requires generated apply configurations
		k8sClient := fake.NewSimpleClientset()
		kubeClient := fakeClient.NewClientBuilder().Build()
		provider := NewVPCBootstrapProvider(client, k8sClient, kubeClient)

		nodeClass := &v1alpha1.IBMNodeClass{
			ObjectMeta: metav1.ObjectMeta{Name: "test-nodeclass"},
			Spec: v1alpha1.IBMNodeClassSpec{
				Region:            "us-south",
				Zone:              "us-south-1",
				APIServerEndpoint: "https://192.168.1.100:6443", // Provide explicit endpoint to avoid discovery failure
			},
		}

		ctx := context.Background()
		emptyNodeClaim := types.NamespacedName{}

		_, err := provider.GetUserData(ctx, nodeClass, emptyNodeClaim)
		// Will fail due to missing CA certificate in fake client setup
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "ca.crt not found")
	})

	t.Run("Kubernetes API server discovery failure", func(t *testing.T) {
		// Create a client that will return no services (simulating API discovery failure)
		//nolint:staticcheck // SA1019: NewSimpleClientset is deprecated but NewClientset requires generated apply configurations
		k8sClient := fake.NewSimpleClientset()
		kubeClient := fakeClient.NewClientBuilder().Build()
		provider := NewVPCBootstrapProvider(client, k8sClient, kubeClient)

		nodeClass := &v1alpha1.IBMNodeClass{
			ObjectMeta: metav1.ObjectMeta{Name: "test-nodeclass"},
			Spec: v1alpha1.IBMNodeClassSpec{
				Region: "us-south",
				Zone:   "us-south-1",
				// No APIServerEndpoint to force discovery
			},
		}

		ctx := context.Background()
		nodeClaim := types.NamespacedName{Name: "test-node", Namespace: "default"}

		_, err := provider.GetUserData(ctx, nodeClass, nodeClaim)
		// Should fail due to inability to discover API server
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "getting internal API server endpoint")
	})

	t.Run("Bootstrap token creation failure", func(t *testing.T) {
		//nolint:staticcheck // SA1019: NewSimpleClientset is deprecated but NewClientset requires generated apply configurations
		k8sClient := fake.NewSimpleClientset()
		kubeClient := fakeClient.NewClientBuilder().Build()
		provider := NewVPCBootstrapProvider(client, k8sClient, kubeClient)

		nodeClass := &v1alpha1.IBMNodeClass{
			ObjectMeta: metav1.ObjectMeta{Name: "test-nodeclass"},
			Spec: v1alpha1.IBMNodeClassSpec{
				Region:            "us-south",
				Zone:              "us-south-1",
				APIServerEndpoint: "https://10.96.0.1:443", // Provide explicit endpoint
			},
		}

		ctx := context.Background()
		nodeClaim := types.NamespacedName{Name: "test-node", Namespace: "default"}

		_, err := provider.GetUserData(ctx, nodeClass, nodeClaim)
		// Will fail due to missing CA certificate in fake client setup
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "ca.crt not found")
	})
}

func TestNewVPCBootstrapProvider_EdgeCases(t *testing.T) {
	t.Run("All nil parameters", func(t *testing.T) {
		provider := NewVPCBootstrapProvider(nil, nil, nil)
		assert.NotNil(t, provider)
	})

	t.Run("Only client provided", func(t *testing.T) {
		client := &ibm.Client{}
		provider := NewVPCBootstrapProvider(client, nil, nil)
		assert.NotNil(t, provider)
	})

	t.Run("Kubernetes clients without IBM client", func(t *testing.T) {
		//nolint:staticcheck // SA1019: NewSimpleClientset is deprecated but NewClientset requires generated apply configurations
		k8sClient := fake.NewSimpleClientset()
		kubeClient := fakeClient.NewClientBuilder().Build()
		provider := NewVPCBootstrapProvider(nil, k8sClient, kubeClient)
		assert.NotNil(t, provider)
	})
}

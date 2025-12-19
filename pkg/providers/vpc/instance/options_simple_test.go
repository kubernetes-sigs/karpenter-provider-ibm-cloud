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

package instance

import (
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes/fake"
	fakeClient "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/vpc/bootstrap"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/utils/vpcclient"
)

func TestVPCInstanceProvider_ConstructorBehavior(t *testing.T) {
	client := &ibm.Client{}
	kubeClient := fakeClient.NewClientBuilder().Build()

	t.Run("Successful creation with required parameters", func(t *testing.T) {
		// Set required environment variable
		_ = os.Setenv("IBMCLOUD_API_KEY", "test-api-key")
		defer func() { _ = os.Unsetenv("IBMCLOUD_API_KEY") }()

		provider, err := NewVPCInstanceProvider(client, kubeClient)
		require.NoError(t, err)
		assert.NotNil(t, provider)
	})

	t.Run("Missing API key environment variable", func(t *testing.T) {
		// Ensure environment variable is not set
		_ = os.Unsetenv("IBMCLOUD_API_KEY")

		_, err := NewVPCInstanceProvider(client, kubeClient)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "IBMCLOUD_API_KEY environment variable is required")
	})

	t.Run("Nil IBM client", func(t *testing.T) {
		_ = os.Setenv("IBMCLOUD_API_KEY", "test-api-key")
		defer func() { _ = os.Unsetenv("IBMCLOUD_API_KEY") }()

		_, err := NewVPCInstanceProvider(nil, kubeClient)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "IBM client cannot be nil")
	})

	t.Run("Nil kube client", func(t *testing.T) {
		_ = os.Setenv("IBMCLOUD_API_KEY", "test-api-key")
		defer func() { _ = os.Unsetenv("IBMCLOUD_API_KEY") }()

		_, err := NewVPCInstanceProvider(client, nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "kubernetes client cannot be nil")
	})

	t.Run("WithKubernetesClient option", func(t *testing.T) {
		_ = os.Setenv("IBMCLOUD_API_KEY", "test-api-key")
		defer func() { _ = os.Unsetenv("IBMCLOUD_API_KEY") }()

		//nolint:staticcheck // SA1019: NewSimpleClientset is deprecated but NewClientset requires generated apply configurations
		k8sClient := fake.NewSimpleClientset()

		provider, err := NewVPCInstanceProvider(client, kubeClient, WithKubernetesClient(k8sClient))
		require.NoError(t, err)
		assert.NotNil(t, provider)
	})

	t.Run("WithKubernetesClient nil client should error", func(t *testing.T) {
		_ = os.Setenv("IBMCLOUD_API_KEY", "test-api-key")
		defer func() { _ = os.Unsetenv("IBMCLOUD_API_KEY") }()

		_, err := NewVPCInstanceProvider(client, kubeClient, WithKubernetesClient(nil))
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "kubernetes client cannot be nil when provided")
	})

	t.Run("WithBootstrapProvider option", func(t *testing.T) {
		_ = os.Setenv("IBMCLOUD_API_KEY", "test-api-key")
		defer func() { _ = os.Unsetenv("IBMCLOUD_API_KEY") }()

		//nolint:staticcheck // SA1019: NewSimpleClientset is deprecated but NewClientset requires generated apply configurations
		k8sClient := fake.NewSimpleClientset()
		bootstrapProvider := bootstrap.NewVPCBootstrapProvider(client, k8sClient, kubeClient)

		provider, err := NewVPCInstanceProvider(client, kubeClient, WithBootstrapProvider(bootstrapProvider))
		require.NoError(t, err)
		assert.NotNil(t, provider)
	})

	t.Run("WithBootstrapProvider nil provider should error", func(t *testing.T) {
		_ = os.Setenv("IBMCLOUD_API_KEY", "test-api-key")
		defer func() { _ = os.Unsetenv("IBMCLOUD_API_KEY") }()

		_, err := NewVPCInstanceProvider(client, kubeClient, WithBootstrapProvider(nil))
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "bootstrap provider cannot be nil when provided")
	})

	t.Run("WithVPCClientManager option", func(t *testing.T) {
		_ = os.Setenv("IBMCLOUD_API_KEY", "test-api-key")
		defer func() { _ = os.Unsetenv("IBMCLOUD_API_KEY") }()

		manager := vpcclient.NewManager(client, 0) // Use 0 timeout for test

		provider, err := NewVPCInstanceProvider(client, kubeClient, WithVPCClientManager(manager))
		require.NoError(t, err)
		assert.NotNil(t, provider)
	})

	t.Run("WithVPCClientManager nil manager should error", func(t *testing.T) {
		_ = os.Setenv("IBMCLOUD_API_KEY", "test-api-key")
		defer func() { _ = os.Unsetenv("IBMCLOUD_API_KEY") }()

		_, err := NewVPCInstanceProvider(client, kubeClient, WithVPCClientManager(nil))
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "VPC client manager cannot be nil when provided")
	})

	t.Run("Multiple options", func(t *testing.T) {
		_ = os.Setenv("IBMCLOUD_API_KEY", "test-api-key")
		defer func() { _ = os.Unsetenv("IBMCLOUD_API_KEY") }()

		//nolint:staticcheck // SA1019: NewSimpleClientset is deprecated but NewClientset requires generated apply configurations
		k8sClient := fake.NewSimpleClientset()
		bootstrapProvider := bootstrap.NewVPCBootstrapProvider(client, k8sClient, kubeClient)
		manager := vpcclient.NewManager(client, 0)

		provider, err := NewVPCInstanceProvider(
			client,
			kubeClient,
			WithKubernetesClient(k8sClient),
			WithBootstrapProvider(bootstrapProvider),
			WithVPCClientManager(manager),
		)
		require.NoError(t, err)
		assert.NotNil(t, provider)
	})
}

func TestQuotaInfo_Struct(t *testing.T) {
	t.Run("QuotaInfo structure", func(t *testing.T) {
		quota := QuotaInfo{
			InstanceUtilization: 0.75,
			VCPUUtilization:     0.82,
		}

		assert.Equal(t, 0.75, quota.InstanceUtilization)
		assert.Equal(t, 0.82, quota.VCPUUtilization)
	})

	t.Run("QuotaInfo zero values", func(t *testing.T) {
		quota := QuotaInfo{}
		assert.Equal(t, 0.0, quota.InstanceUtilization)
		assert.Equal(t, 0.0, quota.VCPUUtilization)
	})

	t.Run("QuotaInfo edge values", func(t *testing.T) {
		quota := QuotaInfo{
			InstanceUtilization: 1.0, // 100% utilization
			VCPUUtilization:     0.0, // 0% utilization
		}

		assert.Equal(t, 1.0, quota.InstanceUtilization)
		assert.Equal(t, 0.0, quota.VCPUUtilization)
	})
}

func TestOptionFunctions_Behavior(t *testing.T) {
	t.Run("Option functions return valid functions", func(t *testing.T) {
		//nolint:staticcheck // SA1019: NewSimpleClientset is deprecated but NewClientset requires generated apply configurations
		k8sClient := fake.NewSimpleClientset()
		kubeClient := fakeClient.NewClientBuilder().Build()

		// Test that option functions are not nil
		opt1 := WithKubernetesClient(k8sClient)
		assert.NotNil(t, opt1)

		bootstrapProvider := bootstrap.NewVPCBootstrapProvider(&ibm.Client{}, k8sClient, kubeClient)
		opt2 := WithBootstrapProvider(bootstrapProvider)
		assert.NotNil(t, opt2)

		manager := vpcclient.NewManager(&ibm.Client{}, 0)
		opt3 := WithVPCClientManager(manager)
		assert.NotNil(t, opt3)
	})

	t.Run("Options can be applied in any order", func(t *testing.T) {
		_ = os.Setenv("IBMCLOUD_API_KEY", "test-api-key")
		defer func() { _ = os.Unsetenv("IBMCLOUD_API_KEY") }()

		client := &ibm.Client{}
		kubeClient := fakeClient.NewClientBuilder().Build()
		//nolint:staticcheck // SA1019: NewSimpleClientset is deprecated but NewClientset requires generated apply configurations
		k8sClient := fake.NewSimpleClientset()
		manager := vpcclient.NewManager(client, 0)
		bootstrapProvider := bootstrap.NewVPCBootstrapProvider(client, k8sClient, kubeClient)

		// Try different orders of options
		orders := [][]Option{
			{WithKubernetesClient(k8sClient), WithVPCClientManager(manager), WithBootstrapProvider(bootstrapProvider)},
			{WithVPCClientManager(manager), WithBootstrapProvider(bootstrapProvider), WithKubernetesClient(k8sClient)},
			{WithBootstrapProvider(bootstrapProvider), WithKubernetesClient(k8sClient), WithVPCClientManager(manager)},
		}

		for i, opts := range orders {
			t.Run(fmt.Sprintf("order_%d", i+1), func(t *testing.T) {
				provider, err := NewVPCInstanceProvider(client, kubeClient, opts...)
				require.NoError(t, err)
				assert.NotNil(t, provider)
			})
		}
	})
}

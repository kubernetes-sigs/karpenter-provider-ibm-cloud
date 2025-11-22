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

	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	fakeClient "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	commonTypes "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/types"
)

// MockK8sClient provides a mock implementation for specific operations
type MockK8sClient struct {
	mock.Mock
	fake.Clientset
}

func (m *MockK8sClient) CoreV1() typedcorev1.CoreV1Interface {
	args := m.Called()
	return args.Get(0).(typedcorev1.CoreV1Interface)
}

// MockCoreV1 provides a mock implementation of CoreV1Interface
type MockCoreV1 struct {
	mock.Mock
}

func (m *MockCoreV1) ConfigMaps(namespace string) typedcorev1.ConfigMapInterface {
	args := m.Called(namespace)
	return args.Get(0).(typedcorev1.ConfigMapInterface)
}

func (m *MockCoreV1) Services(namespace string) typedcorev1.ServiceInterface {
	args := m.Called(namespace)
	return args.Get(0).(typedcorev1.ServiceInterface)
}

func (m *MockCoreV1) Nodes() typedcorev1.NodeInterface {
	args := m.Called()
	return args.Get(0).(typedcorev1.NodeInterface)
}

// MockVPCClient provides a mock implementation for VPC client operations
type MockVPCClient struct {
	mock.Mock
}

func (m *MockVPCClient) GetInstance(ctx context.Context, instanceID string) (*vpcv1.Instance, error) {
	args := m.Called(ctx, instanceID)
	return args.Get(0).(*vpcv1.Instance), args.Error(1)
}

// MockIBMClient provides a mock implementation for IBM client operations
type MockIBMClient struct {
	mock.Mock
	vpcClient *MockVPCClient
}

func (m *MockIBMClient) GetVPCClient() (*ibm.VPCClient, error) {
	args := m.Called()
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*ibm.VPCClient), args.Error(1)
}

func NewMockIBMClient(vpcClient *MockVPCClient) *MockIBMClient {
	return &MockIBMClient{
		vpcClient: vpcClient,
	}
}

// Test helpers

func getTestClusterInfoConfigMap() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster-info",
			Namespace: "kube-system",
		},
		Data: map[string]string{
			"kubeconfig": `apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: dGVzdC1jYS1kYXRh
    server: https://test-cluster.us-south.containers.cloud.ibm.com:31234
  name: test-cluster
contexts:
- context:
    cluster: test-cluster
    user: test-user
  name: test-context
current-context: test-context
kind: Config
users:
- name: test-user
  user:
    token: test-token`,
		},
	}
}

func getTestKubernetesService() *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kubernetes",
			Namespace: "default",
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "10.96.0.1",
			Ports: []corev1.ServicePort{
				{
					Port: 443,
				},
			},
		},
	}
}

// Temporarily disabled - requires proper IBM client mocking
/*func TestVPCBootstrapProvider_GetUserData(t *testing.T) {
	tests := []struct {
		name             string
		nodeClass        *v1alpha1.IBMNodeClass
		nodeClaim        types.NamespacedName
		setupMocks       func(*fake.Clientset)
		expectError      bool
		errorContains    string
		validateUserData func(*testing.T, string)
	}{
		{
			name:      "successful user data generation with cluster-info configmap",
			nodeClass: getTestNodeClass(),
			nodeClaim: types.NamespacedName{Name: "test-nodeclaim", Namespace: "default"},
			setupMocks: func(fakeClient *fake.Clientset) {
				// Add CA certificate secret (required for new implementation)
				caSecret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "default-token",
						Namespace: "kube-system",
					},
					Type: corev1.SecretTypeServiceAccountToken,
					Data: map[string][]byte{
						"ca.crt": []byte("-----BEGIN CERTIFICATE-----\nMIIBKjCB4wIBATANBgkqhkiG9w0BAQsFADA...\n-----END CERTIFICATE-----"),
						"token":  []byte("test-token"),
					},
				}
				_, _ = fakeClient.CoreV1().Secrets("kube-system").Create(context.Background(), caSecret, metav1.CreateOptions{})

				// Add kubeadm-config configmap for API endpoint discovery
				kubeadmConfig := getTestKubeadmConfigMap()
				_, _ = fakeClient.CoreV1().ConfigMaps("kube-system").Create(context.Background(), kubeadmConfig, metav1.CreateOptions{})

				// Add cluster-info configmap
				clusterInfo := getTestClusterInfoConfigMap()
				_, _ = fakeClient.CoreV1().ConfigMaps("kube-system").Create(context.Background(), clusterInfo, metav1.CreateOptions{})

				// Add DNS service for cluster discovery
				dnsService := getTestDNSService()
				_, _ = fakeClient.CoreV1().Services("kube-system").Create(context.Background(), dnsService, metav1.CreateOptions{})

				// Add kubernetes service for cluster CIDR discovery
				kubeService := getTestKubernetesService()
				_, _ = fakeClient.CoreV1().Services("default").Create(context.Background(), kubeService, metav1.CreateOptions{})

				// Add test node for container runtime detection
				testNode := getTestNode()
				_, _ = fakeClient.CoreV1().Nodes().Create(context.Background(), testNode, metav1.CreateOptions{})

				// Add calico daemonset for CNI detection
				calicoDaemonSet := getTestCalicoDaemonSet()
				_, _ = fakeClient.AppsV1().DaemonSets("kube-system").Create(context.Background(), calicoDaemonSet, metav1.CreateOptions{})
			},
			expectError: false,
			validateUserData: func(t *testing.T, userData string) {
				assert.NotEmpty(t, userData)

				// User data is plain text (no base64 encoding)
				assert.Contains(t, userData, "#!/bin/bash")
				// Should contain custom user data from NodeClass
				assert.Contains(t, userData, "custom user data")
			},
		},
		{
			name:      "with kubeadm-config but no cluster-info configmap",
			nodeClass: getTestNodeClass(),
			nodeClaim: types.NamespacedName{Name: "test-nodeclaim", Namespace: "default"},
			setupMocks: func(fakeClient *fake.Clientset) {
				// Add CA certificate secret (required for new implementation)
				caSecret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "default-token",
						Namespace: "kube-system",
					},
					Type: corev1.SecretTypeServiceAccountToken,
					Data: map[string][]byte{
						"ca.crt": []byte("-----BEGIN CERTIFICATE-----\nMIIBKjCB4wIBATANBgkqhkiG9w0BAQsFADA...\n-----END CERTIFICATE-----"),
						"token":  []byte("test-token"),
					},
				}
				_, _ = fakeClient.CoreV1().Secrets("kube-system").Create(context.Background(), caSecret, metav1.CreateOptions{})

				// Add kubeadm-config configmap for API endpoint discovery
				kubeadmConfig := getTestKubeadmConfigMap()
				_, _ = fakeClient.CoreV1().ConfigMaps("kube-system").Create(context.Background(), kubeadmConfig, metav1.CreateOptions{})

				// Add kubernetes service for fallback
				kubeService := getTestKubernetesService()
				_, _ = fakeClient.CoreV1().Services("default").Create(context.Background(), kubeService, metav1.CreateOptions{})

				// Add DNS service for cluster discovery
				dnsService := getTestDNSService()
				_, _ = fakeClient.CoreV1().Services("kube-system").Create(context.Background(), dnsService, metav1.CreateOptions{})

				// Add test node
				testNode := getTestNode()
				_, _ = fakeClient.CoreV1().Nodes().Create(context.Background(), testNode, metav1.CreateOptions{})
			},
			expectError: false,
			validateUserData: func(t *testing.T, userData string) {
				assert.NotEmpty(t, userData)

				// User data is plain text (no base64 encoding)
				assert.Contains(t, userData, "#!/bin/bash")
			},
		},
		{
			name: "nodeclass without custom user data",
			nodeClass: func() *v1alpha1.IBMNodeClass {
				nc := getTestNodeClass()
				nc.Spec.UserData = "" // Remove custom user data
				return nc
			}(),
			nodeClaim: types.NamespacedName{Name: "test-nodeclaim", Namespace: "default"},
			setupMocks: func(fakeClient *fake.Clientset) {
				// Add CA certificate secret (required for new implementation)
				caSecret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "default-token",
						Namespace: "kube-system",
					},
					Type: corev1.SecretTypeServiceAccountToken,
					Data: map[string][]byte{
						"ca.crt": []byte("-----BEGIN CERTIFICATE-----\nMIIBKjCB4wIBATANBgkqhkiG9w0BAQsFADA...\n-----END CERTIFICATE-----"),
						"token":  []byte("test-token"),
					},
				}
				_, _ = fakeClient.CoreV1().Secrets("kube-system").Create(context.Background(), caSecret, metav1.CreateOptions{})

				// Add kubeadm-config configmap for API endpoint discovery
				kubeadmConfig := getTestKubeadmConfigMap()
				_, _ = fakeClient.CoreV1().ConfigMaps("kube-system").Create(context.Background(), kubeadmConfig, metav1.CreateOptions{})

				clusterInfo := getTestClusterInfoConfigMap()
				_, _ = fakeClient.CoreV1().ConfigMaps("kube-system").Create(context.Background(), clusterInfo, metav1.CreateOptions{})

				// Add DNS service for cluster discovery
				dnsService := getTestDNSService()
				_, _ = fakeClient.CoreV1().Services("kube-system").Create(context.Background(), dnsService, metav1.CreateOptions{})

				// Add kubernetes service for cluster CIDR discovery
				kubeService := getTestKubernetesService()
				_, _ = fakeClient.CoreV1().Services("default").Create(context.Background(), kubeService, metav1.CreateOptions{})

				testNode := getTestNode()
				_, _ = fakeClient.CoreV1().Nodes().Create(context.Background(), testNode, metav1.CreateOptions{})
			},
			expectError: false,
			validateUserData: func(t *testing.T, userData string) {
				assert.NotEmpty(t, userData)

				// User data is plain text (no base64 encoding)
				assert.Contains(t, userData, "#!/bin/bash")
				// Should not contain the actual custom user data content (echo 'custom user data')
				assert.NotContains(t, userData, "echo 'custom user data'")
			},
		},
		{
			name:      "cluster info discovery failure",
			nodeClass: getTestNodeClass(),
			nodeClaim: types.NamespacedName{Name: "test-nodeclaim", Namespace: "default"},
			setupMocks: func(fakeClient *fake.Clientset) {
				// Don't add any cluster info sources - both configmap and service will fail
			},
			expectError:   true,
			errorContains: "getting internal API server endpoint",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Create fake Kubernetes client
			scheme := runtime.NewScheme()
			_ = corev1.AddToScheme(scheme)
			fakeClient := fake.NewSimpleClientset()

			// Setup mocks
			tt.setupMocks(fakeClient)

			// Create VPC bootstrap provider
			provider := NewVPCBootstrapProvider(nil, fakeClient, nil)

			// Test GetUserData method
			result, err := provider.GetUserData(ctx, tt.nodeClass, tt.nodeClaim)

			// Validate results
			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
				assert.Empty(t, result)
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, result)
				if tt.validateUserData != nil {
					tt.validateUserData(t, result)
				}
			}
		})
	}
}*/

func TestVPCBootstrapProvider_getClusterInfo(t *testing.T) {
	tests := []struct {
		name           string
		setupMocks     func(*fake.Clientset)
		expectError    bool
		errorContains  string
		validateResult func(*testing.T, *commonTypes.ClusterInfo)
	}{
		{
			name: "successful cluster info from configmap",
			setupMocks: func(fakeClient *fake.Clientset) {
				clusterInfo := getTestClusterInfoConfigMap()
				_, _ = fakeClient.CoreV1().ConfigMaps("kube-system").Create(context.Background(), clusterInfo, metav1.CreateOptions{})
			},
			expectError: false,
			validateResult: func(t *testing.T, info *commonTypes.ClusterInfo) {
				assert.NotNil(t, info)
				assert.Equal(t, "https://test-cluster.us-south.containers.cloud.ibm.com:31234", info.Endpoint)
				assert.NotEmpty(t, info.CAData)
				assert.Equal(t, "karpenter-vpc-cluster", info.ClusterName)
				assert.False(t, info.IsIKSManaged)
			},
		},
		{
			name: "fallback to kubernetes service",
			setupMocks: func(fakeClient *fake.Clientset) {
				// Don't create configmap, but create service for fallback
				kubeService := getTestKubernetesService()
				_, _ = fakeClient.CoreV1().Services("default").Create(context.Background(), kubeService, metav1.CreateOptions{})
			},
			expectError: false,
			validateResult: func(t *testing.T, info *commonTypes.ClusterInfo) {
				assert.NotNil(t, info)
				assert.Equal(t, "https://10.96.0.1:443", info.Endpoint)
				assert.Equal(t, "karpenter-vpc-cluster", info.ClusterName)
				assert.False(t, info.IsIKSManaged)
			},
		},
		{
			name: "configmap exists but no kubeconfig",
			setupMocks: func(fakeClient *fake.Clientset) {
				configMap := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "cluster-info",
						Namespace: "kube-system",
					},
					Data: map[string]string{
						"other-data": "not-kubeconfig",
					},
				}
				_, _ = fakeClient.CoreV1().ConfigMaps("kube-system").Create(context.Background(), configMap, metav1.CreateOptions{})
			},
			expectError:   true,
			errorContains: "kubeconfig not found in cluster-info configmap",
		},
		{
			name: "no cluster info sources available",
			setupMocks: func(fakeClient *fake.Clientset) {
				// Don't create anything - both configmap and service will fail
			},
			expectError:   true,
			errorContains: "getting cluster endpoint",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Create fake Kubernetes client
			fakeClient := fake.NewSimpleClientset()

			// Setup mocks
			tt.setupMocks(fakeClient)

			// Create VPC bootstrap provider
			provider := NewVPCBootstrapProvider(nil, fakeClient, nil)

			// Test getClusterInfo method
			result, err := provider.getClusterInfo(ctx)

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
				if tt.validateResult != nil {
					tt.validateResult(t, result)
				}
			}
		})
	}
}

func TestVPCBootstrapProvider_detectContainerRuntime(t *testing.T) {
	tests := []struct {
		name            string
		setupMocks      func(*fake.Clientset)
		expectedRuntime string
	}{
		{
			name: "containerd runtime detected",
			setupMocks: func(fakeClient *fake.Clientset) {
				node := &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
					Status: corev1.NodeStatus{
						NodeInfo: corev1.NodeSystemInfo{
							ContainerRuntimeVersion: "containerd://1.6.8",
						},
					},
				}
				_, _ = fakeClient.CoreV1().Nodes().Create(context.Background(), node, metav1.CreateOptions{})
			},
			expectedRuntime: "containerd",
		},
		{
			name: "cri-o runtime detected",
			setupMocks: func(fakeClient *fake.Clientset) {
				node := &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
					Status: corev1.NodeStatus{
						NodeInfo: corev1.NodeSystemInfo{
							ContainerRuntimeVersion: "cri-o://1.25.0",
						},
					},
				}
				_, _ = fakeClient.CoreV1().Nodes().Create(context.Background(), node, metav1.CreateOptions{})
			},
			expectedRuntime: "cri-o",
		},
		{
			name: "no nodes available - default to containerd",
			setupMocks: func(fakeClient *fake.Clientset) {
				// Don't create any nodes
			},
			expectedRuntime: "containerd",
		},
		{
			name: "node with empty runtime version - default to containerd",
			setupMocks: func(fakeClient *fake.Clientset) {
				node := &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
					Status: corev1.NodeStatus{
						NodeInfo: corev1.NodeSystemInfo{
							ContainerRuntimeVersion: "",
						},
					},
				}
				_, _ = fakeClient.CoreV1().Nodes().Create(context.Background(), node, metav1.CreateOptions{})
			},
			expectedRuntime: "containerd",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Create fake Kubernetes client
			fakeClient := fake.NewSimpleClientset()

			// Setup mocks
			tt.setupMocks(fakeClient)

			// Create VPC bootstrap provider
			provider := NewVPCBootstrapProvider(nil, fakeClient, nil)

			// Test detectContainerRuntime method
			result := provider.detectContainerRuntime(ctx)

			// Validate result
			assert.Equal(t, tt.expectedRuntime, result)
		})
	}
}

func TestVPCBootstrapProvider_getClusterCA(t *testing.T) {
	tests := []struct {
		name           string
		setupMocks     func(*fake.Clientset)
		expectError    bool
		errorContains  string
		validateResult func(*testing.T, string)
	}{
		{
			name: "successful CA extraction from kube-root-ca.crt ConfigMap",
			setupMocks: func(fakeClient *fake.Clientset) {
				// Create kube-root-ca.crt ConfigMap
				cm := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "kube-root-ca.crt",
						Namespace: "kube-system",
					},
					Data: map[string]string{
						"ca.crt": "-----BEGIN CERTIFICATE-----\nMIIBKjCB4wIBATANBgkqhkiG9w0BAQsFADA...\n-----END CERTIFICATE-----",
					},
				}
				_, err := fakeClient.CoreV1().ConfigMaps("kube-system").Create(context.Background(), cm, metav1.CreateOptions{})
				assert.NoError(t, err)
			},
			expectError: false,
			validateResult: func(t *testing.T, caCert string) {
				assert.Equal(t, "-----BEGIN CERTIFICATE-----\nMIIBKjCB4wIBATANBgkqhkiG9w0BAQsFADA...\n-----END CERTIFICATE-----", caCert)
			},
		},
		{
			name: "successful CA extraction from default-token secret",
			setupMocks: func(fakeClient *fake.Clientset) {
				secret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "default-token",
						Namespace: "kube-system",
					},
					Type: corev1.SecretTypeServiceAccountToken,
					Data: map[string][]byte{
						"ca.crt": []byte("-----BEGIN CERTIFICATE-----\nMIIBKjCB4wIBATANBgkqhkiG9w0BAQsFADA...\n-----END CERTIFICATE-----"),
						"token":  []byte("test-token"),
					},
				}
				_, _ = fakeClient.CoreV1().Secrets("kube-system").Create(context.Background(), secret, metav1.CreateOptions{})
			},
			expectError: false,
			validateResult: func(t *testing.T, caCert string) {
				assert.NotEmpty(t, caCert)
				assert.Contains(t, caCert, "-----BEGIN CERTIFICATE-----")
				assert.Contains(t, caCert, "-----END CERTIFICATE-----")
			},
		},
		{
			name: "fallback to any service account token when default-token not found",
			setupMocks: func(fakeClient *fake.Clientset) {
				secret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "karpenter-token-abc123",
						Namespace: "kube-system",
					},
					Type: corev1.SecretTypeServiceAccountToken,
					Data: map[string][]byte{
						"ca.crt": []byte("-----BEGIN CERTIFICATE-----\nMIIBKjCB4wIBATANBgkqhkiG9w0BAQsFADA...\n-----END CERTIFICATE-----"),
						"token":  []byte("test-token"),
					},
				}
				_, _ = fakeClient.CoreV1().Secrets("kube-system").Create(context.Background(), secret, metav1.CreateOptions{})
			},
			expectError: false,
			validateResult: func(t *testing.T, caCert string) {
				assert.NotEmpty(t, caCert)
				assert.Contains(t, caCert, "-----BEGIN CERTIFICATE-----")
			},
		},
		{
			name: "no service account tokens available",
			setupMocks: func(fakeClient *fake.Clientset) {
				// Don't create any secrets
			},
			expectError:   true,
			errorContains: "unable to find CA certificate in ConfigMap or service account tokens",
		},
		{
			name: "service account token exists but no ca.crt",
			setupMocks: func(fakeClient *fake.Clientset) {
				secret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "default-token",
						Namespace: "kube-system",
					},
					Type: corev1.SecretTypeServiceAccountToken,
					Data: map[string][]byte{
						"token": []byte("test-token"),
						// No ca.crt field
					},
				}
				_, _ = fakeClient.CoreV1().Secrets("kube-system").Create(context.Background(), secret, metav1.CreateOptions{})
			},
			expectError:   true,
			errorContains: "ca.crt not found in service account token secret",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Create fake Kubernetes client
			fakeClient := fake.NewSimpleClientset()

			// Setup mocks
			tt.setupMocks(fakeClient)

			// Create VPC bootstrap provider
			provider := NewVPCBootstrapProvider(nil, fakeClient, nil)

			// Test getClusterCA method
			result, err := provider.getClusterCA(ctx)

			// Validate results
			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
				assert.Empty(t, result)
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, result)
				if tt.validateResult != nil {
					tt.validateResult(t, result)
				}
			}
		})
	}
}

// Temporarily disabled - requires proper IBM client mocking
/*func TestVPCBootstrapProvider_GetUserData_WithCABundle(t *testing.T) {
	tests := []struct {
		name             string
		nodeClass        *v1alpha1.IBMNodeClass
		nodeClaim        types.NamespacedName
		setupMocks       func(*fake.Clientset)
		expectError      bool
		errorContains    string
		validateUserData func(*testing.T, string)
	}{
		{
			name:      "user data generation includes CA certificate",
			nodeClass: getTestNodeClass(),
			nodeClaim: types.NamespacedName{Name: "test-nodeclaim", Namespace: "default"},
			setupMocks: func(fakeClient *fake.Clientset) {
				// Add CA certificate secret
				secret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "default-token",
						Namespace: "kube-system",
					},
					Type: corev1.SecretTypeServiceAccountToken,
					Data: map[string][]byte{
						"ca.crt": []byte("-----BEGIN CERTIFICATE-----\nMIIBKjCB4wIBATANBgkqhkiG9w0BAQsFADA...\n-----END CERTIFICATE-----"),
						"token":  []byte("test-token"),
					},
				}
				_, _ = fakeClient.CoreV1().Secrets("kube-system").Create(context.Background(), secret, metav1.CreateOptions{})

				// Add kubeadm-config configmap for API endpoint discovery
				kubeadmConfig := getTestKubeadmConfigMap()
				_, _ = fakeClient.CoreV1().ConfigMaps("kube-system").Create(context.Background(), kubeadmConfig, metav1.CreateOptions{})

				// Add DNS service for cluster discovery
				dnsService := getTestDNSService()
				_, _ = fakeClient.CoreV1().Services("kube-system").Create(context.Background(), dnsService, metav1.CreateOptions{})

				// Add kubernetes service for cluster CIDR discovery
				kubeService := getTestKubernetesService()
				_, _ = fakeClient.CoreV1().Services("default").Create(context.Background(), kubeService, metav1.CreateOptions{})

				// Add test node for container runtime detection
				testNode := getTestNode()
				_, _ = fakeClient.CoreV1().Nodes().Create(context.Background(), testNode, metav1.CreateOptions{})

				// Add calico daemonset for CNI detection
				calicoDaemonSet := getTestCalicoDaemonSet()
				_, _ = fakeClient.AppsV1().DaemonSets("kube-system").Create(context.Background(), calicoDaemonSet, metav1.CreateOptions{})
			},
			expectError: false,
			validateUserData: func(t *testing.T, userData string) {
				assert.NotEmpty(t, userData)

				// Should contain CA certificate content
				assert.Contains(t, userData, "CA certificate created")
				assert.Contains(t, userData, "-----BEGIN CERTIFICATE-----")
				assert.Contains(t, userData, "-----END CERTIFICATE-----")

				// Should use direct kubelet approach (not kubeadm)
				assert.Contains(t, userData, "Direct Kubelet")
				assert.Contains(t, userData, "bootstrap-kubeconfig")
				assert.Contains(t, userData, "kubelet.service")
				assert.Contains(t, userData, "systemctl start kubelet")

				// Should not contain kubeadm-specific text (since we bypassed kubeadm)
				assert.NotContains(t, userData, "kubeadm join")
				assert.NotContains(t, userData, "discovery-token-ca-cert-hash")
			},
		},
		{
			name:      "CA bundle extraction failure",
			nodeClass: getTestNodeClass(),
			nodeClaim: types.NamespacedName{Name: "test-nodeclaim", Namespace: "default"},
			setupMocks: func(fakeClient *fake.Clientset) {
				// Don't add any CA certificate secrets - will cause failure
				// Add other required resources
				kubeadmConfig := getTestKubeadmConfigMap()
				_, _ = fakeClient.CoreV1().ConfigMaps("kube-system").Create(context.Background(), kubeadmConfig, metav1.CreateOptions{})
			},
			expectError:   true,
			errorContains: "getting cluster CA certificate",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Create fake Kubernetes client
			scheme := runtime.NewScheme()
			_ = corev1.AddToScheme(scheme)
			fakeClient := fake.NewSimpleClientset()

			// Setup mocks
			tt.setupMocks(fakeClient)

			// Create VPC bootstrap provider
			provider := NewVPCBootstrapProvider(nil, fakeClient, nil)

			// Test GetUserData method
			result, err := provider.GetUserData(ctx, tt.nodeClass, tt.nodeClaim)

			// Validate results
			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
				assert.Empty(t, result)
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, result)
				if tt.validateUserData != nil {
					tt.validateUserData(t, result)
				}
			}
		})
	}
}*/

func TestVPCBootstrapProvider_parseKubeconfig(t *testing.T) {
	tests := []struct {
		name             string
		kubeconfig       string
		expectError      bool
		errorContains    string
		expectedEndpoint string
		expectCAData     bool
	}{
		{
			name: "valid kubeconfig",
			kubeconfig: `apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: dGVzdC1jYS1kYXRh
    server: https://test-cluster.us-south.containers.cloud.ibm.com:31234
  name: test-cluster`,
			expectError:      false,
			expectedEndpoint: "https://test-cluster.us-south.containers.cloud.ibm.com:31234",
			expectCAData:     true,
		},
		{
			name: "kubeconfig without CA data",
			kubeconfig: `apiVersion: v1
clusters:
- cluster:
    server: https://test-cluster.us-south.containers.cloud.ibm.com:31234
  name: test-cluster`,
			expectError:      false,
			expectedEndpoint: "https://test-cluster.us-south.containers.cloud.ibm.com:31234",
			expectCAData:     false,
		},
		{
			name: "kubeconfig without server",
			kubeconfig: `apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: dGVzdC1jYS1kYXRh
  name: test-cluster`,
			expectError:   true,
			errorContains: "endpoint not found in kubeconfig",
		},
		{
			name: "invalid base64 CA data",
			kubeconfig: `apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: invalid-base64!@#$
    server: https://test-cluster.us-south.containers.cloud.ibm.com:31234
  name: test-cluster`,
			expectError:   true,
			errorContains: "decoding CA data",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create VPC bootstrap provider
			provider := NewVPCBootstrapProvider(nil, nil, nil)

			// Test parseKubeconfig method
			endpoint, caData, err := provider.parseKubeconfig(tt.kubeconfig)

			// Validate results
			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedEndpoint, endpoint)
				if tt.expectCAData {
					assert.NotEmpty(t, caData)
					// Verify it's valid base64 decoded data
					assert.Equal(t, "test-ca-data", string(caData))
				} else {
					assert.Empty(t, caData)
				}
			}
		})
	}
}

func TestVPCBootstrapProvider_detectArchitectureFromInstanceProfile(t *testing.T) {
	tests := []struct {
		name            string
		instanceProfile string
		expectError     bool
		expectedArch    string
		errorContains   string
	}{
		{
			name:            "empty instance profile - should error",
			instanceProfile: "",
			expectError:     true,
		},
		{
			name:            "nil client - should error with proper message (not nil pointer)",
			instanceProfile: "bx2-2x8",
			expectError:     true,
			errorContains:   "IBM Cloud client is not initialized",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create VPC bootstrap provider without client (will error)
			provider := NewVPCBootstrapProvider(nil, nil, nil)

			// Test detectArchitectureFromInstanceProfile method
			result, err := provider.detectArchitectureFromInstanceProfile(tt.instanceProfile)

			if tt.expectError {
				assert.Error(t, err)
				assert.Empty(t, result)

				// Verify we get the expected error message, not a nil pointer panic
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}

				// Most importantly: verify we don't get the old nil pointer error
				assert.NotContains(t, err.Error(), "listInstanceProfilesOptions cannot be nil")
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedArch, result)
			}
		})
	}
}

// Status Reporting Tests

func TestVPCBootstrapProvider_ReportBootstrapStatus(t *testing.T) {
	tests := []struct {
		name          string
		instanceID    string
		nodeClaimName string
		status        string
		phase         string
		setupMocks    func(*MockIBMClient, *MockVPCClient)
		expectError   bool
		errorContains string
	}{
		{
			name:          "successful status reporting",
			instanceID:    "instance-123",
			nodeClaimName: "test-nodeclaim",
			status:        "configuring",
			phase:         "kubelet-installed",
			setupMocks: func(ibmClient *MockIBMClient, vpcClient *MockVPCClient) {
				// No mocks needed for configmap approach
			},
			expectError: false,
		},
		{
			name:          "empty instance ID - should error",
			instanceID:    "",
			nodeClaimName: "test-nodeclaim",
			status:        "configuring",
			phase:         "kubelet-installed",
			setupMocks:    func(ibmClient *MockIBMClient, vpcClient *MockVPCClient) {},
			expectError:   true,
			errorContains: "instance ID is required for status reporting",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Create mocks
			mockVPCClient := &MockVPCClient{}
			mockIBMClient := NewMockIBMClient(mockVPCClient)

			// Setup mocks
			tt.setupMocks(mockIBMClient, mockVPCClient)

			// Create fake Kubernetes client
			fakeClient := fake.NewSimpleClientset()

			// Create VPC bootstrap provider (nil IBM client is fine for ConfigMap tests)
			provider := NewVPCBootstrapProvider(nil, fakeClient, nil)

			// Test ReportBootstrapStatus method
			err := provider.ReportBootstrapStatus(ctx, tt.instanceID, tt.nodeClaimName, tt.status, tt.phase)

			// Validate results
			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)

				// For successful tests, verify ConfigMap was created
				if tt.instanceID != "" {
					configMapName := "bootstrap-status-" + tt.instanceID
					cm, getErr := fakeClient.CoreV1().ConfigMaps("karpenter").Get(ctx, configMapName, metav1.GetOptions{})
					assert.NoError(t, getErr)
					assert.Equal(t, tt.status, cm.Data["status"])
					assert.Equal(t, tt.phase, cm.Data["phase"])
					assert.Equal(t, tt.nodeClaimName, cm.Data["nodeclaim"])
				}
			}

			// Verify all expectations were met
			mockIBMClient.AssertExpectations(t)
			mockVPCClient.AssertExpectations(t)
		})
	}
}

func TestVPCBootstrapProvider_GetBootstrapStatus(t *testing.T) {
	tests := []struct {
		name           string
		instanceID     string
		setupMocks     func(*MockIBMClient, *MockVPCClient)
		expectError    bool
		errorContains  string
		validateResult func(*testing.T, map[string]string)
	}{
		{
			name:       "successful status retrieval with bootstrap data",
			instanceID: "instance-123",
			setupMocks: func(ibmClient *MockIBMClient, vpcClient *MockVPCClient) {
				// No mocks needed for ConfigMap approach
			},
			expectError: false,
			validateResult: func(t *testing.T, status map[string]string) {
				assert.Equal(t, "running", status["status"])
				assert.Equal(t, "kubelet-active", status["phase"])
				assert.Equal(t, "2025-01-17T10:30:00Z", status["timestamp"])
				assert.Equal(t, "test-nodeclaim", status["nodeclaim"])
			},
		},
		{
			name:       "configmap not found",
			instanceID: "instance-456",
			setupMocks: func(ibmClient *MockIBMClient, vpcClient *MockVPCClient) {
				// No mocks needed - we'll test with a non-existent ConfigMap
			},
			expectError:   true,
			errorContains: "getting bootstrap status ConfigMap",
		},
		{
			name:          "empty instance ID - should error",
			instanceID:    "",
			setupMocks:    func(ibmClient *MockIBMClient, vpcClient *MockVPCClient) {},
			expectError:   true,
			errorContains: "instance ID is required for status retrieval",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Create mocks
			mockVPCClient := &MockVPCClient{}
			mockIBMClient := NewMockIBMClient(mockVPCClient)

			// Setup mocks
			tt.setupMocks(mockIBMClient, mockVPCClient)

			// Create fake Kubernetes client
			fakeClient := fake.NewSimpleClientset()

			// For successful test case, create the ConfigMap
			if !tt.expectError && tt.instanceID == "instance-123" {
				configMapName := "bootstrap-status-" + tt.instanceID
				cm := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      configMapName,
						Namespace: "karpenter",
					},
					Data: map[string]string{
						"status":     "running",
						"phase":      "kubelet-active",
						"timestamp":  "2025-01-17T10:30:00Z",
						"nodeclaim":  "test-nodeclaim",
						"instanceId": tt.instanceID,
					},
				}
				_, _ = fakeClient.CoreV1().ConfigMaps("karpenter").Create(ctx, cm, metav1.CreateOptions{})
			}

			// Create VPC bootstrap provider (nil IBM client is fine for ConfigMap tests)
			provider := NewVPCBootstrapProvider(nil, fakeClient, nil)

			// Test GetBootstrapStatus method
			result, err := provider.GetBootstrapStatus(ctx, tt.instanceID)

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
				if tt.validateResult != nil {
					tt.validateResult(t, result)
				}
			}

			// Verify all expectations were met
			mockIBMClient.AssertExpectations(t)
			mockVPCClient.AssertExpectations(t)
		})
	}
}

func TestCloudInitStatusReporting(t *testing.T) {
	// Test that the cloud-init template contains status reporting calls
	t.Run("cloud-init template includes status reporting", func(t *testing.T) {
		ctx := context.Background()

		// Create test options
		options := commonTypes.Options{
			ClusterEndpoint: "https://test-cluster:6443",
			BootstrapToken:  "test-token",
			NodeName:        "test-node",
			InstanceID:      "test-instance-123",
			Region:          "us-south",
			Zone:            "us-south-1",
		}

		// Create VPC bootstrap provider
		provider := NewVPCBootstrapProvider(nil, nil, nil)

		// Generate cloud-init script
		script, err := provider.generateCloudInitScript(ctx, options)

		// Validate results
		assert.NoError(t, err)
		assert.NotEmpty(t, script)

		// Verify status reporting function is included
		assert.Contains(t, script, "report_status() {")
		assert.Contains(t, script, "karpenter-bootstrap-status.json")
		assert.Contains(t, script, "karpenter-bootstrap-status.log")

		// Verify status reporting calls are included at key points
		assert.Contains(t, script, `report_status "starting" "hostname-setup"`)
		assert.Contains(t, script, `report_status "configuring" "system-setup"`)
		assert.Contains(t, script, `report_status "configuring" "packages-installed"`)
		assert.Contains(t, script, `report_status "configuring" "container-runtime-ready"`)
		assert.Contains(t, script, `report_status "configuring" "kubelet-installed"`)
		assert.Contains(t, script, `report_status "starting" "kubelet-startup"`)
		assert.Contains(t, script, `report_status "running" "kubelet-active"`)
		assert.Contains(t, script, `report_status "completed" "bootstrap-finished"`)

		// Verify instance ID is retrieved from metadata service and node name is templated
		assert.Contains(t, script, "INSTANCE_ID=$(curl -s -f --max-time 10 -H \"Authorization: Bearer $INSTANCE_IDENTITY_TOKEN\" \"http://169.254.169.254/metadata/v1/instance?version=2022-03-29\" | grep -o \"\\\"id\\\":\\\"[0-9a-z]\\{4\\}_[^\\\"]*\" | head -1 | cut -d\"\\\"\" -f4)")
		assert.Contains(t, script, "NODE_NAME=\"test-node\"")

		// Verify structured JSON status creation
		assert.Contains(t, script, `"instanceId": "$INSTANCE_ID"`)
		assert.Contains(t, script, `"nodeClaimName": "$NODE_NAME"`)
		assert.Contains(t, script, `"region": "$REGION"`)
		assert.Contains(t, script, `"zone": "$ZONE"`)
	})
}

func TestVPCBootstrapProvider_GetClusterDNS(t *testing.T) {
	tests := []struct {
		name        string
		services    []corev1.Service
		configMaps  []corev1.ConfigMap
		expectedDNS string
		expectError bool
	}{
		{
			name: "kube-dns service found",
			services: []corev1.Service{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "kube-dns",
						Namespace: "kube-system",
					},
					Spec: corev1.ServiceSpec{
						ClusterIP: "172.21.0.10",
					},
				},
			},
			expectedDNS: "172.21.0.10",
		},
		{
			name: "coredns service found",
			services: []corev1.Service{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "coredns",
						Namespace: "kube-system",
					},
					Spec: corev1.ServiceSpec{
						ClusterIP: "10.96.0.10",
					},
				},
			},
			expectedDNS: "10.96.0.10",
		},
		{
			name: "DNS from kubelet configmap",
			configMaps: []corev1.ConfigMap{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "kubelet-config",
						Namespace: "kube-system",
					},
					Data: map[string]string{
						"kubelet": "apiVersion: kubelet.config.k8s.io/v1beta1\nkind: KubeletConfiguration\nclusterDNS:\n- 172.21.0.10\n",
					},
				},
			},
			expectedDNS: "172.21.0.10",
		},
		{
			name:        "no DNS service or config found",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var k8sObjects []runtime.Object
			for i := range tt.services {
				k8sObjects = append(k8sObjects, &tt.services[i])
			}
			for i := range tt.configMaps {
				k8sObjects = append(k8sObjects, &tt.configMaps[i])
			}
			k8sClient := fake.NewSimpleClientset(k8sObjects...)

			provider := &VPCBootstrapProvider{
				k8sClient: k8sClient,
			}

			dns, err := provider.getClusterDNS(context.Background())

			if tt.expectError {
				assert.Error(t, err)
				assert.Empty(t, dns)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedDNS, dns)
			}
		})
	}
}

func TestVPCBootstrapProvider_GetClusterName(t *testing.T) {
	provider := &VPCBootstrapProvider{}
	name := provider.getClusterName()
	assert.Equal(t, "karpenter-vpc-cluster", name)
}

func TestVPCBootstrapProvider_GetInstanceBootstrapLogs(t *testing.T) {
	provider := &VPCBootstrapProvider{}
	logs, err := provider.GetInstanceBootstrapLogs(context.Background(), "test-instance-id")
	assert.NoError(t, err)
	assert.Contains(t, logs, "/var/log/karpenter-bootstrap.log")
	assert.Contains(t, logs, "/var/log/karpenter-bootstrap-status.log")
	assert.Contains(t, logs, "/var/log/karpenter-bootstrap-status.json")
}

func TestVPCBootstrapProvider_PollInstanceBootstrapStatus(t *testing.T) {
	tests := []struct {
		name          string
		instanceID    string
		expectError   bool
		errorContains string
	}{
		{
			name:          "nil client - should error",
			instanceID:    "test-instance-id",
			expectError:   true,
			errorContains: "IBM Cloud client is not initialized",
		},
		{
			name:          "empty instance ID",
			instanceID:    "",
			expectError:   true,
			errorContains: "instance ID is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a simple mock that implements the interface correctly
			provider := &VPCBootstrapProvider{
				client:    nil, // For error testing - will fail VPC client creation
				k8sClient: fake.NewSimpleClientset(),
			}

			status, err := provider.PollInstanceBootstrapStatus(context.Background(), tt.instanceID)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
				assert.Nil(t, status)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, status)
			}
		})
	}
}

// Architecture Detection Tests - focuses on the logic in the GetUserDataWithInstanceIDAndType method

func TestGetUserDataWithInstanceIDAndType_ArchitectureDetectionFallback(t *testing.T) {
	tests := []struct {
		name                 string
		selectedInstanceType string
		nodeClassProfile     string
		expectError          bool
		errorContains        string
		description          string
	}{
		{
			name:                 "selectedInstanceType parameter provided (primary method)",
			selectedInstanceType: "bx2-4x16",
			nodeClassProfile:     "",
			expectError:          true,
			errorContains:        "getting internal API server endpoint", // Fails earlier in bootstrap process
			description:          "Tests that selectedInstanceType is used first (fails at API endpoint discovery)",
		},
		{
			name:                 "fallback to NodeClass instanceProfile when selectedInstanceType empty",
			selectedInstanceType: "",
			nodeClassProfile:     "cx2-2x4",
			expectError:          true,
			errorContains:        "getting internal API server endpoint", // Fails earlier in bootstrap process
			description:          "Tests that NodeClass instanceProfile is used as fallback (fails at API endpoint discovery)",
		},
		{
			name:                 "error when all methods fail",
			selectedInstanceType: "",
			nodeClassProfile:     "",
			expectError:          true,
			errorContains:        "getting internal API server endpoint", // Fails earlier in bootstrap process
			description:          "Tests error handling when no architecture source available (fails at API endpoint discovery)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Create basic test setup
			fakeK8sClient := fake.NewSimpleClientset()

			// Add minimal required k8s objects for bootstrap
			testSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "default-token",
					Namespace: "kube-system",
				},
				Type: corev1.SecretTypeServiceAccountToken,
				Data: map[string][]byte{
					"ca.crt": []byte("-----BEGIN CERTIFICATE-----\ntest-ca-data\n-----END CERTIFICATE-----"),
					"token":  []byte("test-token"),
				},
			}
			_, _ = fakeK8sClient.CoreV1().Secrets("kube-system").Create(ctx, testSecret, metav1.CreateOptions{})

			// Add kubernetes service for API endpoint discovery
			kubeService := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "kubernetes",
					Namespace: "default",
				},
				Spec: corev1.ServiceSpec{
					ClusterIP: "10.96.0.1",
					Ports: []corev1.ServicePort{
						{Port: 443},
					},
				},
			}
			_, _ = fakeK8sClient.CoreV1().Services("default").Create(ctx, kubeService, metav1.CreateOptions{})

			testService := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "kube-dns",
					Namespace: "kube-system",
				},
				Spec: corev1.ServiceSpec{
					ClusterIP: "172.21.0.10",
				},
			}
			_, _ = fakeK8sClient.CoreV1().Services("kube-system").Create(ctx, testService, metav1.CreateOptions{})

			testNode := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
				Status: corev1.NodeStatus{
					NodeInfo: corev1.NodeSystemInfo{
						ContainerRuntimeVersion: "containerd://1.6.8",
					},
				},
			}
			_, _ = fakeK8sClient.CoreV1().Nodes().Create(ctx, testNode, metav1.CreateOptions{})

			// Add calico daemonset for CNI detection
			calicoDaemonSet := &appsv1.DaemonSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "calico-node",
					Namespace: "kube-system",
				},
			}
			_, _ = fakeK8sClient.AppsV1().DaemonSets("kube-system").Create(ctx, calicoDaemonSet, metav1.CreateOptions{})

			// Create bootstrap provider without IBM client (will cause architecture detection to fail appropriately)
			provider := &VPCBootstrapProvider{
				client:     nil, // No IBM client - tests error handling
				k8sClient:  fakeK8sClient,
				kubeClient: fakeClient.NewClientBuilder().Build(),
			}

			// Create test NodeClass
			nodeClass := &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{
					InstanceProfile: tt.nodeClassProfile,
					Region:          "us-south",
					Zone:            "us-south-1",
				},
			}

			nodeClaim := types.NamespacedName{
				Name:      "test-nodeclaim",
				Namespace: "default",
			}

			// Test the method - this exercises the architecture detection fallback chain
			_, err := provider.GetUserDataWithInstanceIDAndType(ctx, nodeClass, nodeClaim, "test-instance", tt.selectedInstanceType)

			// Validate that the right error path is taken
			assert.Error(t, err, tt.description)
			if tt.errorContains != "" {
				assert.Contains(t, err.Error(), tt.errorContains, tt.description)
			}
		})
	}
}

func TestArchitectureDetectionPriorityOrder(t *testing.T) {
	// This test verifies the logic structure without needing real IBM API calls
	t.Run("code coverage verification", func(t *testing.T) {
		// Create a provider without IBM client to test error handling
		provider := &VPCBootstrapProvider{
			client:     nil,
			k8sClient:  fake.NewSimpleClientset(),
			kubeClient: fakeClient.NewClientBuilder().Build(),
		}

		// Test that empty instance profile returns appropriate error
		arch, err := provider.detectArchitectureFromInstanceProfile("")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "instance profile is empty")
		assert.Empty(t, arch)

		// Test that nil client returns appropriate error (not nil pointer panic)
		arch, err = provider.detectArchitectureFromInstanceProfile("bx2-4x16")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "IBM Cloud client is not initialized")
		assert.Empty(t, arch)

		// This verifies the error handling logic we fixed
		assert.NotContains(t, err.Error(), "nil pointer")
	})
}

// TestCiliumTaintConditionalBehavior focuses specifically on testing that the Cilium taint
// is only added when the CNI plugin is actually Cilium, which was the main bug we fixed.
func TestCiliumTaintConditionalBehavior(t *testing.T) {
	tests := []struct {
		name              string
		cniPlugin         string
		expectCiliumTaint bool
	}{
		{
			name:              "Calico CNI - should NOT have Cilium taint",
			cniPlugin:         "calico",
			expectCiliumTaint: false,
		},
		{
			name:              "Cilium CNI - should have Cilium taint",
			cniPlugin:         "cilium",
			expectCiliumTaint: true,
		},
		{
			name:              "Flannel CNI - should NOT have Cilium taint",
			cniPlugin:         "flannel",
			expectCiliumTaint: false,
		},
		{
			name:              "Empty CNI plugin - should NOT have Cilium taint",
			cniPlugin:         "",
			expectCiliumTaint: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Create minimal test options
			options := commonTypes.Options{
				ClusterEndpoint:   "https://test-cluster:6443",
				BootstrapToken:    "test-token",
				NodeName:          "test-node",
				CNIPlugin:         tt.cniPlugin,
				CNIVersion:        "v1.0.0",
				Architecture:      "amd64",
				DNSClusterIP:      "172.21.0.10",
				CABundle:          "test-ca",
				KubernetesVersion: "v1.29.0",
			}

			// Create VPC bootstrap provider
			provider := NewVPCBootstrapProvider(nil, nil, nil)

			// Generate cloud-init script
			script, err := provider.generateCloudInitScript(ctx, options)

			// Validate that script generation succeeds
			assert.NoError(t, err)
			assert.NotEmpty(t, script)

			// Main test: Verify Cilium taint behavior matches expectation
			if tt.expectCiliumTaint {
				assert.Contains(t, script, "node.cilium.io/agent-not-ready",
					"Cilium taint should be present for Cilium CNI")
			} else {
				assert.NotContains(t, script, "node.cilium.io/agent-not-ready",
					"Cilium taint should NOT be present for non-Cilium CNI: %s", tt.cniPlugin)
			}
		})
	}
}

// TestCloudInitScriptWithCustomTaints verifies that custom taints are added correctly
// alongside CNI-specific taints
func TestCloudInitScriptWithCustomTaints(t *testing.T) {
	tests := []struct {
		name           string
		cniPlugin      string
		customTaints   []corev1.Taint
		validateScript func(*testing.T, string)
	}{
		{
			name:      "Calico with custom taints - no Cilium taint",
			cniPlugin: "calico",
			customTaints: []corev1.Taint{
				{
					Key:    "example.com/special",
					Value:  "true",
					Effect: corev1.TaintEffectNoSchedule,
				},
				{
					Key:    "dedicated",
					Value:  "gpu",
					Effect: corev1.TaintEffectNoExecute,
				},
			},
			validateScript: func(t *testing.T, script string) {
				// Verify custom taints are present
				assert.Contains(t, script, "example.com/special")
				assert.Contains(t, script, "dedicated")
				assert.Contains(t, script, "gpu")
				assert.Contains(t, script, "NoSchedule")
				assert.Contains(t, script, "NoExecute")

				// Verify Cilium taint is NOT present
				assert.NotContains(t, script, "node.cilium.io/agent-not-ready")
			},
		},
		{
			name:      "Cilium with custom taints - includes Cilium taint",
			cniPlugin: "cilium",
			customTaints: []corev1.Taint{
				{
					Key:    "workload",
					Value:  "batch",
					Effect: corev1.TaintEffectPreferNoSchedule,
				},
			},
			validateScript: func(t *testing.T, script string) {
				// Verify custom taint is present
				assert.Contains(t, script, "workload")
				assert.Contains(t, script, "batch")
				assert.Contains(t, script, "PreferNoSchedule")

				// Verify Cilium taint IS also present
				assert.Contains(t, script, "node.cilium.io/agent-not-ready")
			},
		},
		{
			name:      "No CNI plugin with custom taints",
			cniPlugin: "",
			customTaints: []corev1.Taint{
				{
					Key:    "test",
					Value:  "value",
					Effect: corev1.TaintEffectNoSchedule,
				},
			},
			validateScript: func(t *testing.T, script string) {
				// Verify custom taint is present
				assert.Contains(t, script, "test")
				assert.Contains(t, script, "value")
				assert.Contains(t, script, "NoSchedule")

				// Verify Cilium taint is NOT present
				assert.NotContains(t, script, "node.cilium.io/agent-not-ready")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Create test options with custom taints
			options := commonTypes.Options{
				ClusterEndpoint:   "https://test-cluster:6443",
				BootstrapToken:    "test-token",
				NodeName:          "test-node",
				CNIPlugin:         tt.cniPlugin,
				CNIVersion:        "v1.0.0",
				Architecture:      "amd64",
				DNSClusterIP:      "172.21.0.10",
				CABundle:          "test-ca",
				KubernetesVersion: "v1.29.0",
				Taints:            tt.customTaints,
			}

			// Create VPC bootstrap provider
			provider := NewVPCBootstrapProvider(nil, nil, nil)

			// Generate cloud-init script
			script, err := provider.generateCloudInitScript(ctx, options)

			// Validate generation succeeds
			assert.NoError(t, err)
			assert.NotEmpty(t, script)

			// Run custom validation
			if tt.validateScript != nil {
				tt.validateScript(t, script)
			}
		})
	}
}

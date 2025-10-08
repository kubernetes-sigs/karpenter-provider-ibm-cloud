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
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	commonTypes "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/types"
)

func TestGenerateCloudInitScript(t *testing.T) {
	client := &ibm.Client{}
	provider := NewVPCBootstrapProvider(client, nil, nil)

	t.Run("Basic cloud-init generation", func(t *testing.T) {
		options := commonTypes.Options{
			ClusterEndpoint:   "https://api.cluster.example.com",
			BootstrapToken:    "test-token-123",
			DNSClusterIP:      "10.96.0.10",
			Region:            "us-south",
			Zone:              "us-south-1",
			NodeName:          "test-node-001",
			KubernetesVersion: "v1.28.0",
			ContainerRuntime:  "containerd",
			CNIPlugin:         "calico",
			CNIVersion:        "v3.26.0",
			Architecture:      "amd64",
			CABundle:          "-----BEGIN CERTIFICATE-----\nMIIBkTCB...\n-----END CERTIFICATE-----",
			AdditionalCAs:     []string{"-----BEGIN CERTIFICATE-----\nMIIBkTCB...\n-----END CERTIFICATE-----"},
			KubeletClientCAs:  []string{"-----BEGIN CERTIFICATE-----\nMIIBkTCB...\n-----END CERTIFICATE-----"},
		}

		script, err := provider.generateCloudInitScript(context.Background(), options)
		require.NoError(t, err)
		assert.NotEmpty(t, script)

		// Verify key components are present
		assert.Contains(t, script, "#!/bin/bash")
		assert.Contains(t, script, "CLUSTER_ENDPOINT=\"https://api.cluster.example.com\"")
		assert.Contains(t, script, "BOOTSTRAP_TOKEN=\"test-token-123\"")
		assert.Contains(t, script, "CLUSTER_DNS=\"10.96.0.10\"")
		assert.Contains(t, script, "REGION=\"us-south\"")
		assert.Contains(t, script, "ZONE=\"us-south-1\"")
		assert.Contains(t, script, "NODE_NAME=\"test-node-001\"")
		assert.Contains(t, script, "containerd")
		assert.Contains(t, script, "calico")

		// Verify error handling and status reporting
		assert.Contains(t, script, "report_status")
		assert.Contains(t, script, "karpenter-bootstrap.log")
		assert.Contains(t, script, "failed")
		assert.Contains(t, script, "completed")

		// Verify instance metadata handling
		assert.Contains(t, script, "169.254.169.254")
		assert.Contains(t, script, "instance_identity")
		assert.Contains(t, script, "metadata/v1/instance")

		// Verify CNI configuration
		assert.Contains(t, script, "calico")
		assert.Contains(t, script, "/etc/cni/net.d/10-calico.conflist")
		assert.Contains(t, script, "/opt/cni/bin/calico")

		// Verify kubelet configuration
		assert.Contains(t, script, "kubelet")
		assert.Contains(t, script, "/var/lib/kubelet/config.yaml")
		assert.Contains(t, script, "bootstrap-kubeconfig")
	})

	t.Run("CRI-O container runtime", func(t *testing.T) {
		options := commonTypes.Options{
			ClusterEndpoint:   "https://api.cluster.example.com",
			BootstrapToken:    "test-token-123",
			DNSClusterIP:      "10.96.0.10",
			Region:            "us-south",
			Zone:              "us-south-1",
			NodeName:          "test-node-001",
			KubernetesVersion: "v1.28.0",
			ContainerRuntime:  "cri-o",
			CNIPlugin:         "calico",
			CNIVersion:        "v3.26.0",
			Architecture:      "amd64",
			CABundle:          "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----",
		}

		script, err := provider.generateCloudInitScript(context.Background(), options)
		require.NoError(t, err)

		// Verify CRI-O specific configuration
		assert.Contains(t, script, "cri-o")
		assert.Contains(t, script, "install_crio")
		assert.Contains(t, script, "systemctl enable crio")
	})

	t.Run("Cilium CNI plugin", func(t *testing.T) {
		options := commonTypes.Options{
			ClusterEndpoint:   "https://api.cluster.example.com",
			BootstrapToken:    "test-token-123",
			DNSClusterIP:      "10.96.0.10",
			Region:            "us-south",
			Zone:              "us-south-1",
			NodeName:          "test-node-001",
			KubernetesVersion: "v1.28.0",
			ContainerRuntime:  "containerd",
			CNIPlugin:         "cilium",
			CNIVersion:        "v1.14.0",
			Architecture:      "amd64",
			CABundle:          "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----",
		}

		script, err := provider.generateCloudInitScript(context.Background(), options)
		require.NoError(t, err)

		// Verify Cilium specific configuration
		assert.Contains(t, script, "cilium")
		assert.Contains(t, script, "00-cilium-bootstrap.conflist")
		assert.Contains(t, script, "node.cilium.io/agent-not-ready")
		assert.Contains(t, script, "192.168.200.0/24")          // Bootstrap network
		assert.Contains(t, script, "isDefaultGateway\": false") // No route conflicts
	})

	t.Run("Flannel CNI plugin", func(t *testing.T) {
		options := commonTypes.Options{
			ClusterEndpoint:   "https://api.cluster.example.com",
			BootstrapToken:    "test-token-123",
			DNSClusterIP:      "10.96.0.10",
			Region:            "us-south",
			Zone:              "us-south-1",
			NodeName:          "test-node-001",
			KubernetesVersion: "v1.28.0",
			ContainerRuntime:  "containerd",
			CNIPlugin:         "flannel",
			CNIVersion:        "v1.2.0",
			Architecture:      "amd64",
			CABundle:          "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----",
		}

		script, err := provider.generateCloudInitScript(context.Background(), options)
		require.NoError(t, err)

		// Verify Flannel specific configuration
		assert.Contains(t, script, "flannel")
		assert.Contains(t, script, "/etc/cni/net.d/10-flannel.conflist")
		assert.Contains(t, script, "/opt/cni/bin/flannel")
	})

	t.Run("With taints", func(t *testing.T) {
		options := commonTypes.Options{
			ClusterEndpoint:   "https://api.cluster.example.com",
			BootstrapToken:    "test-token-123",
			DNSClusterIP:      "10.96.0.10",
			Region:            "us-south",
			Zone:              "us-south-1",
			NodeName:          "test-node-001",
			KubernetesVersion: "v1.28.0",
			ContainerRuntime:  "containerd",
			CNIPlugin:         "calico",
			CNIVersion:        "v3.26.0",
			Architecture:      "amd64",
			CABundle:          "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----",
			Taints: []corev1.Taint{
				{Key: "example.com/special", Value: "true", Effect: "NoSchedule"},
				{Key: "node.kubernetes.io/spot", Value: "", Effect: "NoSchedule"},
			},
		}

		script, err := provider.generateCloudInitScript(context.Background(), options)
		require.NoError(t, err)

		// Verify taints are included
		assert.Contains(t, script, "registerWithTaints:")
		assert.Contains(t, script, "example.com/special")
		assert.Contains(t, script, "node.kubernetes.io/spot")
		assert.Contains(t, script, "NoSchedule")
	})

	t.Run("With labels", func(t *testing.T) {
		options := commonTypes.Options{
			ClusterEndpoint:   "https://api.cluster.example.com",
			BootstrapToken:    "test-token-123",
			DNSClusterIP:      "10.96.0.10",
			Region:            "us-south",
			Zone:              "us-south-1",
			NodeName:          "test-node-001",
			KubernetesVersion: "v1.28.0",
			ContainerRuntime:  "containerd",
			CNIPlugin:         "calico",
			CNIVersion:        "v3.26.0",
			Architecture:      "amd64",
			CABundle:          "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----",
			Labels: map[string]string{
				"node.kubernetes.io/instance-type": "bx2-4x16",
				"topology.kubernetes.io/zone":      "us-south-1",
				"app.kubernetes.io/managed-by":     "karpenter",
			},
		}

		script, err := provider.generateCloudInitScript(context.Background(), options)
		require.NoError(t, err)

		// Verify labels are included
		assert.Contains(t, script, "nodeLabels:")
		assert.Contains(t, script, "node.kubernetes.io/instance-type")
		assert.Contains(t, script, "bx2-4x16")
		assert.Contains(t, script, "topology.kubernetes.io/zone")
		assert.Contains(t, script, "us-south-1")
		assert.Contains(t, script, "app.kubernetes.io/managed-by")
		assert.Contains(t, script, "karpenter")
	})

	t.Run("With custom user data", func(t *testing.T) {
		options := commonTypes.Options{
			ClusterEndpoint:   "https://api.cluster.example.com",
			BootstrapToken:    "test-token-123",
			DNSClusterIP:      "10.96.0.10",
			Region:            "us-south",
			Zone:              "us-south-1",
			NodeName:          "test-node-001",
			KubernetesVersion: "v1.28.0",
			ContainerRuntime:  "containerd",
			CNIPlugin:         "calico",
			CNIVersion:        "v3.26.0",
			Architecture:      "amd64",
			CABundle:          "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----",
			CustomUserData:    "echo 'Custom setup complete'\napt-get install -y htop",
		}

		script, err := provider.generateCloudInitScript(context.Background(), options)
		require.NoError(t, err)

		// Verify custom user data is included
		assert.Contains(t, script, "Running custom user data...")
		assert.Contains(t, script, "echo 'Custom setup complete'")
		assert.Contains(t, script, "apt-get install -y htop")
	})

	t.Run("With additional CA environment variable", func(t *testing.T) {
		// Set environment variable
		additionalCA := "-----BEGIN CERTIFICATE-----\nAdditional CA\n-----END CERTIFICATE-----"
		_ = os.Setenv("ca_crt", additionalCA)
		defer func() { _ = os.Unsetenv("ca_crt") }()

		options := commonTypes.Options{
			ClusterEndpoint:   "https://api.cluster.example.com",
			BootstrapToken:    "test-token-123",
			DNSClusterIP:      "10.96.0.10",
			Region:            "us-south",
			Zone:              "us-south-1",
			NodeName:          "test-node-001",
			KubernetesVersion: "v1.28.0",
			ContainerRuntime:  "containerd",
			CNIPlugin:         "calico",
			CNIVersion:        "v3.26.0",
			Architecture:      "amd64",
			CABundle:          "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----",
		}

		script, err := provider.generateCloudInitScript(context.Background(), options)
		require.NoError(t, err)

		// Verify additional CA is injected
		assert.Contains(t, script, "export KARPENTER_ADDITIONAL_CA=")
		assert.Contains(t, script, "Additional CA")
	})
}

func TestBuildKubeletExtraArgs(t *testing.T) {
	client := &ibm.Client{}
	provider := NewVPCBootstrapProvider(client, nil, nil)

	t.Run("No extra args", func(t *testing.T) {
		result := provider.buildKubeletExtraArgs(nil)
		assert.Empty(t, result)

		config := &commonTypes.KubeletConfig{}
		result = provider.buildKubeletExtraArgs(config)
		assert.Empty(t, result)
	})

	t.Run("Single extra arg", func(t *testing.T) {
		config := &commonTypes.KubeletConfig{
			ExtraArgs: map[string]string{
				"max-pods": "110",
			},
		}

		result := provider.buildKubeletExtraArgs(config)
		assert.Equal(t, "--max-pods=110", result)
	})

	t.Run("Multiple extra args", func(t *testing.T) {
		config := &commonTypes.KubeletConfig{
			ExtraArgs: map[string]string{
				"max-pods":                   "110",
				"kube-reserved":              "cpu=100m,memory=100Mi",
				"system-reserved":            "cpu=100m,memory=100Mi",
				"eviction-hard":              "memory.available<500Mi",
				"cluster-domain":             "cluster.local",
				"container-runtime":          "remote",
				"container-runtime-endpoint": "unix:///var/run/containerd/containerd.sock",
			},
		}

		result := provider.buildKubeletExtraArgs(config)

		// Since map iteration order is not guaranteed, check that all args are present
		assert.Contains(t, result, "--max-pods=110")
		assert.Contains(t, result, "--kube-reserved=cpu=100m,memory=100Mi")
		assert.Contains(t, result, "--system-reserved=cpu=100m,memory=100Mi")
		assert.Contains(t, result, "--eviction-hard=memory.available<500Mi")
		assert.Contains(t, result, "--cluster-domain=cluster.local")
		assert.Contains(t, result, "--container-runtime=remote")
		assert.Contains(t, result, "--container-runtime-endpoint=unix:///var/run/containerd/containerd.sock")

		// Count the number of arguments
		argCount := strings.Count(result, "--")
		assert.Equal(t, 7, argCount)
	})

	t.Run("Empty values", func(t *testing.T) {
		config := &commonTypes.KubeletConfig{
			ExtraArgs: map[string]string{
				"some-flag": "",
				"max-pods":  "110",
			},
		}

		result := provider.buildKubeletExtraArgs(config)
		assert.Contains(t, result, "--some-flag=")
		assert.Contains(t, result, "--max-pods=110")
	})

	t.Run("Special characters in values", func(t *testing.T) {
		config := &commonTypes.KubeletConfig{
			ExtraArgs: map[string]string{
				"eviction-hard":             "memory.available<500Mi,nodefs.available<10%",
				"feature-gates":             "EphemeralContainers=true,CSIStorageCapacity=true",
				"authentication-kubeconfig": "/var/lib/kubelet/kubeconfig",
			},
		}

		result := provider.buildKubeletExtraArgs(config)
		assert.Contains(t, result, "--eviction-hard=memory.available<500Mi,nodefs.available<10%")
		assert.Contains(t, result, "--feature-gates=EphemeralContainers=true,CSIStorageCapacity=true")
		assert.Contains(t, result, "--authentication-kubeconfig=/var/lib/kubelet/kubeconfig")
	})
}

func TestCloudInitTemplate_EdgeCases(t *testing.T) {
	client := &ibm.Client{}
	provider := NewVPCBootstrapProvider(client, nil, nil)

	t.Run("Unsupported container runtime", func(t *testing.T) {
		options := commonTypes.Options{
			ClusterEndpoint:   "https://api.cluster.example.com",
			BootstrapToken:    "test-token-123",
			DNSClusterIP:      "10.96.0.10",
			Region:            "us-south",
			Zone:              "us-south-1",
			NodeName:          "test-node-001",
			KubernetesVersion: "v1.28.0",
			ContainerRuntime:  "podman", // Unsupported
			CNIPlugin:         "calico",
			CNIVersion:        "v3.26.0",
			Architecture:      "amd64",
			CABundle:          "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----",
		}

		script, err := provider.generateCloudInitScript(context.Background(), options)
		require.NoError(t, err)

		// Verify error handling for unsupported runtime
		assert.Contains(t, script, "podman")
		assert.Contains(t, script, "containerd")
	})

	t.Run("Unknown CNI plugin", func(t *testing.T) {
		options := commonTypes.Options{
			ClusterEndpoint:   "https://api.cluster.example.com",
			BootstrapToken:    "test-token-123",
			DNSClusterIP:      "10.96.0.10",
			Region:            "us-south",
			Zone:              "us-south-1",
			NodeName:          "test-node-001",
			KubernetesVersion: "v1.28.0",
			ContainerRuntime:  "containerd",
			CNIPlugin:         "weave", // Unknown
			CNIVersion:        "v2.8.1",
			Architecture:      "amd64",
			CABundle:          "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----",
		}

		script, err := provider.generateCloudInitScript(context.Background(), options)
		require.NoError(t, err)

		// Verify handling of unknown CNI plugin
		assert.Contains(t, script, "weave")
		assert.Contains(t, script, "DaemonSet")
	})

	t.Run("Empty values", func(t *testing.T) {
		options := commonTypes.Options{
			ClusterEndpoint:   "",
			BootstrapToken:    "",
			DNSClusterIP:      "",
			Region:            "",
			Zone:              "",
			NodeName:          "",
			KubernetesVersion: "",
			ContainerRuntime:  "",
			CNIPlugin:         "",
			CNIVersion:        "",
			Architecture:      "",
			CABundle:          "",
		}

		script, err := provider.generateCloudInitScript(context.Background(), options)
		require.NoError(t, err)

		// Should still generate a script but with empty values
		assert.Contains(t, script, "#!/bin/bash")
		assert.Contains(t, script, "CLUSTER_ENDPOINT=\"\"")
		assert.Contains(t, script, "BOOTSTRAP_TOKEN=\"\"")
		assert.Contains(t, script, "NODE_NAME=\"\"")
	})

	t.Run("Architecture handling", func(t *testing.T) {
		testCases := []struct {
			arch     string
			expected string
		}{
			{"amd64", "amd64"},
			{"arm64", "arm64"},
			{"s390x", "s390x"},
			{"", ""}, // Should handle empty
		}

		for _, tc := range testCases {
			t.Run(tc.arch, func(t *testing.T) {
				options := commonTypes.Options{
					ClusterEndpoint:   "https://api.cluster.example.com",
					BootstrapToken:    "test-token-123",
					DNSClusterIP:      "10.96.0.10",
					Region:            "us-south",
					Zone:              "us-south-1",
					NodeName:          "test-node-001",
					KubernetesVersion: "v1.28.0",
					ContainerRuntime:  "containerd",
					CNIPlugin:         "calico",
					CNIVersion:        "v3.26.0",
					Architecture:      tc.arch,
					CABundle:          "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----",
				}

				script, err := provider.generateCloudInitScript(context.Background(), options)
				require.NoError(t, err)

				if tc.expected != "" {
					assert.Contains(t, script, fmt.Sprintf("linux-%s-", tc.expected))
				}
			})
		}
	})
}

func TestCloudInitTemplate_SecurityFeatures(t *testing.T) {
	client := &ibm.Client{}
	provider := NewVPCBootstrapProvider(client, nil, nil)

	t.Run("Status reporting security", func(t *testing.T) {
		options := commonTypes.Options{
			ClusterEndpoint:   "https://api.cluster.example.com",
			BootstrapToken:    "test-token-123",
			DNSClusterIP:      "10.96.0.10",
			Region:            "us-south",
			Zone:              "us-south-1",
			NodeName:          "test-node-001",
			KubernetesVersion: "v1.28.0",
			ContainerRuntime:  "containerd",
			CNIPlugin:         "calico",
			CNIVersion:        "v3.26.0",
			Architecture:      "amd64",
			CABundle:          "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----",
		}

		script, err := provider.generateCloudInitScript(context.Background(), options)
		require.NoError(t, err)

		// Verify security features
		assert.Contains(t, script, "karpenter-bootstrap-status.json") // Structured status
		assert.Contains(t, script, "karpenter-bootstrap-failure.log") // Failure diagnostics
		assert.Contains(t, script, "Recent system logs")              // Debug info
		assert.Contains(t, script, "Network interface status")        // Network debug
		assert.Contains(t, script, "DNS resolution test")             // DNS debug
	})

	t.Run("Error handling and diagnostics", func(t *testing.T) {
		options := commonTypes.Options{
			ClusterEndpoint:   "https://api.cluster.example.com",
			BootstrapToken:    "test-token-123",
			DNSClusterIP:      "10.96.0.10",
			Region:            "us-south",
			Zone:              "us-south-1",
			NodeName:          "test-node-001",
			KubernetesVersion: "v1.28.0",
			ContainerRuntime:  "containerd",
			CNIPlugin:         "calico",
			CNIVersion:        "v3.26.0",
			Architecture:      "amd64",
			CABundle:          "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----",
		}

		script, err := provider.generateCloudInitScript(context.Background(), options)
		require.NoError(t, err)

		// Verify comprehensive error handling
		assert.Contains(t, script, "instance-identity-token-failed")
		assert.Contains(t, script, "instance-id-metadata-failed")
		assert.Contains(t, script, "Could not get instance identity token")
		assert.Contains(t, script, "Could not retrieve instance ID")
		assert.Contains(t, script, "BOOTSTRAP FAILURE DIAGNOSTICS")
	})

	t.Run("CA bundle handling", func(t *testing.T) {
		options := commonTypes.Options{
			ClusterEndpoint:   "https://api.cluster.example.com",
			BootstrapToken:    "test-token-123",
			DNSClusterIP:      "10.96.0.10",
			Region:            "us-south",
			Zone:              "us-south-1",
			NodeName:          "test-node-001",
			KubernetesVersion: "v1.28.0",
			ContainerRuntime:  "containerd",
			CNIPlugin:         "calico",
			CNIVersion:        "v3.26.0",
			Architecture:      "amd64",
			CABundle:          "-----BEGIN CERTIFICATE-----\nPrimary CA\n-----END CERTIFICATE-----",
			AdditionalCAs: []string{
				"-----BEGIN CERTIFICATE-----\nAdditional CA 1\n-----END CERTIFICATE-----",
				"-----BEGIN CERTIFICATE-----\nAdditional CA 2\n-----END CERTIFICATE-----",
			},
			KubeletClientCAs: []string{
				"-----BEGIN CERTIFICATE-----\nKubelet CA\n-----END CERTIFICATE-----",
			},
		}

		script, err := provider.generateCloudInitScript(context.Background(), options)
		require.NoError(t, err)

		// Verify CA handling
		assert.Contains(t, script, "Primary CA certificate created")
		assert.Contains(t, script, "Combined CA certificate created")
		assert.Contains(t, script, "Primary CA")
		assert.Contains(t, script, "Additional CA 1")
		assert.Contains(t, script, "Additional CA 2")
		assert.Contains(t, script, "Kubelet CA")
		assert.Contains(t, script, "KARPENTER_ADDITIONAL_CA")
		assert.Contains(t, script, "/etc/kubernetes/additional-ca.crt")
	})
}

func TestBootstrapEnvironmentVariableInjection(t *testing.T) {
	client := &ibm.Client{}
	provider := NewVPCBootstrapProvider(client, nil, nil)

	t.Run("inject single BOOTSTRAP_ variable", func(t *testing.T) {
		// Set test environment variable
		require.NoError(t, os.Setenv("BOOTSTRAP_TEST_VAR", "test-value-123"))
		defer func() {
			_ = os.Unsetenv("BOOTSTRAP_TEST_VAR")
		}()

		options := commonTypes.Options{
			ClusterEndpoint:   "https://api.cluster.example.com",
			BootstrapToken:    "test-token",
			DNSClusterIP:      "10.96.0.10",
			Region:            "us-south",
			Zone:              "us-south-1",
			NodeName:          "test-node",
			KubernetesVersion: "v1.28.0",
			ContainerRuntime:  "containerd",
			CNIPlugin:         "calico",
			CNIVersion:        "v3.26.0",
			Architecture:      "amd64",
			CABundle:          "test-ca",
		}

		script, err := provider.generateCloudInitScript(context.Background(), options)
		require.NoError(t, err)

		// Verify variable is injected near the top of the script
		assert.Contains(t, script, `export BOOTSTRAP_TEST_VAR="test-value-123"`)

		// Verify it comes after shebang but before main script logic
		lines := strings.Split(script, "\n")
		foundShebang := false
		foundExport := false
		foundMainLogic := false

		for _, line := range lines {
			if strings.Contains(line, "#!/bin/bash") {
				foundShebang = true
			}
			if strings.Contains(line, "export BOOTSTRAP_TEST_VAR") {
				foundExport = true
				assert.True(t, foundShebang, "Export should come after shebang")
				assert.False(t, foundMainLogic, "Export should come before main logic")
			}
			if strings.Contains(line, "===== Karpenter IBM Cloud Bootstrap") {
				foundMainLogic = true
			}
		}

		assert.True(t, foundExport, "BOOTSTRAP_ variable should be injected")
	})

	t.Run("inject multiple BOOTSTRAP_ variables", func(t *testing.T) {
		// Set multiple test environment variables
		require.NoError(t, os.Setenv("BOOTSTRAP_SERVER", "https://server:9345"))
		require.NoError(t, os.Setenv("BOOTSTRAP_TOKEN", "secret-token-abc123"))
		require.NoError(t, os.Setenv("BOOTSTRAP_VERSION", "v1.30.2"))
		defer func() {
			_ = os.Unsetenv("BOOTSTRAP_SERVER")
			_ = os.Unsetenv("BOOTSTRAP_TOKEN")
			_ = os.Unsetenv("BOOTSTRAP_VERSION")
		}()

		options := commonTypes.Options{
			ClusterEndpoint:   "https://api.cluster.example.com",
			BootstrapToken:    "test-token",
			DNSClusterIP:      "10.96.0.10",
			Region:            "us-south",
			Zone:              "us-south-1",
			NodeName:          "test-node",
			KubernetesVersion: "v1.28.0",
			ContainerRuntime:  "containerd",
			CNIPlugin:         "calico",
			CNIVersion:        "v3.26.0",
			Architecture:      "amd64",
			CABundle:          "test-ca",
		}

		script, err := provider.generateCloudInitScript(context.Background(), options)
		require.NoError(t, err)

		// Verify all variables are injected
		assert.Contains(t, script, `export BOOTSTRAP_SERVER="https://server:9345"`)
		assert.Contains(t, script, `export BOOTSTRAP_TOKEN="secret-token-abc123"`)
		assert.Contains(t, script, `export BOOTSTRAP_VERSION="v1.30.2"`)
	})

	t.Run("ignore non-BOOTSTRAP_ variables", func(t *testing.T) {
		// Set variables without BOOTSTRAP_ prefix
		require.NoError(t, os.Setenv("MY_VAR", "should-not-be-injected"))
		require.NoError(t, os.Setenv("TEST_VAR", "also-should-not-be-injected"))
		defer func() {
			_ = os.Unsetenv("MY_VAR")
			_ = os.Unsetenv("TEST_VAR")
		}()

		options := commonTypes.Options{
			ClusterEndpoint:   "https://api.cluster.example.com",
			BootstrapToken:    "test-token",
			DNSClusterIP:      "10.96.0.10",
			Region:            "us-south",
			Zone:              "us-south-1",
			NodeName:          "test-node",
			KubernetesVersion: "v1.28.0",
			ContainerRuntime:  "containerd",
			CNIPlugin:         "calico",
			CNIVersion:        "v3.26.0",
			Architecture:      "amd64",
			CABundle:          "test-ca",
		}

		script, err := provider.generateCloudInitScript(context.Background(), options)
		require.NoError(t, err)

		// Verify non-BOOTSTRAP_ variables are NOT injected
		assert.NotContains(t, script, "MY_VAR")
		assert.NotContains(t, script, "TEST_VAR")
		assert.NotContains(t, script, "should-not-be-injected")
		assert.NotContains(t, script, "also-should-not-be-injected")
	})

	t.Run("handle variables with special characters", func(t *testing.T) {
		// Set variable with special characters that need quoting
		require.NoError(t, os.Setenv("BOOTSTRAP_SPECIAL", "value with spaces and $pecial chars!"))
		defer func() {
			_ = os.Unsetenv("BOOTSTRAP_SPECIAL")
		}()

		options := commonTypes.Options{
			ClusterEndpoint:   "https://api.cluster.example.com",
			BootstrapToken:    "test-token",
			DNSClusterIP:      "10.96.0.10",
			Region:            "us-south",
			Zone:              "us-south-1",
			NodeName:          "test-node",
			KubernetesVersion: "v1.28.0",
			ContainerRuntime:  "containerd",
			CNIPlugin:         "calico",
			CNIVersion:        "v3.26.0",
			Architecture:      "amd64",
			CABundle:          "test-ca",
		}

		script, err := provider.generateCloudInitScript(context.Background(), options)
		require.NoError(t, err)

		// Verify variable is properly quoted
		assert.Contains(t, script, `export BOOTSTRAP_SPECIAL="value with spaces and $pecial chars!"`)
	})

	t.Run("no BOOTSTRAP_ variables set", func(t *testing.T) {
		// Ensure no BOOTSTRAP_ variables are set
		for _, env := range os.Environ() {
			if strings.HasPrefix(env, "BOOTSTRAP_") {
				parts := strings.SplitN(env, "=", 2)
				_ = os.Unsetenv(parts[0])
			}
		}

		options := commonTypes.Options{
			ClusterEndpoint:   "https://api.cluster.example.com",
			BootstrapToken:    "test-token",
			DNSClusterIP:      "10.96.0.10",
			Region:            "us-south",
			Zone:              "us-south-1",
			NodeName:          "test-node",
			KubernetesVersion: "v1.28.0",
			ContainerRuntime:  "containerd",
			CNIPlugin:         "calico",
			CNIVersion:        "v3.26.0",
			Architecture:      "amd64",
			CABundle:          "test-ca",
		}

		script, err := provider.generateCloudInitScript(context.Background(), options)
		require.NoError(t, err)

		// Script should still be generated successfully without BOOTSTRAP_ vars
		assert.NotEmpty(t, script)
		assert.Contains(t, script, "#!/bin/bash")
		assert.Contains(t, script, "Karpenter IBM Cloud Bootstrap")
	})

	t.Run("RKE2 use case simulation", func(t *testing.T) {
		// Simulate RKE2 bootstrap scenario
		require.NoError(t, os.Setenv("BOOTSTRAP_RKE2_SERVER", "https://10.20.5.71:9345"))
		require.NoError(t, os.Setenv("BOOTSTRAP_RKE2_TOKEN", "K1060b31293d57afc0eda682e6d097d21f7f21ad383318a3a49e1dacb9f635bfe93::server:6bd454f63cb03f1cdacc18b7a245bec3"))
		require.NoError(t, os.Setenv("BOOTSTRAP_RKE2_VERSION", "v1.30.2+rke2r1"))
		defer func() {
			_ = os.Unsetenv("BOOTSTRAP_RKE2_SERVER")
			_ = os.Unsetenv("BOOTSTRAP_RKE2_TOKEN")
			_ = os.Unsetenv("BOOTSTRAP_RKE2_VERSION")
		}()

		options := commonTypes.Options{
			ClusterEndpoint:   "https://api.cluster.example.com",
			BootstrapToken:    "test-token",
			DNSClusterIP:      "10.96.0.10",
			Region:            "us-south",
			Zone:              "us-south-1",
			NodeName:          "test-node",
			KubernetesVersion: "v1.28.0",
			ContainerRuntime:  "containerd",
			CNIPlugin:         "calico",
			CNIVersion:        "v3.26.0",
			Architecture:      "amd64",
			CABundle:          "test-ca",
		}

		script, err := provider.generateCloudInitScript(context.Background(), options)
		require.NoError(t, err)

		// Verify RKE2 variables are injected
		assert.Contains(t, script, `export BOOTSTRAP_RKE2_SERVER="https://10.20.5.71:9345"`)
		assert.Contains(t, script, `export BOOTSTRAP_RKE2_TOKEN="K1060b31293d57afc0eda682e6d097d21f7f21ad383318a3a49e1dacb9f635bfe93::server:6bd454f63cb03f1cdacc18b7a245bec3"`)
		assert.Contains(t, script, `export BOOTSTRAP_RKE2_VERSION="v1.30.2+rke2r1"`)

		// Verify they appear early in the script
		shebangIndex := strings.Index(script, "#!/bin/bash")
		serverIndex := strings.Index(script, "BOOTSTRAP_RKE2_SERVER")
		tokenIndex := strings.Index(script, "BOOTSTRAP_RKE2_TOKEN")
		mainLogicIndex := strings.Index(script, "Karpenter IBM Cloud Bootstrap")

		assert.True(t, shebangIndex < serverIndex, "Shebang should come before SERVER export")
		assert.True(t, serverIndex < mainLogicIndex, "SERVER export should come before main logic")
		assert.True(t, tokenIndex < mainLogicIndex, "TOKEN export should come before main logic")
	})
}

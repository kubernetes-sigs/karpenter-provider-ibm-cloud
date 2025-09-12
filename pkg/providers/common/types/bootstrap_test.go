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

package types

import (
	"encoding/base64"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestBootstrapModeConstants(t *testing.T) {
	tests := []struct {
		name     string
		mode     BootstrapMode
		expected string
	}{
		{"CloudInit", BootstrapModeCloudInit, "cloud-init"},
		{"IKSAPI", BootstrapModeIKSAPI, "iks-api"},
		{"Auto", BootstrapModeAuto, "auto"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.mode) != tt.expected {
				t.Errorf("BootstrapMode %s = %v, want %v", tt.name, tt.mode, tt.expected)
			}
		})
	}
}

func TestOptionsStruct(t *testing.T) {
	options := Options{
		ClusterName:      "test-cluster",
		NodeName:         "test-node",
		ProviderID:       "ibm://us-south-1/instance-123",
		InstanceID:       "instance-123",
		ClusterEndpoint:  "https://test.example.com:6443",
		CABundle:         "LS0tLS1CRUdJTi0=",
		AdditionalCAs:    []string{"LS0tLS1CRUdJTi1BRERJVELPK0FM", "LS0tLS1CRUdJTi1BRERJVElPTkFM"},
		KubeletClientCAs: []string{"LS0tLS1CRUdJTi1LVUJFTEVULUNM"},
		BootstrapToken:   "abcdef.0123456789abcdef",
		ContainerRuntime: "containerd",
		CNIPlugin:        "calico",
		CNIVersion:       "3.27",
		Architecture:     "amd64",
		DNSClusterIP:     "172.21.0.10",
		ClusterCIDR:      "172.30.0.0/16",
		CustomUserData:   "custom data",
		Region:           "us-south",
		Zone:             "us-south-1",
		Labels:           map[string]string{"key": "value"},
	}

	if options.ClusterName != "test-cluster" {
		t.Errorf("Options.ClusterName = %v, want test-cluster", options.ClusterName)
	}
	if options.NodeName != "test-node" {
		t.Errorf("Options.NodeName = %v, want test-node", options.NodeName)
	}
	if options.ProviderID != "ibm://us-south-1/instance-123" {
		t.Errorf("Options.ProviderID = %v, want ibm://us-south-1/instance-123", options.ProviderID)
	}
	if len(options.AdditionalCAs) != 2 {
		t.Errorf("Options.AdditionalCAs length = %v, want 2", len(options.AdditionalCAs))
	}
	if options.AdditionalCAs[0] != "LS0tLS1CRUdJTi1BRERJVELPK0FM" {
		t.Errorf("Options.AdditionalCAs[0] = %v, want LS0tLS1CRUdJTi1BRERJVELPK0FM", options.AdditionalCAs[0])
	}
	if len(options.KubeletClientCAs) != 1 {
		t.Errorf("Options.KubeletClientCAs length = %v, want 1", len(options.KubeletClientCAs))
	}
	if options.KubeletClientCAs[0] != "LS0tLS1CRUdJTi1LVUJFTEVULUNM" {
		t.Errorf("Options.KubeletClientCAs[0] = %v, want LS0tLS1CRUdJTi1LVUJFTEVULUNM", options.KubeletClientCAs[0])
	}
}

func TestKubeletConfigStruct(t *testing.T) {
	maxPods := int32(110)
	kubeletConfig := KubeletConfig{
		ClusterDNS:   []string{"172.21.0.10", "8.8.8.8"},
		MaxPods:      &maxPods,
		ExtraArgs:    map[string]string{"arg1": "value1"},
		FeatureGates: map[string]bool{"gate1": true, "gate2": false},
	}

	if len(kubeletConfig.ClusterDNS) != 2 {
		t.Errorf("KubeletConfig.ClusterDNS length = %v, want 2", len(kubeletConfig.ClusterDNS))
	}
	if kubeletConfig.ClusterDNS[0] != "172.21.0.10" {
		t.Errorf("KubeletConfig.ClusterDNS[0] = %v, want 172.21.0.10", kubeletConfig.ClusterDNS[0])
	}
	if kubeletConfig.MaxPods == nil || *kubeletConfig.MaxPods != 110 {
		t.Errorf("KubeletConfig.MaxPods = %v, want 110", kubeletConfig.MaxPods)
	}
	if kubeletConfig.ExtraArgs["arg1"] != "value1" {
		t.Errorf("KubeletConfig.ExtraArgs[arg1] = %v, want value1", kubeletConfig.ExtraArgs["arg1"])
	}
	if !kubeletConfig.FeatureGates["gate1"] {
		t.Errorf("KubeletConfig.FeatureGates[gate1] = %v, want true", kubeletConfig.FeatureGates["gate1"])
	}
	if kubeletConfig.FeatureGates["gate2"] {
		t.Errorf("KubeletConfig.FeatureGates[gate2] = %v, want false", kubeletConfig.FeatureGates["gate2"])
	}
}

func TestIKSWorkerPoolOptionsStruct(t *testing.T) {
	options := IKSWorkerPoolOptions{
		ClusterID:     "cluster-123",
		WorkerPoolID:  "pool-456",
		VPCInstanceID: "instance-789",
		Zone:          "us-south-1",
	}

	if options.ClusterID != "cluster-123" {
		t.Errorf("IKSWorkerPoolOptions.ClusterID = %v, want cluster-123", options.ClusterID)
	}
	if options.WorkerPoolID != "pool-456" {
		t.Errorf("IKSWorkerPoolOptions.WorkerPoolID = %v, want pool-456", options.WorkerPoolID)
	}
	if options.VPCInstanceID != "instance-789" {
		t.Errorf("IKSWorkerPoolOptions.VPCInstanceID = %v, want instance-789", options.VPCInstanceID)
	}
	if options.Zone != "us-south-1" {
		t.Errorf("IKSWorkerPoolOptions.Zone = %v, want us-south-1", options.Zone)
	}
}

func TestClusterInfoStruct(t *testing.T) {
	caData := []byte("test-ca-data")
	bootstrapToken := &corev1.Secret{Data: map[string][]byte{"token": []byte("test-token")}}

	clusterInfo := ClusterInfo{
		Endpoint:        "https://test.example.com:6443",
		CAData:          caData,
		BootstrapToken:  bootstrapToken,
		ClusterName:     "test-cluster",
		IKSClusterID:    "iks-123",
		IsIKSManaged:    true,
		IKSWorkerPoolID: "pool-456",
	}

	if clusterInfo.Endpoint != "https://test.example.com:6443" {
		t.Errorf("ClusterInfo.Endpoint = %v, want https://test.example.com:6443", clusterInfo.Endpoint)
	}
	if string(clusterInfo.CAData) != "test-ca-data" {
		t.Errorf("ClusterInfo.CAData = %v, want test-ca-data", string(clusterInfo.CAData))
	}
	if clusterInfo.BootstrapToken != bootstrapToken {
		t.Errorf("ClusterInfo.BootstrapToken = %v, want %v", clusterInfo.BootstrapToken, bootstrapToken)
	}
	if !clusterInfo.IsIKSManaged {
		t.Errorf("ClusterInfo.IsIKSManaged = %v, want true", clusterInfo.IsIKSManaged)
	}
}

func TestParseKubeconfig(t *testing.T) {
	// Test valid kubeconfig
	testCAData := "dGVzdC1jYS1kYXRh" // base64 encoded "test-ca-data"
	validKubeconfig := `apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: ` + testCAData + `
    server: https://test.example.com:6443
  name: test-cluster
contexts:
- context:
    cluster: test-cluster
    user: test-user
  name: test-context
current-context: test-context
kind: Config
users:
- name: test-user`

	endpoint, caData, err := ParseKubeconfig(validKubeconfig)
	if err != nil {
		t.Errorf("ParseKubeconfig returned error: %v", err)
	}

	expectedEndpoint := "https://test.example.com:6443"
	if endpoint != expectedEndpoint {
		t.Errorf("ParseKubeconfig endpoint = %v, want %v", endpoint, expectedEndpoint)
	}

	expectedCAData := "test-ca-data"
	if string(caData) != expectedCAData {
		t.Errorf("ParseKubeconfig caData = %v, want %v", string(caData), expectedCAData)
	}
}

func TestParseKubeconfigMissingServer(t *testing.T) {
	kubeconfigMissingServer := `apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: dGVzdC1jYS1kYXRh
  name: test-cluster`

	_, _, err := ParseKubeconfig(kubeconfigMissingServer)
	if err == nil {
		t.Error("ParseKubeconfig should return error for missing server")
	}
	if err.Error() != "endpoint not found in kubeconfig" {
		t.Errorf("ParseKubeconfig error = %v, want 'endpoint not found in kubeconfig'", err.Error())
	}
}

func TestParseKubeconfigInvalidCAData(t *testing.T) {
	kubeconfigInvalidCA := `apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: invalid-base64!!!
    server: https://test.example.com:6443
  name: test-cluster`

	_, _, err := ParseKubeconfig(kubeconfigInvalidCA)
	if err == nil {
		t.Error("ParseKubeconfig should return error for invalid base64 CA data")
	}
	if !containsString(err.Error(), "decoding CA data") {
		t.Errorf("ParseKubeconfig error should contain 'decoding CA data', got: %v", err.Error())
	}
}

func TestParseKubeconfigMissingCAData(t *testing.T) {
	kubeconfigMissingCA := `apiVersion: v1
clusters:
- cluster:
    server: https://test.example.com:6443
  name: test-cluster`

	endpoint, caData, err := ParseKubeconfig(kubeconfigMissingCA)
	if err != nil {
		t.Errorf("ParseKubeconfig returned unexpected error: %v", err)
	}

	expectedEndpoint := "https://test.example.com:6443"
	if endpoint != expectedEndpoint {
		t.Errorf("ParseKubeconfig endpoint = %v, want %v", endpoint, expectedEndpoint)
	}

	if caData != nil {
		t.Errorf("ParseKubeconfig caData should be nil when missing, got: %v", caData)
	}
}

func TestParseKubeconfigWithSpacing(t *testing.T) {
	// Test with various spacing scenarios
	kubeconfigWithSpacing := `apiVersion: v1
clusters:
- cluster:
    certificate-authority-data:    ` + base64.StdEncoding.EncodeToString([]byte("test-ca-data")) + `
    server:   https://test.example.com:6443
  name: test-cluster`

	endpoint, caData, err := ParseKubeconfig(kubeconfigWithSpacing)
	if err != nil {
		t.Errorf("ParseKubeconfig returned error: %v", err)
	}

	if endpoint != "https://test.example.com:6443" {
		t.Errorf("ParseKubeconfig should handle extra spaces, endpoint = %v", endpoint)
	}

	if string(caData) != "test-ca-data" {
		t.Errorf("ParseKubeconfig should handle extra spaces, caData = %v", string(caData))
	}
}

func TestOptionsWithEmptyCAs(t *testing.T) {
	options := Options{
		ClusterName:      "test-cluster",
		CABundle:         "LS0tLS1CRUdJTi0=",
		AdditionalCAs:    []string{},
		KubeletClientCAs: nil,
	}

	if len(options.AdditionalCAs) != 0 {
		t.Errorf("Options.AdditionalCAs should be empty, got length %v", len(options.AdditionalCAs))
	}
	if options.KubeletClientCAs != nil {
		t.Errorf("Options.KubeletClientCAs should be nil, got %v", options.KubeletClientCAs)
	}
}

func TestOptionsWithMultipleCAs(t *testing.T) {
	additionalCAs := []string{
		"LS0tLS1CRUdJTi1BRERJVELPK0FMUzEtLS0tLQ==",
		"LS0tLS1CRUdJTi1BRERJVELPK0FMUzItLS0tLQ==",
		"LS0tLS1CRUdJTi1BRERJVELPK0FMUzMtLS0tLQ==",
	}
	kubeletClientCAs := []string{
		"LS0tLS1CRUdJTi1LVUJFTEVULUNMMQ==",
		"LS0tLS1CRUdJTi1LVUJFTEVULUNMMI==",
	}

	options := Options{
		CABundle:         "LS0tLS1CRUdJTi1QUklNQVJZLUNBLS0tLS0=",
		AdditionalCAs:    additionalCAs,
		KubeletClientCAs: kubeletClientCAs,
	}

	if len(options.AdditionalCAs) != 3 {
		t.Errorf("Expected 3 additional CAs, got %v", len(options.AdditionalCAs))
	}
	if len(options.KubeletClientCAs) != 2 {
		t.Errorf("Expected 2 kubelet client CAs, got %v", len(options.KubeletClientCAs))
	}

	for i, ca := range options.AdditionalCAs {
		if ca != additionalCAs[i] {
			t.Errorf("AdditionalCAs[%d] = %v, want %v", i, ca, additionalCAs[i])
		}
	}

	for i, ca := range options.KubeletClientCAs {
		if ca != kubeletClientCAs[i] {
			t.Errorf("KubeletClientCAs[%d] = %v, want %v", i, ca, kubeletClientCAs[i])
		}
	}
}

// Helper function to check if a string contains a substring
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr || func() bool {
		for i := 1; i <= len(s)-len(substr); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	}()))
}

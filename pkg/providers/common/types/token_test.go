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
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func TestGetInternalAPIServerEndpoint(t *testing.T) {
	tests := []struct {
		name             string
		k8sObjects       []runtime.Object
		expectedEndpoint string
		expectError      bool
	}{
		{
			name: "endpoint from kubeadm-config",
			k8sObjects: []runtime.Object{
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "kubeadm-config",
						Namespace: "kube-system",
					},
					Data: map[string]string{
						"ClusterConfiguration": `apiVersion: kubeadm.k8s.io/v1beta3
kind: ClusterConfiguration
controlPlaneEndpoint: api.example.com:6443
networking:
  serviceSubnet: 10.96.0.0/12`,
					},
				},
			},
			expectedEndpoint: "https://api.example.com:6443",
		},
		{
			name: "endpoint from cluster-info fallback",
			k8sObjects: []runtime.Object{
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "cluster-info",
						Namespace: "kube-public",
					},
					Data: map[string]string{
						"kubeconfig": `apiVersion: v1
clusters:
- cluster:
    server: https://api.cluster.local:6443
  name: cluster`,
					},
				},
			},
			expectedEndpoint: "https://api.cluster.local:6443",
		},
		{
			name: "invalid kubeadm-config, valid cluster-info",
			k8sObjects: []runtime.Object{
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "kubeadm-config",
						Namespace: "kube-system",
					},
					Data: map[string]string{
						"ClusterConfiguration": "invalid yaml content",
					},
				},
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "cluster-info",
						Namespace: "kube-public",
					},
					Data: map[string]string{
						"kubeconfig": `apiVersion: v1
clusters:
- cluster:
    server: https://fallback.example.com:6443
  name: cluster`,
					},
				},
			},
			expectedEndpoint: "https://fallback.example.com:6443",
		},
		{
			name:        "no config maps found - should error",
			k8sObjects:  []runtime.Object{},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			//nolint:staticcheck // SA1019: NewSimpleClientset is deprecated but NewClientset requires generated apply configurations
			client := fake.NewSimpleClientset(tt.k8sObjects...)

			endpoint, err := GetInternalAPIServerEndpoint(context.Background(), client)

			if tt.expectError {
				assert.Error(t, err)
				assert.Empty(t, endpoint)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedEndpoint, endpoint)
			}
		})
	}
}

func TestParseKubeadmConfigEndpoint(t *testing.T) {
	tests := []struct {
		name             string
		clusterConfig    string
		expectedEndpoint string
		expectError      bool
	}{
		{
			name: "valid kubeadm config",
			clusterConfig: `apiVersion: kubeadm.k8s.io/v1beta3
kind: ClusterConfiguration
controlPlaneEndpoint: api.example.com:6443
networking:
  serviceSubnet: 10.96.0.0/12`,
			expectedEndpoint: "https://api.example.com:6443",
		},
		{
			name: "kubeadm config with spaces",
			clusterConfig: `apiVersion: kubeadm.k8s.io/v1beta3
kind: ClusterConfiguration
controlPlaneEndpoint:   api-with-spaces.com:6443
networking:
  serviceSubnet: 10.96.0.0/12`,
			expectedEndpoint: "https://api-with-spaces.com:6443",
		},
		{
			name: "kubeadm config with IP address",
			clusterConfig: `apiVersion: kubeadm.k8s.io/v1beta3
kind: ClusterConfiguration
controlPlaneEndpoint: 192.168.1.100:6443
networking:
  serviceSubnet: 10.96.0.0/12`,
			expectedEndpoint: "https://192.168.1.100:6443",
		},
		{
			name:          "empty config",
			clusterConfig: "",
			expectError:   true,
		},
		{
			name: "config without controlPlaneEndpoint",
			clusterConfig: `apiVersion: kubeadm.k8s.io/v1beta3
kind: ClusterConfiguration
networking:
  serviceSubnet: 10.96.0.0/12`,
			expectError: true,
		},
		{
			name: "malformed controlPlaneEndpoint line",
			clusterConfig: `apiVersion: kubeadm.k8s.io/v1beta3
kind: ClusterConfiguration
controlPlaneEndpoint: invalid-format
networking:
  serviceSubnet: 10.96.0.0/12`,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			endpoint, err := parseKubeadmConfigEndpoint(tt.clusterConfig)

			if tt.expectError {
				assert.Error(t, err)
				assert.Empty(t, endpoint)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedEndpoint, endpoint)
			}
		})
	}
}

func TestParseClusterInfoEndpoint(t *testing.T) {
	tests := []struct {
		name             string
		kubeconfig       string
		expectedEndpoint string
		expectError      bool
	}{
		{
			name: "valid kubeconfig",
			kubeconfig: `apiVersion: v1
clusters:
- cluster:
    server: https://api.example.com:6443
  name: cluster`,
			expectedEndpoint: "https://api.example.com:6443",
		},
		{
			name: "kubeconfig with spaces",
			kubeconfig: `apiVersion: v1
clusters:
- cluster:
    server:   https://api-spaces.com:6443
  name: cluster`,
			expectedEndpoint: "https://api-spaces.com:6443",
		},
		{
			name: "kubeconfig with IP",
			kubeconfig: `apiVersion: v1
clusters:
- cluster:
    server: https://10.0.0.1:6443
  name: cluster`,
			expectedEndpoint: "https://10.0.0.1:6443",
		},
		{
			name:        "empty kubeconfig",
			kubeconfig:  "",
			expectError: true,
		},
		{
			name: "kubeconfig without server",
			kubeconfig: `apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: LS0tLS1CRUdJTi0=
  name: cluster`,
			expectError: true,
		},
		{
			name: "kubeconfig with empty server",
			kubeconfig: `apiVersion: v1
clusters:
- cluster:
    server:
  name: cluster`,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			endpoint, err := parseClusterInfoEndpoint(tt.kubeconfig)

			if tt.expectError {
				assert.Error(t, err)
				assert.Empty(t, endpoint)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedEndpoint, endpoint)
			}
		})
	}
}

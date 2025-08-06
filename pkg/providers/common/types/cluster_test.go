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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func TestDiscoverClusterConfig(t *testing.T) {
	tests := []struct {
		name             string
		k8sObjects       []runtime.Object
		expectError      bool
		expectedDNSIP    string
		expectedCIDR     string
		expectedCNI      string
		expectedIPFamily string
	}{
		{
			name: "successful cluster discovery with kube-dns and calico",
			k8sObjects: []runtime.Object{
				&corev1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "kube-dns",
						Namespace: "kube-system",
					},
					Spec: corev1.ServiceSpec{
						ClusterIP: "10.96.0.10",
					},
				},
				&corev1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "kubernetes",
						Namespace: "default",
					},
					Spec: corev1.ServiceSpec{
						ClusterIP: "10.96.0.1",
					},
				},
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-node",
					},
					Spec: corev1.NodeSpec{
						PodCIDR: "10.244.0.0/24",
					},
				},
				&appsv1.DaemonSet{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "calico-node",
						Namespace: "kube-system",
					},
				},
			},
			expectedDNSIP:    "10.96.0.10",
			expectedCIDR:     "10.244.0.0/24",
			expectedCNI:      "calico",
			expectedIPFamily: "ipv4",
		},
		{
			name: "coredns instead of kube-dns",
			k8sObjects: []runtime.Object{
				&corev1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "coredns",
						Namespace: "kube-system",
					},
					Spec: corev1.ServiceSpec{
						ClusterIP: "10.96.0.10",
					},
				},
				&corev1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "kubernetes",
						Namespace: "default",
					},
					Spec: corev1.ServiceSpec{
						ClusterIP: "10.96.0.1",
					},
				},
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-node",
					},
					Spec: corev1.NodeSpec{
						PodCIDR: "10.244.0.0/24",
					},
				},
				&appsv1.DaemonSet{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "cilium",
						Namespace: "kube-system",
					},
				},
			},
			expectedDNSIP:    "10.96.0.10",
			expectedCIDR:     "10.244.0.0/24",
			expectedCNI:      "cilium",
			expectedIPFamily: "ipv4",
		},
		{
			name: "IPv6 cluster",
			k8sObjects: []runtime.Object{
				&corev1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "kube-dns",
						Namespace: "kube-system",
					},
					Spec: corev1.ServiceSpec{
						ClusterIP: "fd00::10",
					},
				},
				&corev1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "kubernetes",
						Namespace: "default",
					},
					Spec: corev1.ServiceSpec{
						ClusterIP: "fd00::1",
					},
				},
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-node",
					},
					Spec: corev1.NodeSpec{
						PodCIDR: "fd00:10::/64",
					},
				},
				&appsv1.DaemonSet{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "calico-node",
						Namespace: "kube-system",
					},
				},
			},
			expectedDNSIP:    "fd00::10",
			expectedCIDR:     "fd00:10::/64",
			expectedCNI:      "calico",
			expectedIPFamily: "ipv6",
		},
		{
			name: "no DNS service found",
			k8sObjects: []runtime.Object{
				&corev1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "kubernetes",
						Namespace: "default",
					},
					Spec: corev1.ServiceSpec{
						ClusterIP: "10.96.0.1",
					},
				},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewSimpleClientset(tt.k8sObjects...)

			config, err := DiscoverClusterConfig(context.Background(), client)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, config)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, config)
				assert.Equal(t, tt.expectedDNSIP, config.DNSClusterIP)
				assert.Equal(t, tt.expectedCIDR, config.ClusterCIDR)
				assert.Equal(t, tt.expectedCNI, config.CNIPlugin)
				assert.Equal(t, tt.expectedIPFamily, config.IPFamily)
			}
		})
	}
}

func TestDiscoverDNSClusterIP(t *testing.T) {
	tests := []struct {
		name        string
		services    []corev1.Service
		expectedIP  string
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
						ClusterIP: "10.96.0.10",
					},
				},
			},
			expectedIP: "10.96.0.10",
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
			expectedIP: "10.96.0.10",
		},
		{
			name: "DNS service with label selector",
			services: []corev1.Service{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "dns-service",
						Namespace: "kube-system",
						Labels: map[string]string{
							"k8s-app": "kube-dns",
						},
					},
					Spec: corev1.ServiceSpec{
						ClusterIP: "10.96.0.10",
					},
				},
			},
			expectedIP: "10.96.0.10",
		},
		{
			name:        "no DNS service found",
			services:    []corev1.Service{},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var objects []runtime.Object
			for i := range tt.services {
				objects = append(objects, &tt.services[i])
			}
			client := fake.NewSimpleClientset(objects...)

			ip, err := discoverDNSClusterIP(context.Background(), client)

			if tt.expectError {
				assert.Error(t, err)
				assert.Empty(t, ip)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedIP, ip)
			}
		})
	}
}

func TestDiscoverClusterCIDR(t *testing.T) {
	tests := []struct {
		name         string
		nodes        []corev1.Node
		services     []corev1.Service
		expectedCIDR string
		expectError  bool
	}{
		{
			name: "pod CIDR from node",
			nodes: []corev1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-node",
					},
					Spec: corev1.NodeSpec{
						PodCIDR: "10.244.0.0/16",
					},
				},
			},
			expectedCIDR: "10.244.0.0/16",
		},
		{
			name: "fallback to service CIDR discovery",
			nodes: []corev1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-node",
					},
					Spec: corev1.NodeSpec{
						// No PodCIDR set
					},
				},
			},
			services: []corev1.Service{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "kubernetes",
						Namespace: "default",
					},
					Spec: corev1.ServiceSpec{
						ClusterIP: "10.96.0.1",
					},
				},
			},
			expectedCIDR: "10.96.0.0/12",
		},
		{
			name:        "no nodes found",
			nodes:       []corev1.Node{},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var objects []runtime.Object
			for i := range tt.nodes {
				objects = append(objects, &tt.nodes[i])
			}
			for i := range tt.services {
				objects = append(objects, &tt.services[i])
			}
			client := fake.NewSimpleClientset(objects...)

			cidr, err := discoverClusterCIDR(context.Background(), client)

			if tt.expectError {
				assert.Error(t, err)
				assert.Empty(t, cidr)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedCIDR, cidr)
			}
		})
	}
}

func TestDiscoverServiceCIDR(t *testing.T) {
	tests := []struct {
		name         string
		serviceIP    string
		expectedCIDR string
		expectError  bool
	}{
		{
			name:         "standard IPv4 service CIDR",
			serviceIP:    "10.96.0.1",
			expectedCIDR: "10.96.0.0/12",
		},
		{
			name:         "custom IPv4 service CIDR",
			serviceIP:    "172.20.0.1",
			expectedCIDR: "172.20.0.0/16",
		},
		{
			name:         "IPv6 service CIDR",
			serviceIP:    "fd00::1",
			expectedCIDR: "fd00::/108",
		},
		{
			name:         "fallback IPv4 service CIDR",
			serviceIP:    "192.168.1.1",
			expectedCIDR: "10.96.0.0/12",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kubeService := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "kubernetes",
					Namespace: "default",
				},
				Spec: corev1.ServiceSpec{
					ClusterIP: tt.serviceIP,
				},
			}
			client := fake.NewSimpleClientset(kubeService)

			cidr, err := discoverServiceCIDR(context.Background(), client)

			if tt.expectError {
				assert.Error(t, err)
				assert.Empty(t, cidr)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedCIDR, cidr)
			}
		})
	}
}

func TestDetectCNIPlugin(t *testing.T) {
	tests := []struct {
		name        string
		daemonSets  []appsv1.DaemonSet
		configMaps  []corev1.ConfigMap
		expectedCNI string
	}{
		{
			name: "calico detected",
			daemonSets: []appsv1.DaemonSet{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "calico-node",
						Namespace: "kube-system",
					},
				},
			},
			expectedCNI: "calico",
		},
		{
			name: "cilium detected",
			daemonSets: []appsv1.DaemonSet{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "cilium",
						Namespace: "kube-system",
					},
				},
			},
			expectedCNI: "cilium",
		},
		{
			name: "flannel detected",
			daemonSets: []appsv1.DaemonSet{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "kube-flannel-ds",
						Namespace: "kube-system",
					},
				},
			},
			expectedCNI: "flannel",
		},
		{
			name: "weave detected",
			daemonSets: []appsv1.DaemonSet{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "weave-net",
						Namespace: "kube-system",
					},
				},
			},
			expectedCNI: "weave",
		},
		{
			name: "calico from configmap",
			configMaps: []corev1.ConfigMap{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "cni-config",
						Namespace: "kube-system",
					},
					Data: map[string]string{
						"config.conf": "calico configuration here",
					},
				},
			},
			expectedCNI: "calico",
		},
		{
			name:        "unknown CNI",
			expectedCNI: "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var objects []runtime.Object
			for i := range tt.daemonSets {
				objects = append(objects, &tt.daemonSets[i])
			}
			for i := range tt.configMaps {
				objects = append(objects, &tt.configMaps[i])
			}
			client := fake.NewSimpleClientset(objects...)

			cni, err := detectCNIPlugin(context.Background(), client)

			assert.NoError(t, err)
			assert.Equal(t, tt.expectedCNI, cni)
		})
	}
}

func TestContainsFunction(t *testing.T) {
	tests := []struct {
		name     string
		s        string
		substr   string
		expected bool
	}{
		{
			name:     "exact match",
			s:        "calico",
			substr:   "calico",
			expected: true,
		},
		{
			name:     "substring at beginning",
			s:        "calico-config",
			substr:   "calico",
			expected: true,
		},
		{
			name:     "substring at end",
			s:        "kube-calico",
			substr:   "calico",
			expected: true,
		},
		{
			name:     "substring in middle",
			s:        "kube-calico-node",
			substr:   "calico",
			expected: true,
		},
		{
			name:     "no match",
			s:        "cilium-config",
			substr:   "calico",
			expected: false,
		},
		{
			name:     "empty substring",
			s:        "anything",
			substr:   "",
			expected: true,
		},
		{
			name:     "empty string",
			s:        "",
			substr:   "calico",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := contains(tt.s, tt.substr)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFindSubstring(t *testing.T) {
	tests := []struct {
		name     string
		s        string
		substr   string
		expected bool
	}{
		{
			name:     "substring found",
			s:        "hello world",
			substr:   "world",
			expected: true,
		},
		{
			name:     "substring not found",
			s:        "hello world",
			substr:   "foo",
			expected: false,
		},
		{
			name:     "empty substring",
			s:        "hello",
			substr:   "",
			expected: true,
		},
		{
			name:     "substring longer than string",
			s:        "hi",
			substr:   "hello",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := findSubstring(tt.s, tt.substr)
			assert.Equal(t, tt.expected, result)
		})
	}
}

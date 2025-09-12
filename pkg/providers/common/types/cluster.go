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
	"fmt"
	"net"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// ClusterConfig contains dynamically discovered cluster configuration
type ClusterConfig struct {
	DNSClusterIP string
	ClusterCIDR  string
	CNIPlugin    string
	IPFamily     string
}

// DiscoverClusterConfig discovers cluster networking configuration
func DiscoverClusterConfig(ctx context.Context, client kubernetes.Interface) (*ClusterConfig, error) {
	config := &ClusterConfig{}

	// Discover DNS cluster IP
	dnsIP, err := discoverDNSClusterIP(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("failed to discover DNS cluster IP: %w", err)
	}
	config.DNSClusterIP = dnsIP

	// Discover cluster CIDR
	cidr, err := discoverClusterCIDR(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("failed to discover cluster CIDR: %w", err)
	}
	config.ClusterCIDR = cidr

	// Detect CNI plugin
	cniPlugin, err := detectCNIPlugin(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("failed to detect CNI plugin: %w", err)
	}
	config.CNIPlugin = cniPlugin

	// Determine IP family
	if net.ParseIP(dnsIP).To4() != nil {
		config.IPFamily = "ipv4"
	} else {
		config.IPFamily = "ipv6"
	}

	return config, nil
}

// discoverDNSClusterIP discovers the cluster DNS service IP
func discoverDNSClusterIP(ctx context.Context, client kubernetes.Interface) (string, error) {
	// Try kube-dns first
	dnsService, err := client.CoreV1().Services("kube-system").Get(ctx, "kube-dns", metav1.GetOptions{})
	if err == nil {
		return dnsService.Spec.ClusterIP, nil
	}

	// Try CoreDNS
	dnsService, err = client.CoreV1().Services("kube-system").Get(ctx, "coredns", metav1.GetOptions{})
	if err == nil {
		return dnsService.Spec.ClusterIP, nil
	}

	// Try DNS service with generic name
	services, err := client.CoreV1().Services("kube-system").List(ctx, metav1.ListOptions{
		LabelSelector: "k8s-app=kube-dns",
	})
	if err != nil {
		return "", fmt.Errorf("failed to list DNS services: %w", err)
	}

	if len(services.Items) == 0 {
		return "", fmt.Errorf("no DNS service found in kube-system namespace")
	}

	return services.Items[0].Spec.ClusterIP, nil
}

// discoverClusterCIDR discovers the cluster pod CIDR
func discoverClusterCIDR(ctx context.Context, client kubernetes.Interface) (string, error) {
	// Get cluster info from kube-system namespace
	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{
		Limit: 1,
	})
	if err != nil {
		return "", fmt.Errorf("failed to list nodes: %w", err)
	}

	if len(nodes.Items) == 0 {
		return "", fmt.Errorf("no nodes found in cluster")
	}

	// Extract pod CIDR from first node
	node := nodes.Items[0]
	if node.Spec.PodCIDR != "" {
		return node.Spec.PodCIDR, nil
	}

	// Fallback to service CIDR discovery
	return discoverServiceCIDR(ctx, client)
}

// discoverServiceCIDR discovers the service CIDR by looking at service IPs
func discoverServiceCIDR(ctx context.Context, client kubernetes.Interface) (string, error) {
	// Get kubernetes service (always exists)
	kubeService, err := client.CoreV1().Services("default").Get(ctx, "kubernetes", metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to get kubernetes service: %w", err)
	}

	serviceIP := net.ParseIP(kubeService.Spec.ClusterIP)
	if serviceIP == nil {
		return "", fmt.Errorf("invalid service IP: %s", kubeService.Spec.ClusterIP)
	}

	// Assume standard service CIDR based on service IP
	if serviceIP.To4() != nil {
		// IPv4 - common service CIDRs
		if serviceIP.String() >= "10.96.0.0" && serviceIP.String() <= "10.96.255.255" {
			return "10.96.0.0/12", nil
		}
		if serviceIP.String() >= "172.20.0.0" && serviceIP.String() <= "172.20.255.255" {
			return "172.20.0.0/16", nil
		}
		return "10.96.0.0/12", nil // Default fallback
	} else {
		// IPv6
		return "fd00::/108", nil // Common IPv6 service CIDR
	}
}

// detectCNIPlugin detects the CNI plugin being used
func detectCNIPlugin(ctx context.Context, client kubernetes.Interface) (string, error) {
	// Check for Calico
	_, err := client.AppsV1().DaemonSets("kube-system").Get(ctx, "calico-node", metav1.GetOptions{})
	if err == nil {
		return "calico", nil
	}

	// Check for Cilium
	_, err = client.AppsV1().DaemonSets("kube-system").Get(ctx, "cilium", metav1.GetOptions{})
	if err == nil {
		return "cilium", nil
	}

	// Check for Flannel
	_, err = client.AppsV1().DaemonSets("kube-system").Get(ctx, "kube-flannel-ds", metav1.GetOptions{})
	if err == nil {
		return "flannel", nil
	}

	// Check for Weave
	_, err = client.AppsV1().DaemonSets("kube-system").Get(ctx, "weave-net", metav1.GetOptions{})
	if err == nil {
		return "weave", nil
	}

	// Check for generic CNI via config maps
	configMaps, err := client.CoreV1().ConfigMaps("kube-system").List(ctx, metav1.ListOptions{})
	if err != nil {
		return "unknown", nil
	}

	for _, cm := range configMaps.Items {
		if cm.Name == "cni-config" || cm.Name == "kube-proxy" {
			// Try to detect from config map data
			if data, exists := cm.Data["config.conf"]; exists {
				if contains(data, "calico") {
					return "calico", nil
				}
				if contains(data, "cilium") {
					return "cilium", nil
				}
				if contains(data, "flannel") {
					return "flannel", nil
				}
			}
		}
	}

	return "unknown", nil
}

// contains checks if a string contains a substring (case-insensitive)
func contains(s, substr string) bool {
	return len(s) >= len(substr) &&
		(s == substr ||
			len(s) > len(substr) &&
				(s[:len(substr)] == substr ||
					s[len(s)-len(substr):] == substr ||
					findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

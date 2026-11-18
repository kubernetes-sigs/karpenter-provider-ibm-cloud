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
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// GetInternalAPIServerEndpoint discovers the API server endpoint for node bootstrapping
func GetInternalAPIServerEndpoint(ctx context.Context, client kubernetes.Interface) (string, error) {
	// First try to get from kubeadm-config ConfigMap
	kubeadmConfig, err := client.CoreV1().ConfigMaps("kube-system").Get(ctx, "kubeadm-config", metav1.GetOptions{})
	if err == nil {
		if endpoint, parseErr := parseKubeadmConfigEndpoint(kubeadmConfig.Data["ClusterConfiguration"]); parseErr == nil {
			return endpoint, nil
		}
	}

	// Fallback: Get from cluster-info ConfigMap
	clusterInfo, err := client.CoreV1().ConfigMaps("kube-public").Get(ctx, "cluster-info", metav1.GetOptions{})
	if err == nil {
		if endpoint, parseErr := parseClusterInfoEndpoint(clusterInfo.Data["kubeconfig"]); parseErr == nil {
			return endpoint, nil
		}
	}

	// Final fallback: Use the Discovery client's REST client to get server endpoint
	discoveryClient := client.Discovery()
	if discoveryClient != nil {
		if restClient := discoveryClient.RESTClient(); restClient != nil {
			if baseURL := restClient.Get().URL(); baseURL != nil {
				return baseURL.String(), nil
			}
		}
	}

	return "", fmt.Errorf("unable to determine API server endpoint from kubeadm-config, cluster-info, or client configuration")
}

// parseKubeadmConfigEndpoint extracts the endpoint from kubeadm ClusterConfiguration
func parseKubeadmConfigEndpoint(clusterConfig string) (string, error) {
	if clusterConfig == "" {
		return "", fmt.Errorf("empty ClusterConfiguration")
	}

	// Look for controlPlaneEndpoint in the YAML
	lines := strings.Split(clusterConfig, "\n")
	for _, line := range lines {
		if strings.Contains(line, "controlPlaneEndpoint:") {
			parts := strings.Split(line, ":")
			if len(parts) >= 3 {
				// Extract host:port (parts[1] has the host, parts[2] has the port)
				host := strings.TrimSpace(parts[1])
				port := strings.TrimSpace(parts[2])
				return fmt.Sprintf("https://%s:%s", host, port), nil
			}
		}
	}

	return "", fmt.Errorf("controlPlaneEndpoint not found in kubeadm-config")
}

// parseClusterInfoEndpoint extracts the endpoint from cluster-info kubeconfig
func parseClusterInfoEndpoint(kubeconfig string) (string, error) {
	if kubeconfig == "" {
		return "", fmt.Errorf("empty kubeconfig")
	}

	// Look for server URL in the kubeconfig YAML
	lines := strings.Split(kubeconfig, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "server:") {
			endpoint := strings.TrimSpace(strings.TrimPrefix(line, "server:"))
			if endpoint != "" {
				return endpoint, nil
			}
		}
	}

	return "", fmt.Errorf("server endpoint not found in cluster-info kubeconfig")
}

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
	"encoding/base64"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
)

// BootstrapMode defines how nodes should be bootstrapped
type BootstrapMode string

const (
	// BootstrapModeCloudInit uses cloud-init scripts to bootstrap nodes
	BootstrapModeCloudInit BootstrapMode = "cloud-init"

	// BootstrapModeIKSAPI uses IKS Worker Pool API to add nodes
	BootstrapModeIKSAPI BootstrapMode = "iks-api"

	// BootstrapModeAuto automatically selects the best method
	BootstrapModeAuto BootstrapMode = "auto"
)

// Provider defines the interface for bootstrap providers
type Provider interface {
	// GetUserData generates the user data for node bootstrapping
	GetUserData(ctx context.Context, nodeClass *v1alpha1.IBMNodeClass, nodeClaim types.NamespacedName) (string, error)

	// GetUserDataWithInstanceID generates the user data with a known instance ID
	GetUserDataWithInstanceID(ctx context.Context, nodeClass *v1alpha1.IBMNodeClass, nodeClaim types.NamespacedName, instanceID string) (string, error)
}

// Options contains configuration for bootstrap script generation
type Options struct {
	// ClusterName is the name of the Kubernetes cluster
	ClusterName string

	// NodeName is the name of the node (should match NodeClaim name)
	NodeName string

	// ProviderID is the provider ID for the node
	ProviderID string

	// InstanceID is the VPC instance ID (when known)
	InstanceID string

	// ClusterEndpoint is the API server endpoint
	ClusterEndpoint string

	// CABundle is the base64-encoded cluster CA certificate
	CABundle string

	// AdditionalCAs contains additional CA certificates that kubelet should trust
	// This is a slice of base64-encoded CA certificates
	AdditionalCAs []string

	// KubeletClientCAs contains CA certificates specifically for kubelet client auth
	// This allows separation of concerns between different auth mechanisms
	KubeletClientCAs []string

	// BootstrapToken is the token used for node bootstrapping
	BootstrapToken string

	// KubeletConfig contains kubelet configuration
	KubeletConfig *KubeletConfig

	// ContainerRuntime is the container runtime to use (containerd, cri-o)
	ContainerRuntime string

	// CNIPlugin is the detected CNI plugin (calico, cilium, etc)
	CNIPlugin string

	// CNIVersion is the detected CNI plugin version
	CNIVersion string

	// Architecture is the node architecture (amd64, arm64, etc)
	Architecture string

	// KubernetesVersion is the detected cluster Kubernetes version
	KubernetesVersion string

	// DNSClusterIP is the cluster DNS service IP
	DNSClusterIP string

	// ClusterCIDR is the pod network CIDR
	ClusterCIDR string

	// CustomUserData is any custom user data to merge
	CustomUserData string

	// Region is the IBM Cloud region
	Region string

	// Zone is the availability zone
	Zone string

	// Taints to apply to the node
	Taints []corev1.Taint

	// Labels to apply to the node
	Labels map[string]string
}

// KubeletConfig contains kubelet-specific configuration
type KubeletConfig struct {
	// ClusterDNS is the list of DNS server IPs
	ClusterDNS []string

	// MaxPods is the maximum number of pods per node
	MaxPods *int32

	// ExtraArgs are additional kubelet arguments
	ExtraArgs map[string]string

	// FeatureGates are kubelet feature gates
	FeatureGates map[string]bool
}

// IKSWorkerPoolOptions contains configuration for IKS API-based node provisioning
type IKSWorkerPoolOptions struct {
	// ClusterID is the IKS cluster ID
	ClusterID string

	// WorkerPoolID is the worker pool to add nodes to
	WorkerPoolID string

	// VPCInstanceID is the ID of the VPC instance
	VPCInstanceID string

	// Zone is the zone where the instance is created
	Zone string
}

// ClusterInfo contains information about the target cluster
type ClusterInfo struct {
	// Endpoint is the API server endpoint
	Endpoint string

	// CAData is the cluster CA certificate data
	CAData []byte

	// BootstrapToken is the bootstrap token for joining
	BootstrapToken *corev1.Secret

	// ClusterName is the name of the cluster
	ClusterName string

	// IKSClusterID is the IKS cluster ID (if applicable)
	IKSClusterID string

	// IsIKSManaged indicates if this is an IKS-managed cluster
	IsIKSManaged bool

	// IKSWorkerPoolID is the IKS worker pool ID (if applicable)
	IKSWorkerPoolID string
}

// ParseKubeconfig parses kubeconfig to extract endpoint and CA data
func ParseKubeconfig(kubeconfig string) (string, []byte, error) {
	// This is a simplified parser - in production, you'd want to use a proper YAML parser
	lines := strings.Split(kubeconfig, "\n")
	var endpoint string
	var caData []byte

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "server:") {
			endpoint = strings.TrimSpace(strings.TrimPrefix(line, "server:"))
		}
		if strings.HasPrefix(line, "certificate-authority-data:") {
			caDataStr := strings.TrimSpace(strings.TrimPrefix(line, "certificate-authority-data:"))
			var err error
			caData, err = base64.StdEncoding.DecodeString(caDataStr)
			if err != nil {
				return "", nil, fmt.Errorf("decoding CA data: %w", err)
			}
		}
	}

	if endpoint == "" {
		return "", nil, fmt.Errorf("endpoint not found in kubeconfig")
	}

	return endpoint, caData, nil
}

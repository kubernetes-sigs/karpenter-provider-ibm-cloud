//go:build e2e
// +build e2e

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

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
)

// NodeClassConfig defines the configuration for creating a custom NodeClass
type NodeClassConfig struct {
	// Base configuration (defaults to suite values if not specified)
	Region            *string
	Zone              *string
	VPC               *string
	Subnet            *string
	Image             *string
	ResourceGroup     *string
	SecurityGroups    []string
	SSHKeys           []string
	APIServerEndpoint *string

	// Instance configuration
	InstanceProfile *string
	BootstrapMode   *string

	// UserData for custom bootstrap
	UserData *string

	// Block device mappings
	BlockDeviceMappings []v1alpha1.BlockDeviceMapping

	// Tags for the NodeClass
	Tags map[string]string

	// Metadata for the NodeClass object
	Name   string
	Labels map[string]string
}

// NodePoolConfig defines the configuration for creating a custom NodePool
type NodePoolConfig struct {
	// Base configuration
	Name   string
	Labels map[string]string

	// NodeClass reference (if empty, uses the associated NodeClass from config)
	NodeClassRef *karpv1.NodeClassReference

	// Requirements
	InstanceTypes []string
	Architecture  []string
	CapacityTypes []string
	Zones         []string

	// Custom requirements (appended to the above)
	CustomRequirements []karpv1.NodeSelectorRequirementWithMinValues

	// Resource limits
	CPULimit    *resource.Quantity
	MemoryLimit *resource.Quantity

	// Disruption settings
	ConsolidationPolicy *karpv1.ConsolidationPolicy
	ConsolidateAfter    *karpv1.NillableDuration
	ExpireAfter         *karpv1.NillableDuration

	// Taints
	Taints []corev1.Taint

	// Weights
	Weight *int32
}

// WorkloadConfig defines the configuration for creating a test workload
type WorkloadConfig struct {
	Name      string
	Namespace string
	Replicas  int32

	// Pod template configuration
	Image            string
	CPURequest       string
	MemoryRequest    string
	CPULimit         string
	MemoryLimit      string
	NodeSelector     map[string]string
	Tolerations      []corev1.Toleration
	PodAntiAffinity  bool
	TopologySpread   []corev1.TopologySpreadConstraint
	AdditionalLabels map[string]string
}

// TestConfig combines NodeClass, NodePool, and Workload configurations
type TestConfig struct {
	NodeClass NodeClassConfig
	NodePool  NodePoolConfig
	Workload  WorkloadConfig

	// SkipNodeClassCreation skips creating the NodeClass (assumes it already exists)
	SkipNodeClassCreation bool
}

// LoadTestConfigFromFile loads a test configuration from a JSON file
func LoadTestConfigFromFile(path string) (*TestConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config TestConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	return &config, nil
}

// LoadTestConfigFromEnv loads a test configuration from environment variable (JSON string)
func LoadTestConfigFromEnv(envVar string) (*TestConfig, error) {
	data := os.Getenv(envVar)
	if data == "" {
		return nil, fmt.Errorf("environment variable %s is not set", envVar)
	}

	var config TestConfig
	if err := json.Unmarshal([]byte(data), &config); err != nil {
		return nil, fmt.Errorf("failed to parse config from env: %w", err)
	}

	return &config, nil
}

// CreateNodeClassFromConfig creates a NodeClass using the provided configuration
func (s *E2ETestSuite) CreateNodeClassFromConfig(t *testing.T, config NodeClassConfig) *v1alpha1.IBMNodeClass {
	// Apply defaults from suite if not specified
	region := config.Region
	if region == nil {
		region = &s.testRegion
	}

	zone := config.Zone
	if zone == nil {
		zone = &s.testZone
	}

	vpc := config.VPC
	if vpc == nil {
		vpc = &s.testVPC
	}

	subnet := config.Subnet
	if subnet == nil {
		subnet = &s.testSubnet
	}

	image := config.Image
	if image == nil {
		image = &s.testImage
	}

	resourceGroup := config.ResourceGroup
	if resourceGroup == nil {
		resourceGroup = &s.testResourceGroup
	}

	apiEndpoint := config.APIServerEndpoint
	if apiEndpoint == nil {
		apiEndpoint = &s.APIServerEndpoint
	}

	securityGroups := config.SecurityGroups
	if len(securityGroups) == 0 {
		securityGroups = []string{s.testSecurityGroup}
	}

	sshKeys := config.SSHKeys
	if len(sshKeys) == 0 {
		sshKeys = []string{s.testSshKeyId}
	}

	bootstrapMode := config.BootstrapMode
	if bootstrapMode == nil {
		defaultMode := "cloud-init"
		bootstrapMode = &defaultMode
	}

	// Build tags
	tags := map[string]string{
		"test":       "e2e",
		"created-by": "karpenter-e2e",
		"managed-by": "karpenter-test-framework",
	}
	if config.Name != "" {
		tags["test-name"] = config.Name
	}
	for k, v := range config.Tags {
		tags[k] = v
	}

	// Create NodeClass spec
	spec := v1alpha1.IBMNodeClassSpec{
		Region:            *region,
		Zone:              *zone,
		VPC:               *vpc,
		Subnet:            *subnet,
		Image:             *image,
		ResourceGroup:     *resourceGroup,
		SecurityGroups:    securityGroups,
		SSHKeys:           sshKeys,
		APIServerEndpoint: *apiEndpoint,
		BootstrapMode:     bootstrapMode,
		Tags:              tags,
	}

	if config.InstanceProfile != nil {
		spec.InstanceProfile = *config.InstanceProfile
	}

	if config.UserData != nil {
		spec.UserData = *config.UserData
	}

	if len(config.BlockDeviceMappings) > 0 {
		spec.BlockDeviceMappings = config.BlockDeviceMappings
	}

	// Generate name if not provided
	name := config.Name
	if name == "" {
		name = fmt.Sprintf("e2e-test-%d-nodeclass", time.Now().Unix())
	}

	// Build labels
	labels := make(map[string]string)
	if config.Labels != nil {
		for k, v := range config.Labels {
			labels[k] = v
		}
	}

	nodeClass := &v1alpha1.IBMNodeClass{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "karpenter-ibm.sh/v1alpha1",
			Kind:       "IBMNodeClass",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
		Spec: spec,
	}

	err := s.kubeClient.Create(context.Background(), nodeClass)
	require.NoError(t, err)

	t.Logf("Created NodeClass from config: %s", name)
	return nodeClass
}

// CreateNodePoolFromConfig creates a NodePool using the provided configuration
func (s *E2ETestSuite) CreateNodePoolFromConfig(t *testing.T, config NodePoolConfig, nodeClassName string) *karpv1.NodePool {
	// Generate name if not provided
	name := config.Name
	if name == "" {
		name = fmt.Sprintf("e2e-test-%d-nodepool", time.Now().Unix())
	}

	// Build labels
	labels := map[string]string{
		"test":       "e2e",
		"managed-by": "karpenter-test-framework",
	}
	if config.Labels != nil {
		for k, v := range config.Labels {
			labels[k] = v
		}
	}

	// Build NodeClass reference
	nodeClassRef := config.NodeClassRef
	if nodeClassRef == nil {
		nodeClassRef = &karpv1.NodeClassReference{
			Group: "karpenter-ibm.sh",
			Kind:  "IBMNodeClass",
			Name:  nodeClassName,
		}
	}

	// Build requirements
	requirements := []karpv1.NodeSelectorRequirementWithMinValues{}

	// Instance types
	if len(config.InstanceTypes) > 0 {
		requirements = append(requirements, karpv1.NodeSelectorRequirementWithMinValues{
			NodeSelectorRequirement: corev1.NodeSelectorRequirement{
				Key:      corev1.LabelInstanceTypeStable,
				Operator: corev1.NodeSelectorOpIn,
				Values:   config.InstanceTypes,
			},
		})
	}

	// Architecture
	if len(config.Architecture) > 0 {
		requirements = append(requirements, karpv1.NodeSelectorRequirementWithMinValues{
			NodeSelectorRequirement: corev1.NodeSelectorRequirement{
				Key:      corev1.LabelArchStable,
				Operator: corev1.NodeSelectorOpIn,
				Values:   config.Architecture,
			},
		})
	}

	// Capacity types
	if len(config.CapacityTypes) > 0 {
		requirements = append(requirements, karpv1.NodeSelectorRequirementWithMinValues{
			NodeSelectorRequirement: corev1.NodeSelectorRequirement{
				Key:      karpv1.CapacityTypeLabelKey,
				Operator: corev1.NodeSelectorOpIn,
				Values:   config.CapacityTypes,
			},
		})
	}

	// Zones
	if len(config.Zones) > 0 {
		requirements = append(requirements, karpv1.NodeSelectorRequirementWithMinValues{
			NodeSelectorRequirement: corev1.NodeSelectorRequirement{
				Key:      corev1.LabelTopologyZone,
				Operator: corev1.NodeSelectorOpIn,
				Values:   config.Zones,
			},
		})
	}

	// Add custom requirements
	requirements = append(requirements, config.CustomRequirements...)

	// Build disruption settings
	var expireAfter karpv1.NillableDuration
	if config.ExpireAfter != nil {
		expireAfter = *config.ExpireAfter
	} else {
		expireAfter = karpv1.MustParseNillableDuration("5m")
	}

	// Build template
	template := karpv1.NodeClaimTemplate{
		Spec: karpv1.NodeClaimTemplateSpec{
			NodeClassRef: nodeClassRef,
			Requirements: requirements,
			ExpireAfter:  expireAfter,
		},
	}

	if len(config.Taints) > 0 {
		template.Spec.Taints = config.Taints
	}

	// Build limits
	var limits karpv1.Limits
	if config.CPULimit != nil || config.MemoryLimit != nil {
		resourceList := corev1.ResourceList{}
		if config.CPULimit != nil {
			resourceList[corev1.ResourceCPU] = *config.CPULimit
		}
		if config.MemoryLimit != nil {
			resourceList[corev1.ResourceMemory] = *config.MemoryLimit
		}
		limits = karpv1.Limits(resourceList)
	}

	nodePool := &karpv1.NodePool{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
		Spec: karpv1.NodePoolSpec{
			Template: template,
			Limits:   limits,
		},
	}

	if config.Weight != nil {
		nodePool.Spec.Weight = config.Weight
	}

	if config.ConsolidateAfter != nil {
		nodePool.Spec.Disruption.ConsolidateAfter = *config.ConsolidateAfter
	}

	if config.ConsolidationPolicy != nil {
		nodePool.Spec.Disruption.ConsolidationPolicy = *config.ConsolidationPolicy
	}

	err := s.kubeClient.Create(context.Background(), nodePool)
	require.NoError(t, err)

	t.Logf("Created NodePool from config: %s", name)
	return nodePool
}

// CreateWorkloadFromConfig creates a workload using the provided configuration
func (s *E2ETestSuite) CreateWorkloadFromConfig(t *testing.T, config WorkloadConfig) {
	// Defaults
	if config.Name == "" {
		config.Name = fmt.Sprintf("e2e-test-%d-workload", time.Now().Unix())
	}
	if config.Namespace == "" {
		config.Namespace = "default"
	}
	if config.Image == "" {
		config.Image = "quay.io/nginx/nginx-unprivileged:1.29.1-alpine"
	}
	if config.Replicas == 0 {
		config.Replicas = 3
	}

	// Parse resource requirements
	cpuRequest := resource.MustParse(config.CPURequest)
	if config.CPURequest == "" {
		cpuRequest = resource.MustParse("1000m")
	}
	memoryRequest := resource.MustParse(config.MemoryRequest)
	if config.MemoryRequest == "" {
		memoryRequest = resource.MustParse("1Gi")
	}

	cpuLimit := cpuRequest
	if config.CPULimit != "" {
		cpuLimit = resource.MustParse(config.CPULimit)
	}
	memoryLimit := memoryRequest
	if config.MemoryLimit != "" {
		memoryLimit = resource.MustParse(config.MemoryLimit)
	}

	// Build labels
	labels := map[string]string{
		"app":     config.Name,
		"test":    "e2e",
		"purpose": "karpenter-test",
	}
	for k, v := range config.AdditionalLabels {
		labels[k] = v
	}

	// Build pod affinity
	var affinity *corev1.Affinity
	if config.PodAntiAffinity {
		affinity = &corev1.Affinity{
			PodAntiAffinity: &corev1.PodAntiAffinity{
				PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{
					{
						Weight: 100,
						PodAffinityTerm: corev1.PodAffinityTerm{
							LabelSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"app": config.Name,
								},
							},
							TopologyKey: "kubernetes.io/hostname",
						},
					},
				},
			},
		}
	}

	// Build deployment
	s.createDeployment(t, config.Name, config.Namespace, config.Replicas,
		config.Image, labels, config.NodeSelector, config.Tolerations,
		cpuRequest, memoryRequest, cpuLimit, memoryLimit,
		affinity, config.TopologySpread)
}

// Helper function to create deployment
func (s *E2ETestSuite) createDeployment(t *testing.T, name, namespace string, replicas int32,
	image string, labels, nodeSelector map[string]string, tolerations []corev1.Toleration,
	cpuRequest, memoryRequest, cpuLimit, memoryLimit resource.Quantity,
	affinity *corev1.Affinity, topologySpread []corev1.TopologySpreadConstraint) {

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": name,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					NodeSelector:              nodeSelector,
					Tolerations:               tolerations,
					Affinity:                  affinity,
					TopologySpreadConstraints: topologySpread,
					Containers: []corev1.Container{
						{
							Name:  "test-container",
							Image: image,
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    cpuRequest,
									corev1.ResourceMemory: memoryRequest,
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    cpuLimit,
									corev1.ResourceMemory: memoryLimit,
								},
							},
						},
					},
				},
			},
		},
	}

	err := s.kubeClient.Create(context.Background(), deployment)
	require.NoError(t, err)
	t.Logf("Created workload deployment: %s/%s", namespace, name)
}

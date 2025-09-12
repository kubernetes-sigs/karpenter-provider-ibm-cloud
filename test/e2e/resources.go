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
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
)

// createTestNodeClass creates a standard test NodeClass
func (s *E2ETestSuite) createTestNodeClass(t *testing.T, testName string) *v1alpha1.IBMNodeClass {
	// Dynamically get an available instance type
	instanceType := s.GetAvailableInstanceType(t)

	// Debug: Log exact values being used
	t.Logf("🔍 Creating NodeClass with values from suite:")
	t.Logf("  VPC: '%s' (len=%d)", s.testVPC, len(s.testVPC))
	t.Logf("  ResourceGroup: '%s' (len=%d)", s.testResourceGroup, len(s.testResourceGroup))
	t.Logf("  Subnet: '%s' (len=%d)", s.testSubnet, len(s.testSubnet))
	t.Logf("  Region: '%s' (len=%d)", s.testRegion, len(s.testRegion))
	t.Logf("  Zone: '%s' (len=%d)", s.testZone, len(s.testZone))

	// Helper to create string pointer
	bootstrapMode := "cloud-init"

	nodeClass := &v1alpha1.IBMNodeClass{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "karpenter.ibm.sh/v1alpha1",
			Kind:       "IBMNodeClass",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-nodeclass", testName),
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			Region:            s.testRegion,
			Zone:              s.testZone,
			InstanceProfile:   instanceType,
			Image:             s.testImage,
			VPC:               s.testVPC,
			Subnet:            s.testSubnet,
			SecurityGroups:    []string{s.testSecurityGroup},
			APIServerEndpoint: s.APIServerEndpoint,
			BootstrapMode:     &bootstrapMode,
			ResourceGroup:     s.testResourceGroup,
			SSHKeys:           []string{s.testSshKeyId},
			Tags: map[string]string{
				"test":       "e2e",
				"test-name":  testName,
				"created-by": "karpenter-e2e",
				"purpose":    "e2e-verification",
			},
		},
	}

	err := s.kubeClient.Create(context.Background(), nodeClass)
	require.NoError(t, err)

	return nodeClass
}

// createTestNodeClassWithoutInstanceProfile creates a NodeClass without instanceProfile
func (s *E2ETestSuite) createTestNodeClassWithoutInstanceProfile(t *testing.T, testName string) *v1alpha1.IBMNodeClass {
	// Helper to create string pointer
	bootstrapMode := "cloud-init"

	nodeClass := &v1alpha1.IBMNodeClass{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "karpenter.ibm.sh/v1alpha1",
			Kind:       "IBMNodeClass",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-nodeclass", testName),
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			Region: s.testRegion,
			Zone:   s.testZone,
			// No instanceProfile - let NodePool control instance selection
			Image:             s.testImage,
			VPC:               s.testVPC,
			Subnet:            s.testSubnet,
			SecurityGroups:    []string{s.testSecurityGroup},
			APIServerEndpoint: s.APIServerEndpoint,
			BootstrapMode:     &bootstrapMode,
			ResourceGroup:     s.testResourceGroup,
			SSHKeys:           []string{s.testSshKeyId},
			Tags: map[string]string{
				"test":       "e2e",
				"test-name":  testName,
				"created-by": "karpenter-e2e",
				"purpose":    "nodepool-instancetype-test",
			},
		},
	}

	err := s.kubeClient.Create(context.Background(), nodeClass)
	require.NoError(t, err)

	return nodeClass
}

// createTestNodePool creates a standard test NodePool
func (s *E2ETestSuite) createTestNodePool(t *testing.T, testName, nodeClassName string) *karpv1.NodePool {
	expireAfter := karpv1.MustParseNillableDuration("5m")
	instanceTypes := s.GetMultipleInstanceTypes(t, 3)

	nodePool := &karpv1.NodePool{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-nodepool", testName),
			Labels: map[string]string{
				"test":      "e2e",
				"test-name": testName,
			},
		},
		Spec: karpv1.NodePoolSpec{
			Template: karpv1.NodeClaimTemplate{
				Spec: karpv1.NodeClaimTemplateSpec{
					NodeClassRef: &karpv1.NodeClassReference{
						Group: "karpenter.ibm.sh",
						Kind:  "IBMNodeClass",
						Name:  nodeClassName,
					},
					Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
						{
							NodeSelectorRequirement: corev1.NodeSelectorRequirement{
								Key:      "node.kubernetes.io/instance-type",
								Operator: corev1.NodeSelectorOpIn,
								Values:   instanceTypes,
							},
						},
					},
					ExpireAfter: expireAfter,
				},
			},
		},
	}

	err := s.kubeClient.Create(context.Background(), nodePool)
	require.NoError(t, err)

	return nodePool
}

// createTestNodePoolWithMultipleInstanceTypes creates a NodePool with customer's exact config
func (s *E2ETestSuite) createTestNodePoolWithMultipleInstanceTypes(t *testing.T, testName string, nodeClassName string) *karpv1.NodePool {
	expireAfter := karpv1.MustParseNillableDuration("5m")
	nodePool := &karpv1.NodePool{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-nodepool", testName),
			Labels: map[string]string{
				"test":      "e2e",
				"test-name": testName,
			},
		},
		Spec: karpv1.NodePoolSpec{
			Template: karpv1.NodeClaimTemplate{
				ObjectMeta: karpv1.ObjectMeta{
					Labels: map[string]string{
						"provisioner":  "karpenter-vpc",
						"cluster-type": "self-managed",
						"test":         "e2e",
						"test-name":    testName,
					},
				},
				Spec: karpv1.NodeClaimTemplateSpec{
					NodeClassRef: &karpv1.NodeClassReference{
						Group: "karpenter.ibm.sh",
						Kind:  "IBMNodeClass",
						Name:  nodeClassName,
					},
					Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
						{
							NodeSelectorRequirement: corev1.NodeSelectorRequirement{
								Key:      corev1.LabelInstanceTypeStable,
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"bx2-4x16", "mx2-2x16", "mx2d-2x16", "mx3d-2x20"},
							},
						},
						{
							NodeSelectorRequirement: corev1.NodeSelectorRequirement{
								Key:      corev1.LabelArchStable,
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"amd64"},
							},
						},
						{
							NodeSelectorRequirement: corev1.NodeSelectorRequirement{
								Key:      karpv1.CapacityTypeLabelKey,
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"on-demand"},
							},
						},
					},
					ExpireAfter: expireAfter,
				},
			},
		},
	}

	err := s.kubeClient.Create(context.Background(), nodePool)
	require.NoError(t, err)

	return nodePool
}

// createDriftStabilityNodePool creates a NodePool with requirements for drift testing
func (s *E2ETestSuite) createDriftStabilityNodePool(t *testing.T, testName, nodeClassName string) *karpv1.NodePool {
	expireAfter := karpv1.MustParseNillableDuration("20m") // Longer expiration for stability test
	nodePool := &karpv1.NodePool{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-nodepool", testName),
			Labels: map[string]string{
				"test":      "e2e",
				"test-name": testName,
			},
		},
		Spec: karpv1.NodePoolSpec{
			Template: karpv1.NodeClaimTemplate{
				ObjectMeta: karpv1.ObjectMeta{
					Labels: map[string]string{
						"test":         "drift-stability",
						"test-name":    testName,
						"provisioner":  "karpenter-vpc",
						"cluster-type": "self-managed",
					},
				},
				Spec: karpv1.NodeClaimTemplateSpec{
					NodeClassRef: &karpv1.NodeClassReference{
						Group: "karpenter.ibm.sh",
						Kind:  "IBMNodeClass",
						Name:  nodeClassName,
					},
					Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
						{
							NodeSelectorRequirement: corev1.NodeSelectorRequirement{
								Key:      corev1.LabelInstanceTypeStable,
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"bx2-4x16", "mx2-2x16"},
							},
						},
						{
							NodeSelectorRequirement: corev1.NodeSelectorRequirement{
								Key:      corev1.LabelArchStable,
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"amd64"},
							},
						},
						{
							NodeSelectorRequirement: corev1.NodeSelectorRequirement{
								Key:      karpv1.CapacityTypeLabelKey,
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"on-demand"},
							},
						},
					},
					ExpireAfter: expireAfter,
				},
			},
		},
	}

	err := s.kubeClient.Create(context.Background(), nodePool)
	require.NoError(t, err)

	return nodePool
}

// createTestNodeClaim creates a test NodeClaim
func (s *E2ETestSuite) createTestNodeClaim(t *testing.T, testName, nodeClassName string) *karpv1.NodeClaim {
	expireAfter := karpv1.MustParseNillableDuration("5m")
	nodeClaim := &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-nodeclaim", testName),
			Labels: map[string]string{
				"test":      "e2e",
				"test-name": testName,
			},
		},
		Spec: karpv1.NodeClaimSpec{
			NodeClassRef: &karpv1.NodeClassReference{
				Group: "karpenter.ibm.sh",
				Kind:  "IBMNodeClass",
				Name:  nodeClassName,
			},
			Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
				{
					NodeSelectorRequirement: corev1.NodeSelectorRequirement{
						Key:      "node.kubernetes.io/instance-type",
						Operator: corev1.NodeSelectorOpIn,
						Values:   []string{"bx2-2x8"},
					},
				},
			},
			ExpireAfter: expireAfter,
		},
	}

	err := s.kubeClient.Create(context.Background(), nodeClaim)
	require.NoError(t, err)

	return nodeClaim
}

// createTestWorkload creates a deployment that will trigger Karpenter to provision nodes
func (s *E2ETestSuite) createTestWorkload(t *testing.T, testName string) *appsv1.Deployment {
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-workload", testName),
			Namespace: "default",
			Labels: map[string]string{
				"app":     fmt.Sprintf("%s-workload", testName),
				"test":    "e2e",
				"purpose": "karpenter-test",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &[]int32{3}[0],
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": fmt.Sprintf("%s-workload", testName),
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":     fmt.Sprintf("%s-workload", testName),
						"test":    "e2e",
						"purpose": "karpenter-test",
					},
				},
				Spec: corev1.PodSpec{
					NodeSelector: map[string]string{
						"karpenter.sh/nodepool": fmt.Sprintf("%s-nodepool", testName),
					},
					Containers: []corev1.Container{
						{
							Name:  "test-container",
							Image: "nginx:1.21",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1000m"),
									corev1.ResourceMemory: resource.MustParse("1Gi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("2000m"),
									corev1.ResourceMemory: resource.MustParse("2Gi"),
								},
							},
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: 80,
									Protocol:      corev1.ProtocolTCP,
								},
							},
						},
					},
					Affinity: &corev1.Affinity{
						PodAntiAffinity: &corev1.PodAntiAffinity{
							PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{
								{
									Weight: 100,
									PodAffinityTerm: corev1.PodAffinityTerm{
										LabelSelector: &metav1.LabelSelector{
											MatchLabels: map[string]string{
												"app": fmt.Sprintf("%s-workload", testName),
											},
										},
										TopologyKey: "kubernetes.io/hostname",
									},
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

	return deployment
}

// createTestWorkloadWithInstanceTypeRequirements creates a workload with specific resource requirements
func (s *E2ETestSuite) createTestWorkloadWithInstanceTypeRequirements(t *testing.T, testName string) *appsv1.Deployment {
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-workload", testName),
			Namespace: "default",
			Labels: map[string]string{
				"app":     fmt.Sprintf("%s-workload", testName),
				"test":    "e2e",
				"purpose": "instance-type-test",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &[]int32{2}[0],
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": fmt.Sprintf("%s-workload", testName),
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":     fmt.Sprintf("%s-workload", testName),
						"test":    "e2e",
						"purpose": "instance-type-test",
					},
				},
				Spec: corev1.PodSpec{
					NodeSelector: map[string]string{
						"karpenter.sh/nodepool": fmt.Sprintf("%s-nodepool", testName),
					},
					Containers: []corev1.Container{
						{
							Name:  "test-container",
							Image: "nginx:1.21",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1500m"),
									corev1.ResourceMemory: resource.MustParse("2Gi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("3000m"),
									corev1.ResourceMemory: resource.MustParse("4Gi"),
								},
							},
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: 80,
									Protocol:      corev1.ProtocolTCP,
								},
							},
						},
					},
					Affinity: &corev1.Affinity{
						PodAntiAffinity: &corev1.PodAntiAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{
									LabelSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{
											"app": fmt.Sprintf("%s-workload", testName),
										},
									},
									TopologyKey: "kubernetes.io/hostname",
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

	return deployment
}

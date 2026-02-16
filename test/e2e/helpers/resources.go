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

package helpers

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
)

// CreateSimpleNodeClass creates a basic NodeClass for testing
func CreateSimpleNodeClass(
	t *testing.T,
	ctx context.Context,
	kubeClient client.Client,
	name string,
	vpc, subnet, image, region, zone, resourceGroup, apiEndpoint string,
	securityGroups, sshKeys []string,
	instanceProfile string,
) *v1alpha1.IBMNodeClass {
	t.Helper()

	bootstrapMode := "cloud-init"
	nodeClass := &v1alpha1.IBMNodeClass{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "karpenter-ibm.sh/v1alpha1",
			Kind:       "IBMNodeClass",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"test":      "e2e",
				"test-name": name,
			},
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			Region:            region,
			Zone:              zone,
			VPC:               vpc,
			Subnet:            subnet,
			Image:             image,
			ResourceGroup:     resourceGroup,
			SecurityGroups:    securityGroups,
			SSHKeys:           sshKeys,
			APIServerEndpoint: apiEndpoint,
			BootstrapMode:     &bootstrapMode,
			Tags: map[string]string{
				"test":       "e2e",
				"created-by": "karpenter-e2e",
				"managed-by": "karpenter-test-framework",
				"test-name":  name,
			},
		},
	}

	if instanceProfile != "" {
		nodeClass.Spec.InstanceProfile = instanceProfile
	}

	err := kubeClient.Create(ctx, nodeClass)
	require.NoError(t, err, "Failed to create NodeClass %s", name)
	t.Logf("Created NodeClass: %s", name)

	return nodeClass
}

// CreateSimpleNodePool creates a basic NodePool for testing
func CreateSimpleNodePool(
	t *testing.T,
	ctx context.Context,
	kubeClient client.Client,
	name, nodeClassName string,
	instanceTypes []string,
) *karpv1.NodePool {
	t.Helper()

	expireAfter := karpv1.MustParseNillableDuration("5m")

	requirements := []karpv1.NodeSelectorRequirementWithMinValues{}
	if len(instanceTypes) > 0 {
		requirements = append(requirements, karpv1.NodeSelectorRequirementWithMinValues{
			NodeSelectorRequirement: corev1.NodeSelectorRequirement{
				Key:      corev1.LabelInstanceTypeStable,
				Operator: corev1.NodeSelectorOpIn,
				Values:   instanceTypes,
			},
		})
	}

	// Add capacity type requirement (on-demand by default)
	requirements = append(requirements, karpv1.NodeSelectorRequirementWithMinValues{
		NodeSelectorRequirement: corev1.NodeSelectorRequirement{
			Key:      karpv1.CapacityTypeLabelKey,
			Operator: corev1.NodeSelectorOpIn,
			Values:   []string{"on-demand"},
		},
	})

	nodePool := &karpv1.NodePool{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"test":      "e2e",
				"test-name": name,
			},
		},
		Spec: karpv1.NodePoolSpec{
			Template: karpv1.NodeClaimTemplate{
				ObjectMeta: karpv1.ObjectMeta{
					Labels: map[string]string{
						"provisioner":  "karpenter-vpc",
						"cluster-type": "self-managed",
						"test":         "e2e",
						"test-name":    name,
					},
				},
				Spec: karpv1.NodeClaimTemplateSpec{
					NodeClassRef: &karpv1.NodeClassReference{
						Group: "karpenter-ibm.sh",
						Kind:  "IBMNodeClass",
						Name:  nodeClassName,
					},
					Requirements: requirements,
					ExpireAfter:  expireAfter,
				},
			},
		},
	}

	err := kubeClient.Create(ctx, nodePool)
	require.NoError(t, err, "Failed to create NodePool %s", name)
	t.Logf("Created NodePool: %s", name)

	return nodePool
}

// CreateSimpleDeployment creates a basic Deployment for testing
func CreateSimpleDeployment(
	t *testing.T,
	ctx context.Context,
	kubeClient client.Client,
	name, namespace string,
	replicas int32,
	cpuRequest, memoryRequest string,
) *appsv1.Deployment {
	t.Helper()

	cpu := resource.MustParse(cpuRequest)
	memory := resource.MustParse(memoryRequest)

	labels := map[string]string{
		"app":     name,
		"test":    "e2e",
		"purpose": "karpenter-test",
	}

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
					Containers: []corev1.Container{
						{
							Name:  "test-container",
							Image: "quay.io/nginx/nginx-unprivileged:1.29.1-alpine",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    cpu,
									corev1.ResourceMemory: memory,
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    cpu,
									corev1.ResourceMemory: memory,
								},
							},
						},
					},
				},
			},
		},
	}

	err := kubeClient.Create(ctx, deployment)
	require.NoError(t, err, "Failed to create Deployment %s/%s", namespace, name)
	t.Logf("Created Deployment: %s/%s", namespace, name)

	return deployment
}

// DeleteResource safely deletes a Kubernetes resource
func DeleteResource(t *testing.T, ctx context.Context, kubeClient client.Client, obj client.Object) {
	t.Helper()

	err := kubeClient.Delete(ctx, obj)
	if client.IgnoreNotFound(err) != nil {
		t.Logf("Warning: Failed to delete %s %s: %v", obj.GetObjectKind().GroupVersionKind().Kind, obj.GetName(), err)
	} else {
		t.Logf("Deleted %s: %s", obj.GetObjectKind().GroupVersionKind().Kind, obj.GetName())
	}
}

// WaitForDeletion waits for a resource to be deleted
func WaitForDeletion(t *testing.T, ctx context.Context, kubeClient client.Client, obj client.Object, timeout time.Duration) {
	t.Helper()

	if timeout == 0 {
		timeout = 2 * time.Minute
	}

	require.Eventually(t, func() bool {
		err := kubeClient.Get(ctx, client.ObjectKeyFromObject(obj), obj)
		if err != nil && client.IgnoreNotFound(err) == nil {
			// Resource not found - deletion complete
			return true
		}
		return false
	}, timeout, 5*time.Second, "Resource %s %s was not deleted within %s", obj.GetObjectKind().GroupVersionKind().Kind, obj.GetName(), timeout)

	t.Logf("Resource %s %s successfully deleted", obj.GetObjectKind().GroupVersionKind().Kind, obj.GetName())
}

// GenerateTestName creates a unique test name with timestamp
func GenerateTestName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().Unix())
}

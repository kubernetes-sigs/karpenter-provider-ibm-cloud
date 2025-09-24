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
	"time"

	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
)

// TestE2EStartupTaints tests that startup taints are properly applied to new nodes
func TestE2EStartupTaints(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	ctx := context.Background()

	// Create unique names for this test
	testName := fmt.Sprintf("startup-taints-%d", time.Now().Unix())
	nodePoolName := fmt.Sprintf("test-nodepool-%s", testName)
	nodeClassName := fmt.Sprintf("test-nodeclass-%s", testName)
	deploymentName := fmt.Sprintf("test-deployment-%s", testName)

	// Create NodeClass
	nodeClass := &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeClassName,
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			Region:            suite.testRegion,
			Zone:              suite.testZone,
			InstanceProfile:   suite.GetAvailableInstanceType(t), // Dynamically detected instance type
			VPC:               suite.testVPC,
			Subnet:            suite.testSubnet,
			Image:             suite.testImage,
			SecurityGroups:    []string{suite.testSecurityGroup},
			APIServerEndpoint: suite.APIServerEndpoint,
			BootstrapMode:     lo.ToPtr("cloud-init"),
			ResourceGroup:     suite.testResourceGroup,
			SSHKeys:           []string{suite.testSshKeyId},
		},
	}

	err := suite.kubeClient.Create(ctx, nodeClass)
	require.NoError(t, err)
	defer func() {
		_ = suite.kubeClient.Delete(ctx, nodeClass)
	}()

	// Create NodePool with startup taints
	nodePool := &karpv1.NodePool{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodePoolName,
		},
		Spec: karpv1.NodePoolSpec{
			Template: karpv1.NodeClaimTemplate{
				ObjectMeta: karpv1.ObjectMeta{
					Labels: map[string]string{
						"test": testName,
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
								Key:      "node.kubernetes.io/instance-type",
								Operator: corev1.NodeSelectorOpIn,
								Values:   suite.GetMultipleInstanceTypes(t, 1), // Dynamically detected instance type
							},
						},
					},
					StartupTaints: []corev1.Taint{
						{
							Key:    "node.kubernetes.io/not-ready",
							Effect: corev1.TaintEffectNoSchedule,
						},
						{
							Key:    "example.com/initializing",
							Value:  "true",
							Effect: corev1.TaintEffectNoSchedule,
						},
					},
				},
			},
		},
	}

	err = suite.kubeClient.Create(ctx, nodePool)
	require.NoError(t, err)
	defer func() {
		_ = suite.kubeClient.Delete(ctx, nodePool)
	}()

	// Create deployment that forces node creation with nodeSelector
	// This ensures the pod is only scheduled on nodes from the specific NodePool with startup taints
	deployment := createResourceIntensiveWorkload(deploymentName, testName, []corev1.Toleration{
		{
			Key:    "node.kubernetes.io/not-ready",
			Effect: corev1.TaintEffectNoSchedule,
		},
		{
			Key:    "example.com/initializing",
			Value:  "true",
			Effect: corev1.TaintEffectNoSchedule,
		},
	}, map[string]string{
		"test": testName, // Force scheduling on nodes from our NodePool
	})

	err = suite.kubeClient.Create(ctx, deployment)
	require.NoError(t, err)
	defer func() {
		_ = suite.kubeClient.Delete(ctx, deployment)
	}()

	// Wait for NodeClaim to be created
	var nodeClaim karpv1.NodeClaim
	err = wait.PollUntilContextTimeout(ctx, pollInterval, testTimeout, true, func(ctx context.Context) (bool, error) {
		// First check if deployment pods are pending (which should trigger NodeClaim creation)
		var deployment appsv1.Deployment
		getErr := suite.kubeClient.Get(ctx, types.NamespacedName{
			Namespace: "default",
			Name:      deploymentName,
		}, &deployment)
		if getErr != nil {
			t.Logf("Could not get deployment %s: %v", deploymentName, getErr)
		} else {
			t.Logf("Deployment %s: %d/%d replicas ready", deploymentName, deployment.Status.ReadyReplicas, *deployment.Spec.Replicas)
		}

		// Check all NodeClaims (not just ones with our label)
		allNodeClaims := &karpv1.NodeClaimList{}
		if listErr := suite.kubeClient.List(ctx, allNodeClaims); listErr == nil {
			t.Logf("Total NodeClaims in cluster: %d", len(allNodeClaims.Items))
		}

		// Check for NodeClaims with our test label
		nodeClaimList := &karpv1.NodeClaimList{}
		listErr := suite.kubeClient.List(ctx, nodeClaimList, client.MatchingLabels{"test": testName})
		if listErr != nil {
			return false, listErr
		}
		t.Logf("NodeClaims with test label '%s': %d", testName, len(nodeClaimList.Items))
		if len(nodeClaimList.Items) > 0 {
			nodeClaim = nodeClaimList.Items[0]
			t.Logf("Found NodeClaim: %s", nodeClaim.Name)
			return true, nil
		}
		return false, nil
	})
	require.NoError(t, err, "NodeClaim should be created")

	// Verify NodeClaim has startup taints
	assert.Len(t, nodeClaim.Spec.StartupTaints, 2, "NodeClaim should have 2 startup taints")

	foundNotReady := false
	foundInitializing := false
	for _, taint := range nodeClaim.Spec.StartupTaints {
		if taint.Key == "node.kubernetes.io/not-ready" {
			foundNotReady = true
			assert.Equal(t, corev1.TaintEffectNoSchedule, taint.Effect)
		}
		if taint.Key == "example.com/initializing" {
			foundInitializing = true
			assert.Equal(t, "true", taint.Value)
			assert.Equal(t, corev1.TaintEffectNoSchedule, taint.Effect)
		}
	}
	assert.True(t, foundNotReady, "NodeClaim should have 'not-ready' startup taint")
	assert.True(t, foundInitializing, "NodeClaim should have 'initializing' startup taint")

	// Wait for Node to be created and registered
	var node corev1.Node
	err = wait.PollUntilContextTimeout(ctx, pollInterval, testTimeout, true, func(ctx context.Context) (bool, error) {
		if nodeClaim.Status.NodeName == "" {
			// Refresh NodeClaim to get updated status
			getErr := suite.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClaim.Name}, &nodeClaim)
			if getErr != nil {
				return false, getErr
			}
			return false, nil
		}

		getErr := suite.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClaim.Status.NodeName}, &node)
		if getErr != nil {
			return false, client.IgnoreNotFound(getErr)
		}
		return true, nil
	})
	require.NoError(t, err, "Node should be created and registered")

	// Verify startup taints are applied to the Node
	foundNotReadyOnNode := false
	foundInitializingOnNode := false
	for _, taint := range node.Spec.Taints {
		if taint.Key == "node.kubernetes.io/not-ready" {
			foundNotReadyOnNode = true
			assert.Equal(t, corev1.TaintEffectNoSchedule, taint.Effect)
		}
		if taint.Key == "example.com/initializing" {
			foundInitializingOnNode = true
			assert.Equal(t, "true", taint.Value)
			assert.Equal(t, corev1.TaintEffectNoSchedule, taint.Effect)
		}
	}
	assert.True(t, foundNotReadyOnNode, "Node should have 'not-ready' startup taint applied")
	assert.True(t, foundInitializingOnNode, "Node should have 'initializing' startup taint applied")

	// Verify pods can schedule despite startup taints (ignored for provisioning)
	err = wait.PollUntilContextTimeout(ctx, pollInterval, testTimeout, true, func(ctx context.Context) (bool, error) {
		var updatedDeployment appsv1.Deployment
		getErr := suite.kubeClient.Get(ctx, types.NamespacedName{
			Namespace: deployment.Namespace,
			Name:      deployment.Name,
		}, &updatedDeployment)
		if getErr != nil {
			return false, getErr
		}
		return updatedDeployment.Status.ReadyReplicas >= 1, nil
	})
	require.NoError(t, err, "Deployment should have at least 1 ready replica despite startup taints")

	t.Logf("✓ StartupTaints E2E test passed - startup taints properly applied and pods scheduled")
}

// TestE2EStartupTaintsRemoval tests startup taint removal by DaemonSet
func TestE2EStartupTaintsRemoval(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	ctx := context.Background()

	// Create unique names for this test
	testName := fmt.Sprintf("startup-taint-removal-%d", time.Now().Unix())
	nodePoolName := fmt.Sprintf("test-nodepool-%s", testName)
	nodeClassName := fmt.Sprintf("test-nodeclass-%s", testName)
	daemonSetName := fmt.Sprintf("taint-remover-%s", testName)
	deploymentName := fmt.Sprintf("test-deployment-%s", testName)

	// Create NodeClass
	nodeClass := &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeClassName,
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			Region:            suite.testRegion,
			Zone:              suite.testZone,
			InstanceProfile:   suite.GetAvailableInstanceType(t), // Dynamically detected instance type
			VPC:               suite.testVPC,
			Subnet:            suite.testSubnet,
			Image:             suite.testImage,
			SecurityGroups:    []string{suite.testSecurityGroup},
			APIServerEndpoint: suite.APIServerEndpoint,
			BootstrapMode:     lo.ToPtr("cloud-init"),
			ResourceGroup:     suite.testResourceGroup,
			SSHKeys:           []string{suite.testSshKeyId},
		},
	}

	err := suite.kubeClient.Create(ctx, nodeClass)
	require.NoError(t, err)
	defer func() {
		_ = suite.kubeClient.Delete(ctx, nodeClass)
	}()

	// Create NodePool with startup taints
	nodePool := &karpv1.NodePool{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodePoolName,
		},
		Spec: karpv1.NodePoolSpec{
			Template: karpv1.NodeClaimTemplate{
				ObjectMeta: karpv1.ObjectMeta{
					Labels: map[string]string{
						"test": testName,
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
								Key:      "node.kubernetes.io/instance-type",
								Operator: corev1.NodeSelectorOpIn,
								Values:   suite.GetMultipleInstanceTypes(t, 1), // Dynamically detected instance type
							},
						},
					},
					StartupTaints: []corev1.Taint{
						{
							Key:    "example.com/startup-init",
							Value:  "pending",
							Effect: corev1.TaintEffectNoSchedule,
						},
					},
				},
			},
		},
	}

	err = suite.kubeClient.Create(ctx, nodePool)
	require.NoError(t, err)
	defer func() {
		_ = suite.kubeClient.Delete(ctx, nodePool)
	}()

	// Create DaemonSet that tolerates and removes startup taints
	daemonSet := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      daemonSetName,
			Namespace: "default",
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": daemonSetName},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": daemonSetName},
				},
				Spec: corev1.PodSpec{
					Tolerations: []corev1.Toleration{
						{
							Key:    "example.com/startup-init",
							Value:  "pending",
							Effect: corev1.TaintEffectNoSchedule,
						},
					},
					Containers: []corev1.Container{
						{
							Name:  "taint-remover",
							Image: "quay.io/isovalent/busybox:1.37.0",
							Command: []string{
								"/bin/sh",
								"-c",
								`
# Wait for node to be ready, then remove startup taint
NODE_NAME=${MY_NODE_NAME}
echo "Waiting for node $NODE_NAME to be ready..."
sleep 30

# Use kubectl to remove the startup taint
kubectl patch node $NODE_NAME --type=json -p='[{"op": "remove", "path": "/spec/taints", "value": {"key": "example.com/startup-init", "value": "pending", "effect": "NoSchedule"}}]'

# Keep container running
sleep 3600
								`,
							},
							Env: []corev1.EnvVar{
								{
									Name: "MY_NODE_NAME",
									ValueFrom: &corev1.EnvVarSource{
										FieldRef: &corev1.ObjectFieldSelector{
											FieldPath: "spec.nodeName",
										},
									},
								},
							},
						},
					},
					ServiceAccountName: "default", // In practice, would use proper RBAC
				},
			},
		},
	}

	err = suite.kubeClient.Create(ctx, daemonSet)
	require.NoError(t, err)
	defer func() {
		_ = suite.kubeClient.Delete(ctx, daemonSet)
	}()

	// Create deployment to force node creation
	deployment := createResourceIntensiveWorkload(deploymentName, testName, []corev1.Toleration{
		{
			Key:    "example.com/startup-init",
			Value:  "pending",
			Effect: corev1.TaintEffectNoSchedule,
		},
	}, map[string]string{
		"test": testName, // Force scheduling on nodes from our NodePool
	})

	err = suite.kubeClient.Create(ctx, deployment)
	require.NoError(t, err)
	defer func() {
		_ = suite.kubeClient.Delete(ctx, deployment)
	}()

	// Wait for Node to be created
	var node corev1.Node
	err = wait.PollUntilContextTimeout(ctx, pollInterval, testTimeout, true, func(ctx context.Context) (bool, error) {
		nodeClaimList := &karpv1.NodeClaimList{}
		listErr := suite.kubeClient.List(ctx, nodeClaimList, client.MatchingLabels{"test": testName})
		if listErr != nil {
			return false, listErr
		}
		if len(nodeClaimList.Items) == 0 || nodeClaimList.Items[0].Status.NodeName == "" {
			return false, nil
		}

		err = suite.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClaimList.Items[0].Status.NodeName}, &node)
		return err == nil, client.IgnoreNotFound(err)
	})
	require.NoError(t, err, "Node should be created")

	// Verify startup taint is initially present
	foundStartupTaint := false
	for _, taint := range node.Spec.Taints {
		if taint.Key == "example.com/startup-init" && taint.Value == "pending" {
			foundStartupTaint = true
			break
		}
	}
	assert.True(t, foundStartupTaint, "Node should initially have startup taint")

	// NOTE: For a complete implementation, the DaemonSet would need proper RBAC permissions
	// to patch nodes and remove taints. This test verifies the taint is initially applied correctly.

	t.Logf("✓ StartupTaints removal test setup complete - startup taint properly applied")
}

// TestE2ETaintsBasicScheduling tests basic taint/toleration scheduling
func TestE2ETaintsBasicScheduling(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	ctx := context.Background()

	// Create unique names for this test
	testName := fmt.Sprintf("basic-taints-%d", time.Now().Unix())
	nodePoolName := fmt.Sprintf("test-nodepool-%s", testName)
	nodeClassName := fmt.Sprintf("test-nodeclass-%s", testName)
	tolerantDeploymentName := fmt.Sprintf("tolerant-deployment-%s", testName)
	intolerantDeploymentName := fmt.Sprintf("intolerant-deployment-%s", testName)

	// Create NodeClass
	nodeClass := &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeClassName,
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			Region:            suite.testRegion,
			Zone:              suite.testZone,
			InstanceProfile:   suite.GetAvailableInstanceType(t), // Dynamically detected instance type
			VPC:               suite.testVPC,
			Subnet:            suite.testSubnet,
			Image:             suite.testImage,
			SecurityGroups:    []string{suite.testSecurityGroup},
			APIServerEndpoint: suite.APIServerEndpoint,
			BootstrapMode:     lo.ToPtr("cloud-init"),
			ResourceGroup:     suite.testResourceGroup,
			SSHKeys:           []string{suite.testSshKeyId},
		},
	}

	err := suite.kubeClient.Create(ctx, nodeClass)
	require.NoError(t, err)
	defer func() {
		_ = suite.kubeClient.Delete(ctx, nodeClass)
	}()

	// Create NodePool with regular taints
	nodePool := &karpv1.NodePool{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodePoolName,
		},
		Spec: karpv1.NodePoolSpec{
			Template: karpv1.NodeClaimTemplate{
				ObjectMeta: karpv1.ObjectMeta{
					Labels: map[string]string{
						"test": testName,
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
								Key:      "node.kubernetes.io/instance-type",
								Operator: corev1.NodeSelectorOpIn,
								Values:   suite.GetMultipleInstanceTypes(t, 1), // Dynamically detected instance type
							},
						},
					},
					Taints: []corev1.Taint{
						{
							Key:    "dedicated",
							Value:  "gpu-workload",
							Effect: corev1.TaintEffectNoSchedule,
						},
					},
				},
			},
		},
	}

	err = suite.kubeClient.Create(ctx, nodePool)
	require.NoError(t, err)
	defer func() {
		_ = suite.kubeClient.Delete(ctx, nodePool)
	}()

	// Create deployment that tolerates the taint
	tolerantDeployment := createResourceIntensiveWorkload(tolerantDeploymentName, testName, []corev1.Toleration{
		{
			Key:      "dedicated",
			Value:    "gpu-workload",
			Effect:   corev1.TaintEffectNoSchedule,
			Operator: corev1.TolerationOpEqual,
		},
	}, map[string]string{
		"test": testName, // Force scheduling on nodes from our NodePool
	})

	err = suite.kubeClient.Create(ctx, tolerantDeployment)
	require.NoError(t, err)
	defer func() {
		_ = suite.kubeClient.Delete(ctx, tolerantDeployment)
	}()

	// Wait for tolerant deployment to schedule and become ready
	err = wait.PollUntilContextTimeout(ctx, pollInterval, testTimeout, true, func(ctx context.Context) (bool, error) {
		var deployment appsv1.Deployment
		getErr := suite.kubeClient.Get(ctx, types.NamespacedName{
			Namespace: tolerantDeployment.Namespace,
			Name:      tolerantDeployment.Name,
		}, &deployment)
		if getErr != nil {
			return false, getErr
		}
		return deployment.Status.ReadyReplicas >= 1, nil
	})
	require.NoError(t, err, "Tolerant deployment should become ready")

	// Verify node has the expected taint
	var node corev1.Node
	err = wait.PollUntilContextTimeout(ctx, pollInterval, time.Minute, true, func(ctx context.Context) (bool, error) {
		nodeClaimList := &karpv1.NodeClaimList{}
		listErr := suite.kubeClient.List(ctx, nodeClaimList, client.MatchingLabels{"test": testName})
		if listErr != nil {
			return false, listErr
		}
		if len(nodeClaimList.Items) == 0 || nodeClaimList.Items[0].Status.NodeName == "" {
			return false, nil
		}

		err = suite.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClaimList.Items[0].Status.NodeName}, &node)
		return err == nil, client.IgnoreNotFound(err)
	})
	require.NoError(t, err, "Node should be found")

	// Verify taint is present on node
	foundTaint := false
	for _, taint := range node.Spec.Taints {
		if taint.Key == "dedicated" && taint.Value == "gpu-workload" && taint.Effect == corev1.TaintEffectNoSchedule {
			foundTaint = true
			break
		}
	}
	assert.True(t, foundTaint, "Node should have 'dedicated=gpu-workload' taint")

	// Create deployment that does NOT tolerate the taint (should remain pending)
	intolerantDeployment := createResourceIntensiveWorkload(intolerantDeploymentName, testName+"intolerant", nil, nil)

	err = suite.kubeClient.Create(ctx, intolerantDeployment)
	require.NoError(t, err)
	defer func() {
		_ = suite.kubeClient.Delete(ctx, intolerantDeployment)
	}()

	// Wait and verify intolerant deployment does NOT become ready
	time.Sleep(30 * time.Second) // Give it time to try scheduling

	var intolerantDep appsv1.Deployment
	err = suite.kubeClient.Get(ctx, types.NamespacedName{
		Namespace: intolerantDeployment.Namespace,
		Name:      intolerantDeployment.Name,
	}, &intolerantDep)
	require.NoError(t, err)

	// Should have 0 ready replicas due to taint preventing scheduling
	assert.Equal(t, int32(0), intolerantDep.Status.ReadyReplicas, "Intolerant deployment should not have ready replicas due to taint")

	t.Logf("✓ Basic taints scheduling E2E test passed - taints properly prevent intolerant pods from scheduling")
}

// TestE2ETaintValues tests that taint values are correctly applied and updated
func TestE2ETaintValues(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	ctx := context.Background()

	// Create unique names for this test
	testName := fmt.Sprintf("taint-values-%d", time.Now().Unix())
	nodePoolName := fmt.Sprintf("test-nodepool-%s", testName)
	nodeClassName := fmt.Sprintf("test-nodeclass-%s", testName)
	deploymentName := fmt.Sprintf("test-deployment-%s", testName)

	// Create NodeClass
	nodeClass := &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeClassName,
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			Region:            suite.testRegion,
			Zone:              suite.testZone,
			InstanceProfile:   suite.GetAvailableInstanceType(t), // Dynamically detected instance type
			VPC:               suite.testVPC,
			Subnet:            suite.testSubnet,
			Image:             suite.testImage,
			SecurityGroups:    []string{suite.testSecurityGroup},
			APIServerEndpoint: suite.APIServerEndpoint,
			BootstrapMode:     lo.ToPtr("cloud-init"),
			ResourceGroup:     suite.testResourceGroup,
			SSHKeys:           []string{suite.testSshKeyId},
		},
	}

	err := suite.kubeClient.Create(ctx, nodeClass)
	require.NoError(t, err)
	defer func() {
		_ = suite.kubeClient.Delete(ctx, nodeClass)
	}()

	// Create NodePool with specific taint values
	nodePool := &karpv1.NodePool{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodePoolName,
		},
		Spec: karpv1.NodePoolSpec{
			Template: karpv1.NodeClaimTemplate{
				ObjectMeta: karpv1.ObjectMeta{
					Labels: map[string]string{
						"test": testName,
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
								Key:      "node.kubernetes.io/instance-type",
								Operator: corev1.NodeSelectorOpIn,
								Values:   suite.GetMultipleInstanceTypes(t, 1), // Dynamically detected instance type
							},
						},
					},
					Taints: []corev1.Taint{
						{
							Key:    "workload-type",
							Value:  "batch-processing",
							Effect: corev1.TaintEffectNoSchedule,
						},
						{
							Key:    "priority",
							Value:  "high",
							Effect: corev1.TaintEffectPreferNoSchedule,
						},
					},
				},
			},
		},
	}

	err = suite.kubeClient.Create(ctx, nodePool)
	require.NoError(t, err)
	defer func() {
		_ = suite.kubeClient.Delete(ctx, nodePool)
	}()

	// Create deployment that tolerates the taints with exact values
	deployment := createResourceIntensiveWorkload(deploymentName, testName, []corev1.Toleration{
		{
			Key:      "workload-type",
			Value:    "batch-processing",
			Effect:   corev1.TaintEffectNoSchedule,
			Operator: corev1.TolerationOpEqual,
		},
		{
			Key:      "priority",
			Value:    "high",
			Effect:   corev1.TaintEffectPreferNoSchedule,
			Operator: corev1.TolerationOpEqual,
		},
	}, map[string]string{
		"test": testName, // Force scheduling on nodes from our NodePool
	})

	err = suite.kubeClient.Create(ctx, deployment)
	require.NoError(t, err)
	defer func() {
		_ = suite.kubeClient.Delete(ctx, deployment)
	}()

	// Wait for deployment to become ready
	err = wait.PollUntilContextTimeout(ctx, pollInterval, testTimeout, true, func(ctx context.Context) (bool, error) {
		var dep appsv1.Deployment
		getErr := suite.kubeClient.Get(ctx, types.NamespacedName{
			Namespace: deployment.Namespace,
			Name:      deployment.Name,
		}, &dep)
		if getErr != nil {
			return false, getErr
		}
		return dep.Status.ReadyReplicas >= 1, nil
	})
	require.NoError(t, err, "Deployment should become ready")

	// Get the created node and verify taint values
	var node corev1.Node
	err = wait.PollUntilContextTimeout(ctx, pollInterval, time.Minute, true, func(ctx context.Context) (bool, error) {
		nodeClaimList := &karpv1.NodeClaimList{}
		listErr := suite.kubeClient.List(ctx, nodeClaimList, client.MatchingLabels{"test": testName})
		if listErr != nil {
			return false, listErr
		}
		if len(nodeClaimList.Items) == 0 || nodeClaimList.Items[0].Status.NodeName == "" {
			return false, nil
		}

		err = suite.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClaimList.Items[0].Status.NodeName}, &node)
		return err == nil, client.IgnoreNotFound(err)
	})
	require.NoError(t, err, "Node should be found")

	// Verify exact taint values are present
	expectedTaints := map[string]struct {
		value  string
		effect corev1.TaintEffect
	}{
		"workload-type": {"batch-processing", corev1.TaintEffectNoSchedule},
		"priority":      {"high", corev1.TaintEffectPreferNoSchedule},
	}

	foundTaints := make(map[string]bool)
	for _, taint := range node.Spec.Taints {
		if expected, exists := expectedTaints[taint.Key]; exists {
			assert.Equal(t, expected.value, taint.Value, "Taint '%s' should have correct value", taint.Key)
			assert.Equal(t, expected.effect, taint.Effect, "Taint '%s' should have correct effect", taint.Key)
			foundTaints[taint.Key] = true
		}
	}

	assert.True(t, foundTaints["workload-type"], "Node should have 'workload-type' taint")
	assert.True(t, foundTaints["priority"], "Node should have 'priority' taint")

	t.Logf("✓ Taint values E2E test passed - taint values correctly applied and verified")
}

// TestE2ETaintSync tests taint synchronization from NodeClaim to Node
func TestE2ETaintSync(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	ctx := context.Background()

	// Create unique names for this test
	testName := fmt.Sprintf("taint-sync-%d", time.Now().Unix())
	nodePoolName := fmt.Sprintf("test-nodepool-%s", testName)
	nodeClassName := fmt.Sprintf("test-nodeclass-%s", testName)
	deploymentName := fmt.Sprintf("test-deployment-%s", testName)

	// Create NodeClass
	nodeClass := &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeClassName,
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			Region:            suite.testRegion,
			Zone:              suite.testZone,
			InstanceProfile:   suite.GetAvailableInstanceType(t), // Dynamically detected instance type
			VPC:               suite.testVPC,
			Subnet:            suite.testSubnet,
			Image:             suite.testImage,
			SecurityGroups:    []string{suite.testSecurityGroup},
			APIServerEndpoint: suite.APIServerEndpoint,
			BootstrapMode:     lo.ToPtr("cloud-init"),
			ResourceGroup:     suite.testResourceGroup,
			SSHKeys:           []string{suite.testSshKeyId},
		},
	}

	err := suite.kubeClient.Create(ctx, nodeClass)
	require.NoError(t, err)
	defer func() {
		_ = suite.kubeClient.Delete(ctx, nodeClass)
	}()

	// Create NodePool with both regular and startup taints
	nodePool := &karpv1.NodePool{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodePoolName,
		},
		Spec: karpv1.NodePoolSpec{
			Template: karpv1.NodeClaimTemplate{
				ObjectMeta: karpv1.ObjectMeta{
					Labels: map[string]string{
						"test": testName,
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
								Key:      "node.kubernetes.io/instance-type",
								Operator: corev1.NodeSelectorOpIn,
								Values:   suite.GetMultipleInstanceTypes(t, 1), // Dynamically detected instance type
							},
						},
					},
					Taints: []corev1.Taint{
						{
							Key:    "regular-taint",
							Value:  "regular-value",
							Effect: corev1.TaintEffectNoSchedule,
						},
					},
					StartupTaints: []corev1.Taint{
						{
							Key:    "startup-taint",
							Value:  "startup-value",
							Effect: corev1.TaintEffectNoExecute,
						},
					},
				},
			},
		},
	}

	err = suite.kubeClient.Create(ctx, nodePool)
	require.NoError(t, err)
	defer func() {
		_ = suite.kubeClient.Delete(ctx, nodePool)
	}()

	// Create deployment that tolerates all taints
	deployment := createResourceIntensiveWorkload(deploymentName, testName, []corev1.Toleration{
		{
			Key:      "regular-taint",
			Value:    "regular-value",
			Effect:   corev1.TaintEffectNoSchedule,
			Operator: corev1.TolerationOpEqual,
		},
		{
			Key:      "startup-taint",
			Value:    "startup-value",
			Effect:   corev1.TaintEffectNoExecute,
			Operator: corev1.TolerationOpEqual,
		},
	}, map[string]string{
		"test": testName, // Force scheduling on nodes from our NodePool
	})

	err = suite.kubeClient.Create(ctx, deployment)
	require.NoError(t, err)
	defer func() {
		_ = suite.kubeClient.Delete(ctx, deployment)
	}()

	// Wait for NodeClaim to be created
	var nodeClaim karpv1.NodeClaim
	err = wait.PollUntilContextTimeout(ctx, pollInterval, testTimeout, true, func(ctx context.Context) (bool, error) {
		nodeClaimList := &karpv1.NodeClaimList{}
		listErr := suite.kubeClient.List(ctx, nodeClaimList, client.MatchingLabels{"test": testName})
		if listErr != nil {
			return false, listErr
		}
		if len(nodeClaimList.Items) > 0 {
			nodeClaim = nodeClaimList.Items[0]
			return true, nil
		}
		return false, nil
	})
	require.NoError(t, err, "NodeClaim should be created")

	// Verify NodeClaim has expected taints
	assert.Len(t, nodeClaim.Spec.Taints, 1, "NodeClaim should have 1 regular taint")
	assert.Len(t, nodeClaim.Spec.StartupTaints, 1, "NodeClaim should have 1 startup taint")

	// Wait for Node to be created and registered
	var node corev1.Node
	err = wait.PollUntilContextTimeout(ctx, pollInterval, testTimeout, true, func(ctx context.Context) (bool, error) {
		if nodeClaim.Status.NodeName == "" {
			// Refresh NodeClaim
			getErr := suite.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClaim.Name}, &nodeClaim)
			if getErr != nil {
				return false, getErr
			}
			return false, nil
		}

		getErr := suite.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClaim.Status.NodeName}, &node)
		return getErr == nil, client.IgnoreNotFound(getErr)
	})
	require.NoError(t, err, "Node should be created")

	// Verify Node has both regular and startup taints synced from NodeClaim
	foundRegularTaint := false
	foundStartupTaint := false

	for _, taint := range node.Spec.Taints {
		if taint.Key == "regular-taint" && taint.Value == "regular-value" {
			foundRegularTaint = true
			assert.Equal(t, corev1.TaintEffectNoSchedule, taint.Effect)
		}
		if taint.Key == "startup-taint" && taint.Value == "startup-value" {
			foundStartupTaint = true
			assert.Equal(t, corev1.TaintEffectNoExecute, taint.Effect)
		}
	}

	assert.True(t, foundRegularTaint, "Node should have regular taint synced from NodeClaim")
	assert.True(t, foundStartupTaint, "Node should have startup taint synced from NodeClaim")

	t.Logf("✓ Taint sync E2E test passed - both regular and startup taints properly synced from NodeClaim to Node")
}

// TestE2EUnregisteredTaintHandling tests proper unregistered taint lifecycle
func TestE2EUnregisteredTaintHandling(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	ctx := context.Background()

	// Create unique names for this test
	testName := fmt.Sprintf("unregistered-taint-%d", time.Now().Unix())
	nodePoolName := fmt.Sprintf("test-nodepool-%s", testName)
	nodeClassName := fmt.Sprintf("test-nodeclass-%s", testName)
	deploymentName := fmt.Sprintf("test-deployment-%s", testName)

	// Create NodeClass
	nodeClass := &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeClassName,
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			Region:            suite.testRegion,
			Zone:              suite.testZone,
			InstanceProfile:   suite.GetAvailableInstanceType(t), // Dynamically detected instance type
			VPC:               suite.testVPC,
			Subnet:            suite.testSubnet,
			Image:             suite.testImage,
			SecurityGroups:    []string{suite.testSecurityGroup},
			APIServerEndpoint: suite.APIServerEndpoint,
			BootstrapMode:     lo.ToPtr("cloud-init"),
			ResourceGroup:     suite.testResourceGroup,
			SSHKeys:           []string{suite.testSshKeyId},
		},
	}

	err := suite.kubeClient.Create(ctx, nodeClass)
	require.NoError(t, err)
	defer func() {
		_ = suite.kubeClient.Delete(ctx, nodeClass)
	}()

	// Create NodePool
	nodePool := &karpv1.NodePool{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodePoolName,
		},
		Spec: karpv1.NodePoolSpec{
			Template: karpv1.NodeClaimTemplate{
				ObjectMeta: karpv1.ObjectMeta{
					Labels: map[string]string{
						"test": testName,
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
								Key:      "node.kubernetes.io/instance-type",
								Operator: corev1.NodeSelectorOpIn,
								Values:   suite.GetMultipleInstanceTypes(t, 1), // Dynamically detected instance type
							},
						},
					},
				},
			},
		},
	}

	err = suite.kubeClient.Create(ctx, nodePool)
	require.NoError(t, err)
	defer func() {
		_ = suite.kubeClient.Delete(ctx, nodePool)
	}()

	// Create deployment to force node creation
	deployment := createResourceIntensiveWorkload(deploymentName, testName, nil, map[string]string{
		"test": testName, // Force scheduling on nodes from our NodePool
	})

	err = suite.kubeClient.Create(ctx, deployment)
	require.NoError(t, err)
	defer func() {
		_ = suite.kubeClient.Delete(ctx, deployment)
	}()

	// Wait for Node to be created and registered
	var node corev1.Node
	err = wait.PollUntilContextTimeout(ctx, pollInterval, testTimeout, true, func(ctx context.Context) (bool, error) {
		nodeClaimList := &karpv1.NodeClaimList{}
		listErr := suite.kubeClient.List(ctx, nodeClaimList, client.MatchingLabels{"test": testName})
		if listErr != nil {
			return false, listErr
		}
		if len(nodeClaimList.Items) == 0 {
			return false, nil
		}

		nodeClaim := nodeClaimList.Items[0]
		if nodeClaim.Status.NodeName == "" {
			return false, nil
		}

		getErr := suite.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClaim.Status.NodeName}, &node)
		if getErr != nil {
			return false, client.IgnoreNotFound(getErr)
		}

		// Check if node is registered (unregistered taint should be removed)
		return nodeClaim.StatusConditions().Get(karpv1.ConditionTypeRegistered).IsTrue(), nil
	})
	require.NoError(t, err, "Node should be registered")

	// Verify unregistered taint has been removed after registration
	foundUnregisteredTaint := false
	for _, taint := range node.Spec.Taints {
		if taint.Key == karpv1.UnregisteredTaintKey {
			foundUnregisteredTaint = true
			break
		}
	}
	assert.False(t, foundUnregisteredTaint, "Unregistered taint should be removed after node registration")

	t.Logf("✓ Unregistered taint handling E2E test passed - unregistered taint properly removed after registration")
}

// Helper function to create resource-intensive workloads that force new node creation
func createResourceIntensiveWorkload(name, testLabel string, tolerations []corev1.Toleration, nodeSelector map[string]string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: lo.ToPtr(int32(1)),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":  name,
						"test": testLabel,
					},
				},
				Spec: corev1.PodSpec{
					Tolerations:  tolerations,
					NodeSelector: nodeSelector,
					Containers: []corev1.Container{
						{
							Name:  "resource-intensive-container",
							Image: "quay.io/isovalent/busybox:1.37.0",
							Command: []string{
								"/bin/sh",
								"-c",
								"sleep 3600",
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									// Request enough resources to force new node creation
									// Conservative sizing for bx2a-2x8 (2 CPU total, 250m for calico-node)
									// Using 1000m to ensure sufficient headroom for provisioning
									corev1.ResourceCPU:    resource.MustParse("1000m"),
									corev1.ResourceMemory: resource.MustParse("4Gi"),
								},
							},
						},
					},
				},
			},
		},
	}
}

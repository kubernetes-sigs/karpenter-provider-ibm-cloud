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
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/controller-runtime/pkg/client"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
)

// TestE2EStartupTaints tests that startup taints are properly applied to new nodes
func TestE2EStartupTaints(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	ctx := context.Background()

	// Create unique names for this test
	testName := fmt.Sprintf("startup-taints-%d", time.Now().Unix())
	t.Cleanup(func() { suite.cleanupTestResources(t, testName) })
	nodePoolName := fmt.Sprintf("test-nodepool-%s", testName)
	nodeClassName := fmt.Sprintf("test-nodeclass-%s", testName)
	deploymentName := fmt.Sprintf("test-deployment-%s", testName)

	// Create NodeClass
	nodeClass := &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:   nodeClassName,
			Labels: taintTestLabels(testName),
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

	// Create NodePool with startup taints
	nodePool := &karpv1.NodePool{
		ObjectMeta: metav1.ObjectMeta{
			Name:   nodePoolName,
			Labels: taintTestLabels(testName),
		},
		Spec: karpv1.NodePoolSpec{
			Template: karpv1.NodeClaimTemplate{
				ObjectMeta: karpv1.ObjectMeta{
					Labels: map[string]string{
						"test":       testName,
						"test-name":  testName,
						"created-by": "karpenter-e2e",
					},
				},
				Spec: karpv1.NodeClaimTemplateSpec{
					NodeClassRef: &karpv1.NodeClassReference{
						Group: "karpenter-ibm.sh",
						Kind:  "IBMNodeClass",
						Name:  nodeClassName,
					},
					Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
						{
							Key:      "node.kubernetes.io/instance-type",
							Operator: corev1.NodeSelectorOpIn,
							Values:   suite.GetMultipleInstanceTypes(t, 1), // Dynamically detected instance type

						},
					},
					StartupTaints: []corev1.Taint{
						{
							Key:    "example.com/startup",
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

	// Create deployment that forces node creation with nodeSelector
	// This ensures the pod is only scheduled on nodes from the specific NodePool with startup taints
	deployment := createResourceIntensiveWorkload(deploymentName, testName, []corev1.Toleration{
		{
			Key:    "example.com/startup",
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

	foundStartup := false
	foundInitializing := false
	for _, taint := range nodeClaim.Spec.StartupTaints {
		if taint.Key == "example.com/startup" {
			foundStartup = true
			assert.Equal(t, corev1.TaintEffectNoSchedule, taint.Effect)
		}
		if taint.Key == "example.com/initializing" {
			foundInitializing = true
			assert.Equal(t, "true", taint.Value)
			assert.Equal(t, corev1.TaintEffectNoSchedule, taint.Effect)
		}
	}
	assert.True(t, foundStartup, "NodeClaim should have 'startup' startup taint")
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
	foundStartupOnNode := false
	foundInitializingOnNode := false
	for _, taint := range node.Spec.Taints {
		if taint.Key == "example.com/startup" {
			foundStartupOnNode = true
			assert.Equal(t, corev1.TaintEffectNoSchedule, taint.Effect)
		}
		if taint.Key == "example.com/initializing" {
			foundInitializingOnNode = true
			assert.Equal(t, "true", taint.Value)
			assert.Equal(t, corev1.TaintEffectNoSchedule, taint.Effect)
		}
	}
	assert.True(t, foundStartupOnNode, "Node should have 'startup' startup taint applied")
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

	t.Logf("PASS: StartupTaints E2E test passed - startup taints properly applied and pods scheduled")
}

// TestE2EStartupTaintsRemoval tests startup taint removal by DaemonSet
func TestE2EStartupTaintsRemoval(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	ctx := context.Background()

	// Create unique names for this test
	testName := fmt.Sprintf("startup-taint-removal-%d", time.Now().Unix())
	t.Cleanup(func() { suite.cleanupTestResources(t, testName) })
	nodePoolName := fmt.Sprintf("test-nodepool-%s", testName)
	nodeClassName := fmt.Sprintf("test-nodeclass-%s", testName)
	deploymentName := fmt.Sprintf("test-deployment-%s", testName)

	// Create NodeClass
	nodeClass := &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:   nodeClassName,
			Labels: taintTestLabels(testName),
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

	// Create NodePool with startup taints
	nodePool := &karpv1.NodePool{
		ObjectMeta: metav1.ObjectMeta{
			Name:   nodePoolName,
			Labels: taintTestLabels(testName),
		},
		Spec: karpv1.NodePoolSpec{
			Template: karpv1.NodeClaimTemplate{
				ObjectMeta: karpv1.ObjectMeta{
					Labels: map[string]string{
						"test":       testName,
						"test-name":  testName,
						"created-by": "karpenter-e2e",
					},
				},
				Spec: karpv1.NodeClaimTemplateSpec{
					NodeClassRef: &karpv1.NodeClassReference{
						Group: "karpenter-ibm.sh",
						Kind:  "IBMNodeClass",
						Name:  nodeClassName,
					},
					Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
						{
							Key:      "node.kubernetes.io/instance-type",
							Operator: corev1.NodeSelectorOpIn,
							Values:   suite.GetMultipleInstanceTypes(t, 1), // Dynamically detected instance type

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

	// Create deployment to force node creation
	deployment := createResourceIntensiveWorkload(deploymentName, testName, nil, map[string]string{"test": testName})

	err = suite.kubeClient.Create(ctx, deployment)
	require.NoError(t, err)

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
		if taint.Key == "example.com/startup-init" && taint.Value == "pending" && taint.Effect == corev1.TaintEffectNoSchedule {
			foundStartupTaint = true
			break
		}
	}

	require.True(t, foundStartupTaint, "Startup taint must be present before removal")
	startup := corev1.Taint{Key: "example.com/startup-init", Value: "pending", Effect: corev1.TaintEffectNoSchedule}
	require.NoError(t, suite.removeTestStartupTaint(ctx, &node, testName, startup))
	suite.waitForPodsToBeScheduled(t, deployment.Name, deployment.Namespace)
	var current corev1.Node
	require.NoError(t, suite.kubeClient.Get(ctx, client.ObjectKeyFromObject(&node), &current))
	require.Equal(t, node.UID, current.UID)
	for _, taint := range current.Spec.Taints {
		require.False(t, sameTestTaint(startup, taint), "Application startup taint must remain removed")
	}
}

// TestE2ETaintsBasicScheduling tests basic taint/toleration scheduling
func TestE2ETaintsBasicScheduling(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	ctx := context.Background()

	// Create unique names for this test
	testName := fmt.Sprintf("basic-taints-%d", time.Now().Unix())
	t.Cleanup(func() { suite.cleanupTestResources(t, testName) })
	nodePoolName := fmt.Sprintf("test-nodepool-%s", testName)
	nodeClassName := fmt.Sprintf("test-nodeclass-%s", testName)
	tolerantDeploymentName := fmt.Sprintf("tolerant-deployment-%s", testName)
	intolerantDeploymentName := fmt.Sprintf("intolerant-deployment-%s", testName)

	// Create NodeClass
	nodeClass := &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:   nodeClassName,
			Labels: taintTestLabels(testName),
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

	// Create NodePool with regular taints
	nodePool := &karpv1.NodePool{
		ObjectMeta: metav1.ObjectMeta{
			Name:   nodePoolName,
			Labels: taintTestLabels(testName),
		},
		Spec: karpv1.NodePoolSpec{
			Template: karpv1.NodeClaimTemplate{
				ObjectMeta: karpv1.ObjectMeta{
					Labels: map[string]string{
						"test":       testName,
						"test-name":  testName,
						"created-by": "karpenter-e2e",
					},
				},
				Spec: karpv1.NodeClaimTemplateSpec{
					NodeClassRef: &karpv1.NodeClassReference{
						Group: "karpenter-ibm.sh",
						Kind:  "IBMNodeClass",
						Name:  nodeClassName,
					},
					Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
						{
							Key:      "node.kubernetes.io/instance-type",
							Operator: corev1.NodeSelectorOpIn,
							Values:   suite.GetMultipleInstanceTypes(t, 1), // Dynamically detected instance type

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
	intolerantDeployment := createResourceIntensiveWorkload(intolerantDeploymentName, testName+"intolerant", nil, map[string]string{"test": testName})

	err = suite.kubeClient.Create(ctx, intolerantDeployment)
	require.NoError(t, err)

	// Verify intolerant deployment does NOT become ready (check multiple times to ensure stability)
	suite.verifyDeploymentNotReady(t, intolerantDeployment.Name, intolerantDeployment.Namespace, 30*time.Second)

	t.Logf("PASS: Basic taints scheduling E2E test passed - taints properly prevent intolerant pods from scheduling")
}

// TestE2ETaintValues tests that taint values are correctly applied and updated
func TestE2ETaintValues(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	ctx := context.Background()

	// Create unique names for this test
	testName := fmt.Sprintf("taint-values-%d", time.Now().Unix())
	t.Cleanup(func() { suite.cleanupTestResources(t, testName) })
	nodePoolName := fmt.Sprintf("test-nodepool-%s", testName)
	nodeClassName := fmt.Sprintf("test-nodeclass-%s", testName)
	deploymentName := fmt.Sprintf("test-deployment-%s", testName)

	// Create NodeClass
	nodeClass := &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:   nodeClassName,
			Labels: taintTestLabels(testName),
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

	// Create NodePool with specific taint values
	nodePool := &karpv1.NodePool{
		ObjectMeta: metav1.ObjectMeta{
			Name:   nodePoolName,
			Labels: taintTestLabels(testName),
		},
		Spec: karpv1.NodePoolSpec{
			Template: karpv1.NodeClaimTemplate{
				ObjectMeta: karpv1.ObjectMeta{
					Labels: map[string]string{
						"test":       testName,
						"test-name":  testName,
						"created-by": "karpenter-e2e",
					},
				},
				Spec: karpv1.NodeClaimTemplateSpec{
					NodeClassRef: &karpv1.NodeClassReference{
						Group: "karpenter-ibm.sh",
						Kind:  "IBMNodeClass",
						Name:  nodeClassName,
					},
					Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
						{
							Key:      "node.kubernetes.io/instance-type",
							Operator: corev1.NodeSelectorOpIn,
							Values:   suite.GetMultipleInstanceTypes(t, 1), // Dynamically detected instance type

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

	t.Logf("PASS: Taint values E2E test passed - taint values correctly applied and verified")
}

// TestE2ETaintSync tests taint synchronization from NodeClaim to Node
func TestE2ETaintSync(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	ctx := context.Background()

	// Create unique names for this test
	testName := fmt.Sprintf("taint-sync-%d", time.Now().Unix())
	t.Cleanup(func() { suite.cleanupTestResources(t, testName) })
	nodePoolName := fmt.Sprintf("test-nodepool-%s", testName)
	nodeClassName := fmt.Sprintf("test-nodeclass-%s", testName)
	deploymentName := fmt.Sprintf("test-deployment-%s", testName)

	// Create NodeClass
	nodeClass := &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:   nodeClassName,
			Labels: taintTestLabels(testName),
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

	// Create NodePool with both regular and startup taints
	nodePool := &karpv1.NodePool{
		ObjectMeta: metav1.ObjectMeta{
			Name:   nodePoolName,
			Labels: taintTestLabels(testName),
		},
		Spec: karpv1.NodePoolSpec{
			Template: karpv1.NodeClaimTemplate{
				ObjectMeta: karpv1.ObjectMeta{
					Labels: map[string]string{
						"test":       testName,
						"test-name":  testName,
						"created-by": "karpenter-e2e",
					},
				},
				Spec: karpv1.NodeClaimTemplateSpec{
					NodeClassRef: &karpv1.NodeClassReference{
						Group: "karpenter-ibm.sh",
						Kind:  "IBMNodeClass",
						Name:  nodeClassName,
					},
					Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
						{
							Key:      "node.kubernetes.io/instance-type",
							Operator: corev1.NodeSelectorOpIn,
							Values:   suite.GetMultipleInstanceTypes(t, 1), // Dynamically detected instance type

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

	t.Logf("PASS: Taint sync E2E test passed - both regular and startup taints properly synced from NodeClaim to Node")
}

// TestE2EUnregisteredTaintHandling tests proper unregistered taint lifecycle
func TestE2EUnregisteredTaintHandling(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	ctx := context.Background()

	// Create unique names for this test
	testName := fmt.Sprintf("unregistered-taint-%d", time.Now().Unix())
	t.Cleanup(func() { suite.cleanupTestResources(t, testName) })
	nodePoolName := fmt.Sprintf("test-nodepool-%s", testName)
	nodeClassName := fmt.Sprintf("test-nodeclass-%s", testName)
	deploymentName := fmt.Sprintf("test-deployment-%s", testName)

	// Create NodeClass
	nodeClass := &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:   nodeClassName,
			Labels: taintTestLabels(testName),
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

	// Create NodePool
	nodePool := &karpv1.NodePool{
		ObjectMeta: metav1.ObjectMeta{
			Name:   nodePoolName,
			Labels: taintTestLabels(testName),
		},
		Spec: karpv1.NodePoolSpec{
			Template: karpv1.NodeClaimTemplate{
				ObjectMeta: karpv1.ObjectMeta{
					Labels: map[string]string{
						"test":       testName,
						"test-name":  testName,
						"created-by": "karpenter-e2e",
					},
				},
				Spec: karpv1.NodeClaimTemplateSpec{
					NodeClassRef: &karpv1.NodeClassReference{
						Group: "karpenter-ibm.sh",
						Kind:  "IBMNodeClass",
						Name:  nodeClassName,
					},
					Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
						{
							Key:      "node.kubernetes.io/instance-type",
							Operator: corev1.NodeSelectorOpIn,
							Values:   suite.GetMultipleInstanceTypes(t, 1), // Dynamically detected instance type

						},
					},
				},
			},
		},
	}

	err = suite.kubeClient.Create(ctx, nodePool)
	require.NoError(t, err)

	selector := labels.Set{"test": testName}.String()
	initialNodes, err := suite.coreClient.Nodes().List(ctx, metav1.ListOptions{LabelSelector: selector})
	require.NoError(t, err)
	require.Empty(t, initialNodes.Items, "The new fixture must not have pre-existing Nodes")
	observeCtx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()
	nodeEvents, err := suite.coreClient.Nodes().Watch(observeCtx, metav1.ListOptions{
		LabelSelector: selector, ResourceVersion: initialNodes.ResourceVersion,
	})
	require.NoError(t, err)
	defer nodeEvents.Stop()

	// Create deployment to force node creation
	deployment := createResourceIntensiveWorkload(deploymentName, testName, nil, map[string]string{
		"test": testName, // Force scheduling on nodes from our NodePool
	})

	err = suite.kubeClient.Create(ctx, deployment)
	require.NoError(t, err)

	initialNode, initialClaim, err := suite.waitForUnregisteredTestNode(observeCtx, nodeEvents, nodePool, testName)
	require.NoError(t, err, "Must observe the unregistered taint on this fixture's initial Node")
	var node corev1.Node
	err = wait.PollUntilContextTimeout(ctx, pollInterval, testTimeout, true, func(ctx context.Context) (bool, error) {
		var claim karpv1.NodeClaim
		if getErr := suite.kubeClient.Get(ctx, client.ObjectKeyFromObject(initialClaim), &claim); getErr != nil {
			return false, getErr
		}
		if getErr := suite.kubeClient.Get(ctx, client.ObjectKeyFromObject(initialNode), &node); getErr != nil {
			return false, getErr
		}
		return registeredTestNodeMatches(initialNode, initialClaim, &node, &claim)
	})
	require.NoError(t, err, "The same Node and NodeClaim must register and remove the observed taint")

	t.Logf("PASS: Unregistered taint handling E2E test passed - unregistered taint properly removed after registration")
}

func (s *E2ETestSuite) waitForUnregisteredTestNode(ctx context.Context, events watch.Interface, pool *karpv1.NodePool, testName string) (*corev1.Node, *karpv1.NodeClaim, error) {
	for {
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case event, ok := <-events.ResultChan():
			if !ok {
				return nil, nil, fmt.Errorf("Node watch ended before unregistered-taint evidence")
			}
			if event.Type == watch.Error {
				return nil, nil, fmt.Errorf("Node watch failed before unregistered-taint evidence: %v", event.Object)
			}
			if event.Type != watch.Added && event.Type != watch.Modified {
				continue
			}
			node, ok := event.Object.(*corev1.Node)
			if !ok || node.Labels["test"] != testName || node.Labels[karpv1.NodePoolLabelKey] != pool.Name {
				continue
			}
			present := false
			for _, taint := range node.Spec.Taints {
				present = present || (taint.MatchTaint(&karpv1.UnregisteredNoExecuteTaint) && taint.Value == karpv1.UnregisteredNoExecuteTaint.Value)
			}
			if !present {
				continue
			}
			var claim karpv1.NodeClaim
			if err := s.kubeClient.Get(ctx, client.ObjectKey{Name: node.Name}, &claim); err != nil {
				return nil, nil, err
			}
			owned := false
			for _, owner := range claim.OwnerReferences {
				owned = owned || (owner.APIVersion == "karpenter.sh/v1" && owner.Kind == "NodePool" && owner.Name == pool.Name && owner.UID == pool.UID)
			}
			if node.UID == "" || claim.UID == "" || pool.UID == "" || !owned || claim.Labels["test"] != testName ||
				claim.Labels[karpv1.NodePoolLabelKey] != pool.Name || claim.Status.ProviderID == "" || node.Spec.ProviderID != claim.Status.ProviderID ||
				!node.DeletionTimestamp.IsZero() || !claim.DeletionTimestamp.IsZero() {
				return nil, nil, fmt.Errorf("observed unregistered Node does not match the live owned claim")
			}
			return node.DeepCopy(), claim.DeepCopy(), nil
		}
	}
}

func registeredTestNodeMatches(initialNode *corev1.Node, initialClaim *karpv1.NodeClaim, node *corev1.Node, claim *karpv1.NodeClaim) (bool, error) {
	if node.UID != initialNode.UID || claim.UID != initialClaim.UID || node.Spec.ProviderID != initialNode.Spec.ProviderID ||
		claim.Status.ProviderID != initialClaim.Status.ProviderID || !node.DeletionTimestamp.IsZero() || !claim.DeletionTimestamp.IsZero() {
		return false, fmt.Errorf("observed Node or NodeClaim identity changed before registration")
	}
	if !claim.StatusConditions().Get(karpv1.ConditionTypeRegistered).IsTrue() {
		return false, nil
	}
	if claim.Status.NodeName != node.Name || len(node.OwnerReferences) != 1 || node.OwnerReferences[0].UID != claim.UID ||
		node.OwnerReferences[0].APIVersion != "karpenter.sh/v1" || node.OwnerReferences[0].Kind != "NodeClaim" || node.OwnerReferences[0].Name != claim.Name {
		return false, fmt.Errorf("registered Node is not owned by the observed NodeClaim")
	}
	for _, taint := range node.Spec.Taints {
		if taint.Key == karpv1.UnregisteredTaintKey {
			return false, nil
		}
	}
	return true, nil
}

func taintTestLabels(testName string) map[string]string {
	return map[string]string{
		"test":       "e2e",
		"test-name":  testName,
		"created-by": "karpenter-e2e",
	}
}

func createResourceIntensiveWorkload(name, testLabel string, tolerations []corev1.Toleration, nodeSelector map[string]string) *appsv1.Deployment {
	testName := nodeSelector["test"]
	if testName == "" {
		testName = testLabel
	}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels:    taintTestLabels(testName),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: lo.ToPtr(int32(1)),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":        name,
						"test":       testLabel,
						"test-name":  testName,
						"created-by": "karpenter-e2e",
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
									corev1.ResourceCPU:    resource.MustParse("1000m"),
									corev1.ResourceMemory: resource.MustParse("1Gi"),
								},
							},
						},
					},
				},
			},
		},
	}
}

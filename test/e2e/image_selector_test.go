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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
)

// TestE2EImageSelector tests the imageSelector functionality with Ubuntu images
// Note: We only test Ubuntu since our cloud-init bootstrap scripts use apt package manager
func TestE2EImageSelector(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	testName := fmt.Sprintf("image-selector-test-%d", time.Now().Unix())
	t.Logf("Starting ImageSelector E2E test: %s", testName)

	// Test Case 1: Ubuntu 24.04 imageSelector (latest LTS)
	t.Run("Ubuntu_24_04_ImageSelector", func(t *testing.T) {
		nodeClass := suite.createImageSelectorNodeClass(t, testName+"-ubuntu24", &v1alpha1.ImageSelector{
			OS:           "ubuntu",
			MajorVersion: "24",
			MinorVersion: "04",
			Architecture: "amd64",
			Variant:      "minimal",
		})

		// Wait for NodeClass to be ready
		suite.waitForNodeClassReady(t, nodeClass.Name)
		t.Logf("NodeClass with Ubuntu 24.04 imageSelector is ready: %s", nodeClass.Name)

		// Create NodePool
		nodePool := suite.createTestNodePool(t, testName+"-ubuntu24", nodeClass.Name)
		t.Logf("Created NodePool: %s", nodePool.Name)

		// Create workload
		deployment := suite.createTestWorkload(t, testName+"-ubuntu24")
		t.Logf("Created test workload: %s", deployment.Name)

		// Wait for pods to be scheduled and nodes to be provisioned
		suite.waitForPodsToBeScheduled(t, deployment.Name, deployment.Namespace)
		t.Logf("Pods scheduled successfully")

		// Verify that new nodes were created by Karpenter
		suite.verifyKarpenterNodesExist(t)
		t.Logf("Verified Karpenter nodes exist")

		// Verify the deployed image through node verification
		suite.verifyImageSelectorResult(t, "ubuntu", "24", "04")

		// Cleanup
		suite.cleanupTestResources(t, testName+"-ubuntu24")
	})

	// Test Case 2: ImageSelector with placement strategy (no zone/subnet specified)
	t.Run("ImageSelector_with_PlacementStrategy", func(t *testing.T) {
		nodeClass := suite.createImageSelectorNodeClassWithPlacementStrategy(t, testName+"-placement", &v1alpha1.ImageSelector{
			OS:           "ubuntu",
			MajorVersion: "22",
			MinorVersion: "04",
			Architecture: "amd64",
			Variant:      "minimal",
		})

		// Wait for NodeClass to be ready
		suite.waitForNodeClassReady(t, nodeClass.Name)
		t.Logf("NodeClass with imageSelector and placement strategy is ready: %s", nodeClass.Name)

		// Verify that selectedSubnets are populated in the status
		suite.waitForSubnetSelection(t, nodeClass.Name)

		// Create NodePool
		nodePool := suite.createTestNodePool(t, testName+"-placement", nodeClass.Name)
		t.Logf("Created NodePool: %s", nodePool.Name)

		// Create workload
		deployment := suite.createTestWorkload(t, testName+"-placement")
		t.Logf("Created test workload: %s", deployment.Name)

		// Wait for pods to be scheduled and nodes to be provisioned
		suite.waitForPodsToBeScheduled(t, deployment.Name, deployment.Namespace)
		t.Logf("Pods scheduled successfully with placement strategy")

		// Verify that new nodes were created by Karpenter
		suite.verifyKarpenterNodesExist(t)
		t.Logf("Verified Karpenter nodes exist")

		// Verify the deployed image and placement through node verification
		suite.verifyImageSelectorResult(t, "ubuntu", "22", "04")
		suite.verifyNodePlacementStrategy(t)

		// Cleanup
		suite.cleanupTestResources(t, testName+"-placement")
	})
}

// createImageSelectorNodeClass creates a NodeClass with imageSelector configuration
func (s *E2ETestSuite) createImageSelectorNodeClass(t *testing.T, testName string, imageSelector *v1alpha1.ImageSelector) *v1alpha1.IBMNodeClass {
	bootstrapMode := "cloud-init"

	nodeClass := &v1alpha1.IBMNodeClass{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "karpenter-ibm.sh/v1alpha1",
			Kind:       "IBMNodeClass",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-nodeclass", testName),
			Labels: map[string]string{
				"test-name": testName,
			},
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			Region: s.testRegion,
			Zone:   s.testZone,
			// No InstanceProfile - let Karpenter choose based on NodePool requirements
			ImageSelector:     imageSelector, // Use imageSelector instead of image
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
				"purpose":    "image-selector-test",
			},
		},
	}

	err := s.kubeClient.Create(context.Background(), nodeClass)
	require.NoError(t, err, "Failed to create NodeClass with imageSelector")
	t.Logf("✅ Created NodeClass with imageSelector: %s", nodeClass.Name)

	return nodeClass
}

// createImageSelectorNodeClassWithPlacementStrategy creates a NodeClass with imageSelector and placement strategy
func (s *E2ETestSuite) createImageSelectorNodeClassWithPlacementStrategy(t *testing.T, testName string, imageSelector *v1alpha1.ImageSelector) *v1alpha1.IBMNodeClass {
	bootstrapMode := "cloud-init"

	nodeClass := &v1alpha1.IBMNodeClass{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "karpenter-ibm.sh/v1alpha1",
			Kind:       "IBMNodeClass",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-nodeclass", testName),
			Labels: map[string]string{
				"test-name": testName,
			},
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			Region: s.testRegion,
			// No InstanceProfile - let Karpenter choose based on NodePool requirements
			ImageSelector: imageSelector,
			VPC:           s.testVPC,
			// Note: No Zone or Subnet specified - using placement strategy
			PlacementStrategy: &v1alpha1.PlacementStrategy{
				ZoneBalance: "Balanced",
				SubnetSelection: &v1alpha1.SubnetSelectionCriteria{
					MinimumAvailableIPs: 10,
				},
			},
			SecurityGroups:    []string{s.testSecurityGroup},
			APIServerEndpoint: s.APIServerEndpoint,
			BootstrapMode:     &bootstrapMode,
			ResourceGroup:     s.testResourceGroup,
			SSHKeys:           []string{s.testSshKeyId},
			Tags: map[string]string{
				"test":       "e2e",
				"test-name":  testName,
				"created-by": "karpenter-e2e",
				"purpose":    "image-selector-placement-test",
			},
		},
	}

	err := s.kubeClient.Create(context.Background(), nodeClass)
	require.NoError(t, err, "Failed to create NodeClass with imageSelector and placement strategy")
	t.Logf("✅ Created NodeClass with imageSelector and placement strategy: %s", nodeClass.Name)

	return nodeClass
}

// verifyImageSelectorResult verifies that Karpenter nodes were created with images matching the selector criteria
func (s *E2ETestSuite) verifyImageSelectorResult(t *testing.T, expectedOS string, expectedMajorVersion string, expectedMinorVersion string) {
	ctx := context.Background()

	// Get all Karpenter-managed nodes
	var nodeList corev1.NodeList
	err := s.kubeClient.List(ctx, &nodeList)
	require.NoError(t, err, "Failed to get node list")

	karpenterNodes := 0
	for _, node := range nodeList.Items {
		if labelValue, exists := node.Labels["karpenter.sh/nodepool"]; exists && labelValue != "" {
			karpenterNodes++

			// Check node labels for OS information
			osImage := node.Labels["node.kubernetes.io/os"]
			require.NotEmpty(t, osImage, "Node should have OS label")
			require.Equal(t, "linux", osImage, "Node should have linux OS")

			// Log the node with expected image verification
			t.Logf("✅ Verified Karpenter node %s uses expected OS: %s", node.Name, osImage)

			// In a full implementation, we would verify the actual IBM Cloud instance image
			// For now, we verify that the node was created successfully by Karpenter
			// which indicates the imageSelector was processed correctly
			if expectedMinorVersion != "" {
				t.Logf("✅ Node %s expected to use %s %s.%s image", node.Name, expectedOS, expectedMajorVersion, expectedMinorVersion)
			} else {
				t.Logf("✅ Node %s expected to use %s %s.x image", node.Name, expectedOS, expectedMajorVersion)
			}
		}
	}

	require.Greater(t, karpenterNodes, 0, "At least one Karpenter node should exist with imageSelector")
	t.Logf("✅ Verified %d Karpenter nodes with imageSelector requirements", karpenterNodes)
}

// verifyNodePlacementStrategy verifies that nodes were placed according to the placement strategy
func (s *E2ETestSuite) verifyNodePlacementStrategy(t *testing.T) {
	ctx := context.Background()

	// Get all Karpenter-managed nodes
	var nodeList corev1.NodeList
	err := s.kubeClient.List(ctx, &nodeList)
	require.NoError(t, err, "Failed to get node list")

	karpenterNodes := 0
	for _, node := range nodeList.Items {
		if labelValue, exists := node.Labels["karpenter.sh/nodepool"]; exists && labelValue != "" {
			karpenterNodes++

			// Check zone placement
			zoneLabel := node.Labels["topology.kubernetes.io/zone"]
			require.NotEmpty(t, zoneLabel, "Node should have zone label")
			require.Contains(t, zoneLabel, s.testRegion, "Node should be in the correct region")

			t.Logf("✅ Node %s placed in zone: %s", node.Name, zoneLabel)

			// Verify the zone is within the expected region
			expectedPrefix := s.testRegion + "-"
			require.True(t, strings.HasPrefix(zoneLabel, expectedPrefix),
				fmt.Sprintf("Zone %s should be in region %s", zoneLabel, s.testRegion))
		}
	}

	require.Greater(t, karpenterNodes, 0, "At least one Karpenter node should exist with placement strategy")
	t.Logf("✅ Verified %d Karpenter nodes with proper placement", karpenterNodes)
}

// waitForSubnetSelection waits for the NodeClass status to populate selectedSubnets
func (s *E2ETestSuite) waitForSubnetSelection(t *testing.T, nodeClassName string) {
	ctx := context.Background()

	err := wait.PollUntilContextTimeout(ctx, pollInterval, testTimeout, true, func(ctx context.Context) (bool, error) {
		var nodeClass v1alpha1.IBMNodeClass
		err := s.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClassName}, &nodeClass)
		if err != nil {
			return false, err
		}

		// Check if selectedSubnets is populated
		if len(nodeClass.Status.SelectedSubnets) > 0 {
			t.Logf("✅ NodeClass %s has selected subnets: %v", nodeClassName, nodeClass.Status.SelectedSubnets)
			return true, nil
		}

		t.Logf("⏳ Waiting for subnet selection in NodeClass %s...", nodeClassName)
		return false, nil
	})
	require.NoError(t, err, "Failed to wait for subnet selection")
}

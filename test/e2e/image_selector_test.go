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
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

// TestE2EImageSelector tests the imageSelector functionality with Ubuntu images
// Note: We only test Ubuntu since our cloud-init bootstrap scripts use apt package manager
func TestE2EImageSelector(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	testName := fmt.Sprintf("image-selector-test-%d", time.Now().Unix())
	t.Logf("Starting ImageSelector E2E test: %s", testName)

	// Test Case 1: Ubuntu 24.04 imageSelector (latest LTS)
	t.Run("Ubuntu_24_04_ImageSelector", func(t *testing.T) {
		t.Cleanup(func() { suite.cleanupTestResources(t, testName+"-ubuntu24") })
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
		suite.verifyImageSelectorResult(t, nodePool, nodeClass)

	})

	// Test Case 2: ImageSelector with placement strategy (no zone/subnet specified)
	t.Run("ImageSelector_with_PlacementStrategy", func(t *testing.T) {
		t.Cleanup(func() { suite.cleanupTestResources(t, testName+"-placement") })
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
		suite.verifyImageSelectorResult(t, nodePool, nodeClass)
		suite.verifyNodePlacementStrategy(t, nodePool, nodeClass)

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
				"test-name":  testName,
				"test":       "e2e",
				"created-by": "karpenter-e2e",
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
	t.Logf("Created NodeClass with imageSelector: %s", nodeClass.Name)

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
				"test-name":  testName,
				"test":       "e2e",
				"created-by": "karpenter-e2e",
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
	t.Logf("Created NodeClass with imageSelector and placement strategy: %s", nodeClass.Name)

	return nodeClass
}

func (s *E2ETestSuite) verifyImageSelectorResult(t *testing.T, pool *karpv1.NodePool, class *v1alpha1.IBMNodeClass) {
	t.Helper()
	ctx := context.Background()
	var fresh v1alpha1.IBMNodeClass
	require.NoError(t, s.kubeClient.Get(ctx, client.ObjectKeyFromObject(class), &fresh))
	require.Equal(t, class.UID, fresh.UID)
	require.NotNil(t, fresh.Spec.ImageSelector)
	var claims karpv1.NodeClaimList
	require.NoError(t, s.kubeClient.List(ctx, &claims, client.MatchingLabels{karpv1.NodePoolLabelKey: pool.Name}))
	require.NotEmpty(t, claims.Items, "Image selector test must own real allocations")
	vpc, err := s.vpcClient()
	require.NoError(t, err)
	for _, claim := range claims.Items {
		owned := false
		for _, owner := range claim.OwnerReferences {
			if owner.Kind == "NodePool" && owner.UID == pool.UID {
				owned = true
			}
		}
		require.True(t, owned, "Image verification must not include foreign claims")
		require.NotNil(t, claim.Spec.NodeClassRef)
		require.Equal(t, class.Name, claim.Spec.NodeClassRef.Name)
		id := vpcInstanceID(claim.Status.ProviderID)
		require.NotEmpty(t, id)
		instance, err := vpc.GetInstance(ctx, id)
		require.NoError(t, err)
		s.verifyOwnedTestInstance(t, instance, &claim, class)
		require.NotNil(t, instance.Image)
		require.NotNil(t, instance.Image.ID)
		image, err := vpc.GetImage(ctx, *instance.Image.ID)
		require.NoError(t, err)
		require.NoError(t, selectedTestImageMatches(image, fresh.Spec.ImageSelector))
		var node corev1.Node
		require.NoError(t, s.kubeClient.Get(ctx, client.ObjectKey{Name: claim.Status.NodeName}, &node))
		require.Equal(t, "linux", node.Labels[corev1.LabelOSStable])
		t.Logf("Verified owned instance %s image %s (%s)", id, *image.ID, *image.Name)
	}
}

func selectedTestImageMatches(image *vpcv1.Image, selector *v1alpha1.ImageSelector) error {
	if image == nil || image.ID == nil || image.Name == nil || image.OperatingSystem == nil || selector == nil {
		return fmt.Errorf("image has no verifiable identity or operating system metadata")
	}
	os := image.OperatingSystem
	if os.Family == nil || (!strings.EqualFold(*os.Family, selector.OS) && !strings.EqualFold(*os.Family, selector.OS+" Linux")) || os.Name == nil || os.Version == nil {
		return fmt.Errorf("actual image operating system differs from selector")
	}
	if !regexp.MustCompile("^" + regexp.QuoteMeta(selector.MajorVersion) + "([.-]|$)").MatchString(*os.Version) {
		return fmt.Errorf("actual image major version differs from selector")
	}
	version := selector.OS + "-" + selector.MajorVersion
	if selector.MinorVersion != "" {
		version += "-" + selector.MinorVersion
	}
	if !strings.HasPrefix(strings.ToLower(*os.Name), version+"-") {
		return fmt.Errorf("actual operating system name does not identify the requested OS version")
	}
	if !strings.Contains(strings.ToLower(*image.Name), version+"-") {
		return fmt.Errorf("actual image name does not identify the requested OS version")
	}
	if selector.Architecture != "" && (os.Architecture == nil || *os.Architecture != selector.Architecture) {
		return fmt.Errorf("actual image architecture differs from selector")
	}
	if selector.Variant != "" && !strings.Contains(*image.Name, "-"+selector.Variant+"-") {
		return fmt.Errorf("actual image variant differs from selector")
	}
	return nil
}

// verifyNodePlacementStrategy verifies that nodes were placed according to the placement strategy
func (s *E2ETestSuite) verifyNodePlacementStrategy(t *testing.T, pool *karpv1.NodePool, class *v1alpha1.IBMNodeClass) {
	t.Helper()
	ctx := context.Background()
	var fresh v1alpha1.IBMNodeClass
	require.NoError(t, s.kubeClient.Get(ctx, client.ObjectKeyFromObject(class), &fresh))
	require.Equal(t, class.UID, fresh.UID)
	require.NotEmpty(t, fresh.Status.SelectedSubnets)
	var claims karpv1.NodeClaimList
	require.NoError(t, s.kubeClient.List(ctx, &claims, client.MatchingLabels{karpv1.NodePoolLabelKey: pool.Name}))
	require.NotEmpty(t, claims.Items)
	vpc, err := s.vpcClient()
	require.NoError(t, err)
	for _, claim := range claims.Items {
		owned := false
		for _, owner := range claim.OwnerReferences {
			if owner.Kind == "NodePool" && owner.UID == pool.UID {
				owned = true
			}
		}
		require.True(t, owned, "Placement verification must include only this test's allocations")
		instance, err := vpc.GetInstance(ctx, vpcInstanceID(claim.Status.ProviderID))
		require.NoError(t, err)
		s.verifyOwnedTestInstance(t, instance, &claim, class)
		actualSubnet := instanceSubnetID(instance)
		require.NotEmpty(t, actualSubnet)
		require.Contains(t, fresh.Status.SelectedSubnets, actualSubnet)
		require.NotNil(t, instance.Zone)
		require.NotNil(t, instance.Zone.Name)
		var node corev1.Node
		require.NoError(t, s.kubeClient.Get(ctx, client.ObjectKey{Name: claim.Status.NodeName}, &node))
		require.Equal(t, claim.Status.ProviderID, node.Spec.ProviderID)
		require.Equal(t, *instance.Zone.Name, node.Labels[corev1.LabelTopologyZone])
		require.True(t, strings.HasPrefix(*instance.Zone.Name, fresh.Spec.Region+"-"))
	}
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
			t.Logf("NodeClass %s has selected subnets: %v", nodeClassName, nodeClass.Status.SelectedSubnets)
			return true, nil
		}

		t.Logf("Waiting for subnet selection in NodeClass %s...", nodeClassName)
		return false, nil
	})
	require.NoError(t, err, "Failed to wait for subnet selection")
}

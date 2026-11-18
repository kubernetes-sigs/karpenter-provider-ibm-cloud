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
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
)

func TestE2ESubnetDrift_PlacementStrategy_DetectedAndReplaced(t *testing.T) {
	if strings.TrimSpace(os.Getenv("TEST_SUBNETS")) == "" {
		t.Skip("TEST_SUBNETS must configure real zone=subnet alternatives")
	}
	suite := SetupE2ETestSuite(t)
	testName := fmt.Sprintf("subnet-drift-placement-%d", time.Now().Unix())
	t.Logf("Starting subnet drift with PlacementStrategy test: %s", testName)

	suite.WithAutoCleanup(t, testName, func() {
		ctx := t.Context()

		// Step 1: Create NodeClass with PlacementStrategy (no explicit subnet)
		nodeClass := suite.createTestNodeClassWithPlacementStrategy(t, testName)
		suite.waitForNodeClassReady(t, nodeClass.Name)
		require.Empty(t, nodeClass.Spec.Subnet, "NodeClass must NOT have explicit subnet")
		require.NotNil(t, nodeClass.Spec.PlacementStrategy, "NodeClass must have PlacementStrategy")
		t.Logf("OK: NodeClass %s ready with PlacementStrategy (ZoneBalance=%s)",
			nodeClass.Name, nodeClass.Spec.PlacementStrategy.ZoneBalance)

		// Step 2: Wait for autoplacement controller to populate Status.SelectedSubnets
		var currentNodeClass v1alpha1.IBMNodeClass
		waitErr := wait.PollUntilContextTimeout(ctx, 5*time.Second, 2*time.Minute, true,
			func(ctx context.Context) (bool, error) {
				err := suite.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClass.Name}, &currentNodeClass)
				if err != nil {
					return false, err
				}
				if len(currentNodeClass.Status.SelectedSubnets) > 0 {
					t.Logf("OK: Autoplacement controller populated SelectedSubnets: %v",
						currentNodeClass.Status.SelectedSubnets)
					return true, nil
				}
				t.Logf("Waiting: SelectedSubnets not yet populated by autoplacement controller")
				return false, nil
			})
		require.NoError(t, waitErr, "Autoplacement controller should populate Status.SelectedSubnets")

		// Step 3: Create NodePool and workload to trigger provisioning
		nodePool := suite.createDriftNodePool(t, testName, nodeClass.Name)
		t.Logf("OK: Created NodePool %s", nodePool.Name)

		deployment := suite.createTestWorkload(t, testName)
		suite.waitForPodsToBeScheduled(t, deployment.Name, deployment.Namespace)
		t.Logf("OK: Workload %s/%s scheduled", deployment.Namespace, deployment.Name)

		// Step 4: Capture the initial READY NodeClaim
		var originalNC karpv1.NodeClaim
		var originalName string
		waitErr = wait.PollUntilContextTimeout(ctx, 10*time.Second, testTimeout, true,
			func(ctx context.Context) (bool, error) {
				var nodeClaimList karpv1.NodeClaimList
				err := suite.kubeClient.List(ctx, &nodeClaimList, client.MatchingLabels{
					"test-name": testName,
				})
				if err != nil {
					t.Logf("Error: Failed to list NodeClaims: %v", err)
					return false, err
				}
				if len(nodeClaimList.Items) == 0 {
					t.Logf("Waiting: No NodeClaims found yet for test %s", testName)
					return false, nil
				}
				for _, nc := range nodeClaimList.Items {
					if nc.Labels[karpv1.NodePoolLabelKey] == nodePool.Name && suite.isNodeClaimReady(nc) {
						originalNC = nc
						originalName = nc.Name
						t.Logf("OK: Selected READY NodeClaim %s as original", originalName)
						return true, nil
					}
				}
				t.Logf("Waiting: NodeClaims exist but none are READY yet")
				return false, nil
			})
		if waitErr != nil {
			suite.dumpNodeClassOnFailure(t, nodeClass.Name, "no READY NodeClaim for subnet drift test")
			suite.logNodeClaimStatus(t)
			suite.logNodePoolStatus(t, nodePool.Name)
		}
		require.NoError(t, waitErr, "Should find a READY NodeClaim")

		// Step 5: Verify subnet annotation was set by instance provider
		storedSubnet := originalNC.Annotations[v1alpha1.AnnotationIBMNodeClaimSubnetID]
		require.NotEmpty(t, storedSubnet, "NodeClaim must have subnet annotation")
		t.Logf("OK: NodeClaim %s used subnet %s", originalName, storedSubnet)

		// Verify stored subnet came from the SelectedSubnets pool
		found := false
		for _, s := range currentNodeClass.Status.SelectedSubnets {
			if s == storedSubnet {
				found = true
				break
			}
		}
		require.True(t, found, "Stored subnet %s should be in SelectedSubnets %v",
			storedSubnet, currentNodeClass.Status.SelectedSubnets)

		network, err := suite.vpcClient()
		require.NoError(t, err)
		fixtureCtx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		alternate, err := alternateDriftSubnet(fixtureCtx, network, os.Getenv("TEST_SUBNETS"), storedSubnet, suite.testVPC, suite.testRegion, nodePool)
		require.NoError(t, err, "resolve a real alternate subnet")
		if alternate == nil {
			t.Skip("TEST_SUBNETS contains no distinct subnet allowed by the test NodePool")
		}

		require.NoError(t, suite.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClass.Name}, &currentNodeClass))
		updated := currentNodeClass.DeepCopy()
		updated.Spec.Subnet = *alternate.ID
		updated.Spec.Zone = *alternate.Zone.Name
		updated.Spec.PlacementStrategy = nil
		t.Logf("Changing subnet placement from %s to %s in %s", storedSubnet, updated.Spec.Subnet, updated.Spec.Zone)
		initialUIDs, snapshotErr := driftClaimUIDs(ctx, suite.kubeClient, testName, nodePool.Name)
		require.NoError(t, snapshotErr, "capture all NodeClaims before subnet mutation")
		require.Contains(t, initialUIDs, originalNC.UID)
		require.NoError(t, suite.kubeClient.Update(ctx, updated), "update NodeClass to a real alternate subnet")
		suite.waitForDriftNodeClassReady(t, nodeClass.Name)

		// Step 7: Wait for NodeClaim to be marked Drifted
		waitErr = wait.PollUntilContextTimeout(ctx, 10*time.Second, testTimeout, true,
			func(ctx context.Context) (bool, error) {
				var nc karpv1.NodeClaim
				if getErr := suite.kubeClient.Get(ctx, types.NamespacedName{Name: originalName}, &nc); getErr != nil {
					return false, getErr
				}
				for _, cond := range nc.Status.Conditions {
					if cond.Type == "Drifted" && cond.Status == metav1.ConditionTrue {
						t.Logf("OK: NodeClaim %s marked Drifted (Reason=%s)", nc.Name, cond.Reason)
						return true, nil
					}
				}
				t.Logf("Waiting: NodeClaim %s not yet marked Drifted", originalName)
				return false, nil
			})
		require.NoError(t, waitErr, "NodeClaim should become drifted after subnet placement changes")

		// Step 8: Wait for replacement NodeClaim
		var replacementName string
		var replacementUID types.UID
		waitErr = wait.PollUntilContextTimeout(ctx, 10*time.Second, testTimeout, true,
			func(ctx context.Context) (bool, error) {
				replacement, findErr := suite.findDriftReplacement(ctx, testName, nodePool.Name, initialUIDs, driftReplacementPlacement{Subnet: *alternate.ID, Zone: *alternate.Zone.Name})
				if findErr != nil {
					return false, findErr
				}
				if replacement != nil {
					replacementName = replacement.Name
					replacementUID = replacement.UID
					t.Logf("OK: Found replacement NodeClaim %s", replacementName)
					return true, nil
				}
				t.Logf("Waiting: No ready replacement NodeClaim found yet")
				return false, nil
			})
		require.NoError(t, waitErr, "Replacement NodeClaim should become ready")
		var replacementNC karpv1.NodeClaim
		require.NoError(t, suite.kubeClient.Get(ctx, types.NamespacedName{Name: replacementName}, &replacementNC))
		require.Equal(t, replacementUID, replacementNC.UID)
		require.NotContains(t, initialUIDs, replacementNC.UID)
		require.True(t, suite.isNodeClaimReady(replacementNC))
		require.Equal(t, *alternate.ID, replacementNC.Annotations[v1alpha1.AnnotationIBMNodeClaimSubnetID])
		require.Equal(t, *alternate.Zone.Name, replacementNC.Labels[corev1.LabelTopologyZone])
		vpc, err := suite.vpcClient()
		require.NoError(t, err)
		instance, err := vpc.GetInstance(ctx, vpcInstanceID(replacementNC.Status.ProviderID))
		require.NoError(t, err, "replacement instance must exist in IBM Cloud")
		require.Equal(t, *alternate.ID, instanceSubnetID(instance), "IBM Cloud must report the replacement on the alternate subnet")
		require.Equal(t, *alternate.Zone.Name, *instance.Zone.Name)

		// Step 9: Verify original NodeClaim is deleted or being deleted
		waitErr = wait.PollUntilContextTimeout(ctx, 10*time.Second, testTimeout, true,
			func(ctx context.Context) (bool, error) {
				return nodeClaimDeletingOrAbsent(ctx, suite.kubeClient, originalName)
			})
		require.NoError(t, waitErr, "Original NodeClaim should be deleted after replacement")

		t.Logf("OK: Subnet drift test completed successfully")
	})
}

func TestE2ESecurityGroupDrift_DetectedAndReplaced(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	testName := fmt.Sprintf("sg-drift-%d", time.Now().Unix())
	t.Logf("Starting security group drift test: %s", testName)

	suite.WithAutoCleanup(t, testName, func() {
		ctx := t.Context()
		defaultGroup := suite.readDriftDefaultSecurityGroup(t)
		if defaultGroup == suite.testSecurityGroup {
			t.Skip("the configured security group is already the VPC default; no additive drift fixture is available")
		}

		// Step 1: Create NodeClass with explicit security groups
		nodeClass := suite.createTestNodeClass(t, testName)
		suite.waitForNodeClassResolved(t, nodeClass.Name)
		require.NotEmpty(t, nodeClass.Spec.SecurityGroups, "NodeClass must have security groups")

		// Verify Status.ResolvedSecurityGroups mirrors spec.SecurityGroups
		var currentNodeClass v1alpha1.IBMNodeClass
		err := suite.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClass.Name}, &currentNodeClass)
		require.NoError(t, err)
		require.ElementsMatch(t, nodeClass.Spec.SecurityGroups, currentNodeClass.Status.ResolvedSecurityGroups)
		t.Logf("OK: NodeClass %s ready with security groups: %v", nodeClass.Name, nodeClass.Spec.SecurityGroups)

		// Step 2: Create NodePool and workload to trigger provisioning
		nodePool := suite.createDriftNodePool(t, testName, nodeClass.Name)
		t.Logf("OK: Created NodePool %s", nodePool.Name)

		deployment := suite.createTestWorkload(t, testName)
		suite.waitForPodsToBeScheduled(t, deployment.Name, deployment.Namespace)
		t.Logf("OK: Workload %s/%s scheduled", deployment.Namespace, deployment.Name)

		// Step 3: Capture the initial READY NodeClaim
		var originalNC karpv1.NodeClaim
		var originalName string
		waitErr := wait.PollUntilContextTimeout(ctx, 10*time.Second, testTimeout, true,
			func(ctx context.Context) (bool, error) {
				var nodeClaimList karpv1.NodeClaimList
				listErr := suite.kubeClient.List(ctx, &nodeClaimList, client.MatchingLabels{
					"test-name": testName,
				})
				if listErr != nil {
					t.Logf("Error: Failed to list NodeClaims: %v", listErr)
					return false, listErr
				}
				if len(nodeClaimList.Items) == 0 {
					t.Logf("Waiting: No NodeClaims found yet for test %s", testName)
					return false, nil
				}
				for _, nc := range nodeClaimList.Items {
					if nc.Labels[karpv1.NodePoolLabelKey] == nodePool.Name && suite.isNodeClaimReady(nc) {
						originalNC = nc
						originalName = nc.Name
						t.Logf("OK: Selected READY NodeClaim %s as original", originalName)
						return true, nil
					}
				}
				t.Logf("Waiting: NodeClaims exist but none are READY yet")
				return false, nil
			})
		if waitErr != nil {
			suite.dumpNodeClassOnFailure(t, nodeClass.Name, "no READY NodeClaim for SG drift test")
			suite.logNodeClaimStatus(t)
			suite.logNodePoolStatus(t, nodePool.Name)
		}
		require.NoError(t, waitErr, "Should find a READY NodeClaim")

		// Step 4: Verify security group annotation was set by instance provider
		storedSGs := originalNC.Annotations[v1alpha1.AnnotationIBMNodeClaimSecurityGroups]
		require.NotEmpty(t, storedSGs, "NodeClaim must have security groups annotation")
		storedSGList := strings.Split(storedSGs, ",")
		require.ElementsMatch(t, nodeClass.Spec.SecurityGroups, storedSGList)
		t.Logf("OK: NodeClaim %s used security groups: %v", originalName, storedSGList)

		// Step 5: Modify NodeClass to use different security groups (this triggers hash drift)
		// Get fresh copy of NodeClass
		err = suite.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClass.Name}, &currentNodeClass)
		require.NoError(t, err, "Failed to get NodeClass for update")

		if slices.Contains(currentNodeClass.Spec.SecurityGroups, defaultGroup) {
			t.Skip("the NodeClass already includes the VPC default security group; no additive drift fixture is available")
		}
		updated := currentNodeClass.DeepCopy()
		updated.Spec.SecurityGroups = append(updated.Spec.SecurityGroups, defaultGroup)

		t.Logf("SecurityGroups changing from %v to %v", currentNodeClass.Spec.SecurityGroups, updated.Spec.SecurityGroups)
		initialUIDs, snapshotErr := driftClaimUIDs(ctx, suite.kubeClient, testName, nodePool.Name)
		require.NoError(t, snapshotErr, "capture all NodeClaims before security group mutation")
		require.Contains(t, initialUIDs, originalNC.UID)
		require.NoError(t, suite.kubeClient.Update(ctx, updated), "add the real default VPC security group")
		suite.waitForDriftNodeClassReady(t, nodeClass.Name)

		// Step 6: Wait for NodeClaim to be marked Drifted
		waitErr = wait.PollUntilContextTimeout(ctx, 10*time.Second, testTimeout, true,
			func(ctx context.Context) (bool, error) {
				var nc karpv1.NodeClaim
				if getErr := suite.kubeClient.Get(ctx, types.NamespacedName{Name: originalName}, &nc); getErr != nil {
					return false, getErr
				}
				for _, cond := range nc.Status.Conditions {
					if cond.Type == "Drifted" && cond.Status == metav1.ConditionTrue {
						t.Logf("OK: NodeClaim %s marked Drifted (Reason=%s)", nc.Name, cond.Reason)
						return true, nil
					}
				}
				t.Logf("Waiting: NodeClaim %s not yet marked Drifted", originalName)
				return false, nil
			})
		if waitErr != nil {
			suite.dumpNodeClassOnFailure(t, nodeClass.Name, "drift detection did not fire after SG change")
			suite.logNodeClaimStatus(t)
		}
		require.NoError(t, waitErr, "NodeClaim should become drifted after security groups change")

		// Step 7: Wait for replacement NodeClaim
		var replacementName string
		var replacementUID types.UID
		waitErr = wait.PollUntilContextTimeout(ctx, 10*time.Second, testTimeout, true,
			func(ctx context.Context) (bool, error) {
				replacement, findErr := suite.findDriftReplacement(ctx, testName, nodePool.Name, initialUIDs, driftReplacementPlacement{SecurityGroups: updated.Spec.SecurityGroups})
				if findErr != nil {
					return false, findErr
				}
				if replacement != nil {
					replacementName = replacement.Name
					replacementUID = replacement.UID
					t.Logf("OK: Found replacement NodeClaim %s", replacementName)
					return true, nil
				}
				t.Logf("Waiting: No ready replacement NodeClaim found yet")
				return false, nil
			})
		if waitErr != nil {
			suite.dumpNodeClassOnFailure(t, nodeClass.Name, "no replacement NodeClaim after SG drift")
			suite.logNodeClaimStatus(t)
			suite.logNodePoolStatus(t, nodePool.Name)
		}
		require.NoError(t, waitErr, "Replacement NodeClaim should become ready")

		// Step 8: Verify the replacement NodeClaim has the new security groups
		var replacementNC karpv1.NodeClaim
		err = suite.kubeClient.Get(ctx, types.NamespacedName{Name: replacementName}, &replacementNC)
		require.NoError(t, err, "Failed to get replacement NodeClaim")
		require.Equal(t, replacementUID, replacementNC.UID)
		require.NotContains(t, initialUIDs, replacementNC.UID)
		require.True(t, suite.isNodeClaimReady(replacementNC))

		replacementSGs := replacementNC.Annotations[v1alpha1.AnnotationIBMNodeClaimSecurityGroups]
		require.NotEmpty(t, replacementSGs, "Replacement NodeClaim must have security groups annotation")
		require.ElementsMatch(t, updated.Spec.SecurityGroups, strings.Split(replacementSGs, ","))
		t.Logf("OK: Replacement NodeClaim %s has security groups: %s", replacementName, replacementSGs)

		// Step 9: Verify original NodeClaim is deleted or being deleted
		waitErr = wait.PollUntilContextTimeout(ctx, 10*time.Second, testTimeout, true,
			func(ctx context.Context) (bool, error) {
				return nodeClaimDeletingOrAbsent(ctx, suite.kubeClient, originalName)
			})
		require.NoError(t, waitErr, "Original NodeClaim should be deleted after replacement")

		t.Logf("OK: Security group drift test completed successfully")
	})
}

// The default group can prevent bootstrap, so verify attachment before node registration.
func TestE2ESecurityGroupDrift_DefaultSecurityGroup(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	testName := fmt.Sprintf("sg-drift-default-%d", time.Now().Unix())
	suite.WithAutoCleanup(t, testName, func() {
		ctx := t.Context()
		defaultGroup := suite.readDriftDefaultSecurityGroup(t)
		nodeClass := suite.createTestNodeClassWithoutSecurityGroups(t, testName)
		suite.waitForNodeClassResolved(t, nodeClass.Name)
		require.Empty(t, nodeClass.Spec.SecurityGroups)
		var currentClass v1alpha1.IBMNodeClass
		require.NoError(t, suite.kubeClient.Get(ctx, client.ObjectKeyFromObject(nodeClass), &currentClass))
		require.Equal(t, nodeClass.UID, currentClass.UID)
		require.ElementsMatch(t, []string{defaultGroup}, currentClass.Status.ResolvedSecurityGroups)
		pool := suite.createDriftNodePool(t, testName, nodeClass.Name)
		suite.createTestWorkload(t, testName)
		var original karpv1.NodeClaim
		require.NoError(t, wait.PollUntilContextTimeout(ctx, pollInterval, testTimeout, true, func(ctx context.Context) (bool, error) {
			var claims karpv1.NodeClaimList
			if err := suite.kubeClient.List(ctx, &claims, client.MatchingLabels{"test-name": testName, karpv1.NodePoolLabelKey: pool.Name}); err != nil {
				return false, err
			}
			for _, claim := range claims.Items {
				owned := slices.ContainsFunc(claim.OwnerReferences, func(owner metav1.OwnerReference) bool {
					return owner.Kind == "NodePool" && owner.UID == pool.UID
				})
				if owned && claim.Spec.NodeClassRef != nil && claim.Spec.NodeClassRef.Name == nodeClass.Name &&
					claim.DeletionTimestamp.IsZero() && claim.StatusConditions().Get(karpv1.ConditionTypeLaunched).IsTrue() {
					original = claim
					return true, nil
				}
			}
			return false, nil
		}), "Default-group test must own a Launched allocation")
		require.NotEmpty(t, original.UID)
		require.ElementsMatch(t, []string{defaultGroup}, strings.Split(original.Annotations[v1alpha1.AnnotationIBMNodeClaimSecurityGroups], ","))
		network, err := suite.vpcClient()
		require.NoError(t, err)
		instanceID := vpcInstanceID(original.Status.ProviderID)
		require.NotEmpty(t, instanceID)
		instance, err := network.GetInstance(ctx, instanceID)
		require.NoError(t, err)
		suite.verifyOwnedTestInstance(t, instance, &original, nodeClass)
		require.NotNil(t, instance.ID)
		require.Equal(t, instanceID, *instance.ID)
		require.NotNil(t, instance.PrimaryNetworkInterface)
		require.NotNil(t, instance.PrimaryNetworkInterface.ID)
		nic, _, err := network.GetSDKClient().GetInstanceNetworkInterfaceWithContext(ctx, &vpcv1.GetInstanceNetworkInterfaceOptions{
			InstanceID: &instanceID, ID: instance.PrimaryNetworkInterface.ID,
		})
		require.NoError(t, err, "Read the actual owned VM attachment")
		require.NotNil(t, nic)
		require.NotNil(t, nic.ID)
		require.Equal(t, *instance.PrimaryNetworkInterface.ID, *nic.ID)
		groups := make([]string, 0, len(nic.SecurityGroups))
		for _, group := range nic.SecurityGroups {
			require.NotNil(t, group.ID)
			groups = append(groups, *group.ID)
		}
		require.ElementsMatch(t, []string{defaultGroup}, groups, "Actual attachment must contain exactly the resolved default group")
		until := time.Now().Add(30 * time.Second)
		require.NoError(t, wait.PollUntilContextTimeout(ctx, pollInterval, time.Minute, true, func(ctx context.Context) (bool, error) {
			var current karpv1.NodeClaim
			if err := suite.kubeClient.Get(ctx, client.ObjectKeyFromObject(&original), &current); err != nil {
				return false, err
			}
			if current.UID != original.UID || !current.DeletionTimestamp.IsZero() || current.Status.ProviderID != original.Status.ProviderID {
				return false, fmt.Errorf("default-group claim identity changed during observation")
			}
			if current.StatusConditions().Get(karpv1.ConditionTypeDrifted).IsTrue() {
				return false, fmt.Errorf("default-group claim unexpectedly drifted")
			}
			return !time.Now().Before(until), nil
		}))
		t.Logf("Verified default-group selection and actual NIC attachment; no policy change or bootstrap assertion")
	})
}

func (s *E2ETestSuite) createDriftNodePool(t *testing.T, testName, nodeClassName string) *karpv1.NodePool {
	t.Helper()
	pool := s.createTestNodePoolObject(t, testName, nodeClassName)
	pool.Spec.Template.Labels = map[string]string{"test": "e2e", "test-name": testName}
	pool.Spec.Template.Spec.ExpireAfter = karpv1.MustParseNillableDuration("Never")
	pool.Spec.Disruption.ConsolidateAfter = karpv1.MustParseNillableDuration("Never")
	require.NoError(t, s.kubeClient.Create(t.Context(), pool))
	return pool
}

func (s *E2ETestSuite) waitForDriftNodeClassReady(t *testing.T, name string) {
	t.Helper()
	err := wait.PollUntilContextTimeout(t.Context(), pollInterval, testTimeout, true, func(ctx context.Context) (bool, error) {
		class := &v1alpha1.IBMNodeClass{}
		if getErr := s.kubeClient.Get(ctx, types.NamespacedName{Name: name}, class); getErr != nil {
			return false, getErr
		}
		for _, condition := range class.Status.Conditions {
			if condition.Type == "Ready" && condition.Status == metav1.ConditionTrue && condition.ObservedGeneration == class.Generation {
				return true, nil
			}
		}
		return false, nil
	})
	require.NoError(t, err, "updated NodeClass must be ready at its current generation")
}

func nodeClaimDeletingOrAbsent(ctx context.Context, reader client.Reader, name string) (bool, error) {
	claim := &karpv1.NodeClaim{}
	err := reader.Get(ctx, types.NamespacedName{Name: name}, claim)
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return claim.DeletionTimestamp != nil, nil
}

func driftClaimUIDs(ctx context.Context, reader client.Reader, testName, poolName string) (map[types.UID]struct{}, error) {
	claims := &karpv1.NodeClaimList{}
	if err := reader.List(ctx, claims, client.MatchingLabels{"test-name": testName, karpv1.NodePoolLabelKey: poolName}); err != nil {
		return nil, err
	}
	uids := make(map[types.UID]struct{}, len(claims.Items))
	for _, claim := range claims.Items {
		if claim.UID == "" {
			return nil, fmt.Errorf("initial NodeClaim %s has no UID", claim.Name)
		}
		uids[claim.UID] = struct{}{}
	}
	return uids, nil
}

type driftReplacementPlacement struct {
	Subnet, Zone   string
	SecurityGroups []string
}

func (s *E2ETestSuite) findDriftReplacement(ctx context.Context, testName, poolName string, initialUIDs map[types.UID]struct{}, desired driftReplacementPlacement) (*karpv1.NodeClaim, error) {
	claims := &karpv1.NodeClaimList{}
	if err := s.kubeClient.List(ctx, claims, client.MatchingLabels{"test-name": testName, karpv1.NodePoolLabelKey: poolName}); err != nil {
		return nil, err
	}
	expectedGroups := slices.Clone(desired.SecurityGroups)
	slices.Sort(expectedGroups)
	for i := range claims.Items {
		claim := &claims.Items[i]
		if _, existed := initialUIDs[claim.UID]; existed || claim.UID == "" || !s.isNodeClaimReady(*claim) {
			continue
		}
		if desired.Subnet != "" && claim.Annotations[v1alpha1.AnnotationIBMNodeClaimSubnetID] != desired.Subnet {
			continue
		}
		if desired.Zone != "" && claim.Labels[corev1.LabelTopologyZone] != desired.Zone {
			continue
		}
		if desired.SecurityGroups != nil {
			actualGroups := strings.Split(claim.Annotations[v1alpha1.AnnotationIBMNodeClaimSecurityGroups], ",")
			slices.Sort(actualGroups)
			if !slices.Equal(expectedGroups, actualGroups) {
				continue
			}
		}
		return claim, nil
	}
	return nil, nil
}

type driftNetworkReader interface {
	GetSubnet(context.Context, string) (*vpcv1.Subnet, error)
	GetVPC(context.Context, string, string) (*vpcv1.VPC, error)
}

func (s *E2ETestSuite) readDriftDefaultSecurityGroup(t *testing.T) string {
	t.Helper()
	reader, err := s.vpcClient()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	group, err := defaultDriftSecurityGroup(ctx, reader, s.testVPC, s.testResourceGroup)
	require.NoError(t, err, "resolve the real default VPC security group")
	return group
}

func alternateDriftSubnet(ctx context.Context, reader driftNetworkReader, configured, used, vpcID, region string, pool *karpv1.NodePool) (*vpcv1.Subnet, error) {
	for _, entry := range strings.Split(configured, ",") {
		zone, id, valid := strings.Cut(strings.TrimSpace(entry), "=")
		zone, id = strings.TrimSpace(zone), strings.TrimSpace(id)
		if !valid || zone == "" || id == "" || strings.Contains(id, "=") || !strings.HasPrefix(zone, region+"-") {
			return nil, fmt.Errorf("TEST_SUBNETS must contain region-matching zone=subnet entries")
		}
		if id == used || !driftPoolAllowsZone(pool, zone) {
			continue
		}
		subnet, err := reader.GetSubnet(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("reading alternate drift subnet: %w", err)
		}
		if subnet == nil || subnet.ID == nil || *subnet.ID != id || subnet.VPC == nil || subnet.VPC.ID == nil || *subnet.VPC.ID != vpcID || subnet.Zone == nil || subnet.Zone.Name == nil || *subnet.Zone.Name != zone {
			return nil, fmt.Errorf("alternate drift subnet does not match its configured ID, VPC, and zone")
		}
		if subnet.Status == nil || *subnet.Status != vpcv1.SubnetStatusAvailableConst || subnet.AvailableIpv4AddressCount == nil || *subnet.AvailableIpv4AddressCount < 1 {
			continue
		}
		return subnet, nil
	}
	return nil, nil
}

func driftPoolAllowsZone(pool *karpv1.NodePool, zone string) bool {
	for _, requirement := range pool.Spec.Template.Spec.Requirements {
		if requirement.Key != corev1.LabelTopologyZone {
			continue
		}
		switch requirement.Operator {
		case corev1.NodeSelectorOpIn:
			if !slices.Contains(requirement.Values, zone) {
				return false
			}
		case corev1.NodeSelectorOpNotIn:
			if slices.Contains(requirement.Values, zone) {
				return false
			}
		case corev1.NodeSelectorOpExists:
		default:
			return false
		}
	}
	return true
}

func defaultDriftSecurityGroup(ctx context.Context, reader driftNetworkReader, vpcID, resourceGroup string) (string, error) {
	vpc, err := reader.GetVPC(ctx, vpcID, resourceGroup)
	if err != nil {
		return "", fmt.Errorf("reading drift fixture VPC: %w", err)
	}
	if vpc == nil || vpc.ID == nil || *vpc.ID != vpcID || vpc.DefaultSecurityGroup == nil || vpc.DefaultSecurityGroup.ID == nil || *vpc.DefaultSecurityGroup.ID == "" {
		return "", fmt.Errorf("drift fixture VPC has no confirmed default security group")
	}
	return *vpc.DefaultSecurityGroup.ID, nil
}

func instanceSubnetID(instance *vpcv1.Instance) string {
	if instance.PrimaryNetworkAttachment != nil && instance.PrimaryNetworkAttachment.Subnet != nil && instance.PrimaryNetworkAttachment.Subnet.ID != nil {
		return *instance.PrimaryNetworkAttachment.Subnet.ID
	}
	if instance.PrimaryNetworkInterface != nil && instance.PrimaryNetworkInterface.Subnet != nil && instance.PrimaryNetworkInterface.Subnet.ID != nil {
		return *instance.PrimaryNetworkInterface.Subnet.ID
	}
	return ""
}

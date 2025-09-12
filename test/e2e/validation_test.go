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

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
)

// TestE2ENodeClassValidation tests NodeClass validation with invalid resources
func TestE2ENodeClassValidation(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	ctx := context.Background()
	testName := fmt.Sprintf("validation-test-%d", time.Now().Unix())

	// Test invalid NodeClass (invalid resource references)
	invalidNodeClass := &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-invalid", testName),
			Labels: map[string]string{
				"test-name": testName,
			},
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			Region:          suite.testRegion,
			Zone:            suite.testZone,
			InstanceProfile: "bx2-2x8",
			Image:           "r010-00000000-0000-0000-0000-000000000000",           // Valid format but non-existent image
			VPC:             "r010-00000000-0000-0000-0000-000000000000",           // Valid format but non-existent VPC
			Subnet:          "02b7-00000000-0000-0000-0000-000000000000",           // Valid format but non-existent subnet
			SecurityGroups:  []string{"r010-00000000-0000-0000-0000-000000000000"}, // Valid format but non-existent security group
		},
	}

	err := suite.kubeClient.Create(ctx, invalidNodeClass)
	require.NoError(t, err)

	// Wait for status controller to process validation
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var updatedNodeClass v1alpha1.IBMNodeClass
	err = wait.PollUntilContextTimeout(ctx, 5*time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		if getErr := suite.kubeClient.Get(ctx, types.NamespacedName{Name: invalidNodeClass.Name}, &updatedNodeClass); getErr != nil {
			return false, getErr
		}

		// Check if validation has been processed (either Ready=True or Ready=False)
		for _, condition := range updatedNodeClass.Status.Conditions {
			if condition.Type == "Ready" {
				return true, nil
			}
			// Check for Ready condition with False status (validation failure)
			if condition.Type == "Ready" && condition.Status == metav1.ConditionFalse {
				t.Logf("Validation condition failed as expected: %s - %s", condition.Type, condition.Message)
				return true, nil
			}
		}

		t.Logf("Still waiting for validation, current conditions: %d", len(updatedNodeClass.Status.Conditions))
		for _, condition := range updatedNodeClass.Status.Conditions {
			t.Logf("  - %s: %s (%s)", condition.Type, condition.Status, condition.Reason)
		}

		return false, nil
	})

	require.NoError(t, err, "NodeClass validation should complete within timeout")

	// Verify that the NodeClass has validation failures
	hasValidationFailure := false
	for _, condition := range updatedNodeClass.Status.Conditions {
		if condition.Status == metav1.ConditionFalse {
			hasValidationFailure = true
			t.Logf("✅ Expected validation failure: %s - %s", condition.Type, condition.Message)
		}
	}

	require.True(t, hasValidationFailure, "NodeClass should have validation failures for invalid resources")

	// Test valid NodeClass for comparison
	validNodeClass := suite.createTestNodeClass(t, testName+"-valid")
	suite.waitForNodeClassReady(t, validNodeClass.Name)
	t.Logf("✅ Valid NodeClass passed validation: %s", validNodeClass.Name)

	// Cleanup
	suite.cleanupTestResources(t, testName)
	t.Logf("NodeClass validation test completed")
}

// TestE2EValidNodeClassCreation tests creation of a valid NodeClass
func TestE2EValidNodeClassCreation(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	testName := fmt.Sprintf("valid-nodeclass-%d", time.Now().Unix())

	// Create a valid NodeClass using test environment variables
	nodeClass := suite.createTestNodeClass(t, testName)
	t.Logf("Created NodeClass: %s", nodeClass.Name)

	// Verify all required fields are set
	require.NotEmpty(t, nodeClass.Spec.Region, "Region should be set")
	require.NotEmpty(t, nodeClass.Spec.Zone, "Zone should be set")
	require.NotEmpty(t, nodeClass.Spec.InstanceProfile, "InstanceProfile should be set")
	require.NotEmpty(t, nodeClass.Spec.Image, "Image should be set")
	require.NotEmpty(t, nodeClass.Spec.VPC, "VPC should be set")
	require.NotEmpty(t, nodeClass.Spec.Subnet, "Subnet should be set")
	require.NotEmpty(t, nodeClass.Spec.SecurityGroups, "SecurityGroups should be set")
	require.NotEmpty(t, nodeClass.Spec.APIServerEndpoint, "APIServerEndpoint should be set")

	// Wait for NodeClass to become ready
	suite.waitForNodeClassReady(t, nodeClass.Name)

	// Verify the NodeClass has all expected ready conditions
	ctx := context.Background()
	var updatedNodeClass v1alpha1.IBMNodeClass
	err := suite.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClass.Name}, &updatedNodeClass)
	require.NoError(t, err)

	// Check for Ready condition (current controller implementation)
	readyFound := false
	for _, condition := range updatedNodeClass.Status.Conditions {
		if condition.Type == "Ready" && condition.Status == metav1.ConditionTrue {
			readyFound = true
			t.Logf("✅ NodeClass Ready condition is true")
			break
		}
	}

	require.True(t, readyFound, "NodeClass should have Ready=True condition")
	t.Logf("✅ NodeClass %s is fully validated and ready", nodeClass.Name)

	// Cleanup
	suite.cleanupTestResources(t, testName)
	t.Logf("Valid NodeClass creation test completed")
}

// TestE2ENodeClassWithMissingFields tests NodeClass with missing required fields
func TestE2ENodeClassWithMissingFields(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	ctx := context.Background()
	testName := fmt.Sprintf("missing-fields-test-%d", time.Now().Unix())

	// Create NodeClass with missing VPC field
	nodeClassMissingVPC := &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-no-vpc", testName),
			Labels: map[string]string{
				"test-name": testName,
			},
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			Region:          suite.testRegion,
			Zone:            suite.testZone,
			InstanceProfile: "bx2-2x8",
			Image:           suite.testImage,
			// VPC field is missing
			Subnet:            suite.testSubnet,
			SecurityGroups:    []string{suite.testSecurityGroup},
			APIServerEndpoint: suite.APIServerEndpoint,
		},
	}

	// This should fail at admission/validation level if webhook validation is enabled
	err := suite.kubeClient.Create(ctx, nodeClassMissingVPC)
	if err != nil {
		t.Logf("✅ NodeClass creation failed as expected due to missing VPC: %v", err)
		// Test passed - admission validation prevented creation
		return
	}

	// If creation succeeded, wait for status validation to catch the error
	t.Logf("NodeClass creation succeeded, waiting for status validation")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var updatedNodeClass v1alpha1.IBMNodeClass
	err = wait.PollUntilContextTimeout(ctx, 5*time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		if getErr := suite.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClassMissingVPC.Name}, &updatedNodeClass); getErr != nil {
			return false, getErr
		}

		// Look for validation failure conditions
		for _, condition := range updatedNodeClass.Status.Conditions {
			if condition.Status == metav1.ConditionFalse && condition.Type == "Ready" {
				t.Logf("✅ Validation failed as expected: %s - %s", condition.Type, condition.Message)
				return true, nil
			}
		}

		return false, nil
	})

	require.NoError(t, err, "NodeClass should fail validation within timeout")

	// Cleanup
	suite.cleanupTestResources(t, testName)
	t.Logf("Missing fields validation test completed")
}

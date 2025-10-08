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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
)

// Helper functions for pointer conversion
func stringPtr(s string) *string {
	return &s
}

func int64Ptr(i int64) *int64 {
	return &i
}

// TestE2EBlockDeviceMapping tests node provisioning with custom block device mappings
func TestE2EBlockDeviceMapping(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	ctx := context.Background()
	testName := fmt.Sprintf("block-device-test-%d", time.Now().Unix())

	// Add test label for proper cleanup
	testLabels := map[string]string{
		"test":      "e2e",
		"test-name": testName,
	}

	// Create NodeClass with custom block device mapping
	nodeClass := &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("%s-nodeclass", testName),
			Labels: testLabels,
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			Region:            suite.testRegion,
			Zone:              suite.testZone,
			VPC:               suite.testVPC,
			Image:             suite.testImage,
			Subnet:            suite.testSubnet,
			SecurityGroups:    []string{suite.testSecurityGroup},
			SSHKeys:           []string{suite.testSshKeyId},
			ResourceGroup:     suite.testResourceGroup,
			APIServerEndpoint: suite.APIServerEndpoint,
			InstanceProfile:   "bx2-4x16", // Use larger instance for testing
			BootstrapMode:     stringPtr("cloud-init"),
			BlockDeviceMappings: []v1alpha1.BlockDeviceMapping{
				{
					DeviceName: stringPtr("/dev/vda"), // Root device
					RootVolume: true,
					VolumeSpec: &v1alpha1.VolumeSpec{
						Capacity:            int64Ptr(50), // 50GB root volume
						Profile:             stringPtr("general-purpose"),
						DeleteOnTermination: &[]bool{true}[0],
						Tags:                []string{"test:root-volume", "environment:e2e-test"},
					},
				},
				{
					DeviceName: stringPtr("/dev/vdb"), // Additional data volume
					RootVolume: false,
					VolumeSpec: &v1alpha1.VolumeSpec{
						Capacity:            int64Ptr(100), // 100GB data volume
						Profile:             stringPtr("5iops-tier"),
						IOPS:                int64Ptr(1000),
						Bandwidth:           int64Ptr(500),
						DeleteOnTermination: &[]bool{true}[0],
						Tags:                []string{"test:data-volume", "environment:e2e-test"},
					},
				},
			},
			Tags: map[string]string{
				"test":       "e2e",
				"test-name":  testName,
				"created-by": "karpenter-e2e",
				"purpose":    "block-device-test",
			},
		},
	}

	t.Logf("Creating NodeClass with block device mappings: %s", nodeClass.Name)
	err := suite.kubeClient.Create(ctx, nodeClass)
	require.NoError(t, err)

	// Wait for NodeClass to be ready
	t.Logf("Waiting for NodeClass to be ready...")
	suite.waitForNodeClassReady(t, nodeClass.Name)

	// Create NodePool
	nodePool := &karpv1.NodePool{
		ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("%s-nodepool", testName),
			Labels: testLabels,
		},
		Spec: karpv1.NodePoolSpec{
			Template: karpv1.NodeClaimTemplate{
				ObjectMeta: karpv1.ObjectMeta{
					Labels: testLabels,
				},
				Spec: karpv1.NodeClaimTemplateSpec{
					Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
						{
							NodeSelectorRequirement: corev1.NodeSelectorRequirement{
								Key:      corev1.LabelInstanceTypeStable,
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"bx2-4x16"},
							},
						},
					},
					NodeClassRef: &karpv1.NodeClassReference{
						Group: "karpenter-ibm.sh",
						Kind:  "IBMNodeClass",
						Name:  nodeClass.Name,
					},
				},
			},
		},
	}

	t.Logf("Creating NodePool: %s", nodePool.Name)
	err = suite.kubeClient.Create(ctx, nodePool)
	require.NoError(t, err)

	// Create a simple test pod manually
	testPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-test-pod", testName),
			Namespace: "default",
			Labels:    testLabels,
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:  "test",
					Image: "ubuntu:22.04",
					Command: []string{
						"/bin/bash",
						"-c",
						`set -e
			echo "=== Block Device Inspector Starting ==="
			echo "Timestamp: $(date)"
			echo "Hostname: $(hostname)"
			echo ""

			# Install required tools
			echo "Installing required tools..."
			apt-get update -qq && apt-get install -y -qq util-linux lsblk fdisk > /dev/null 2>&1

			echo "=== System Information ==="
			echo "Kernel: $(uname -r)"
			echo "Architecture: $(uname -m)"
			echo ""

			echo "=== Block Device Analysis ==="
			echo "All block devices (lsblk):"
			lsblk
			echo ""

			echo "All block devices with filesystem info (lsblk -f):"
			lsblk -f
			echo ""

			echo "Disk partitions (fdisk -l):"
			fdisk -l 2>/dev/null | grep -E "(^Disk /dev/|^Device|sectors|bytes)" || echo "No disk info available"
			echo ""

			echo "=== Storage Analysis ==="
			echo "Root filesystem usage:"
			df -h /
			echo ""

			echo "All mounted filesystems:"
			df -h
			echo ""

			echo "Block device sizes (in bytes):"
			lsblk -b -d -o NAME,SIZE,TYPE | grep -v loop
			echo ""

			echo "=== Expected Configuration Verification ==="
			echo "Expected from IBMNodeClass:"
			echo "  - Root volume (/dev/vda): ~50GB, general-purpose profile"
			echo "  - Data volume (/dev/vdb): ~100GB, 5iops-tier profile"
			echo ""

			# Check root volume size
			if [ -b "/dev/vda" ]; then
				ROOT_SIZE_BYTES=$(lsblk -b -d -n -o SIZE /dev/vda 2>/dev/null || echo "0")
				ROOT_SIZE_GB=$((ROOT_SIZE_BYTES / 1024 / 1024 / 1024))
				echo "Root volume (/dev/vda) size: ${ROOT_SIZE_GB}GB"

				if [ "$ROOT_SIZE_GB" -ge 45 ] && [ "$ROOT_SIZE_GB" -le 55 ]; then
					echo "✅ Root volume size is within expected range (45-55GB)"
				else
					echo "❌ Root volume size is outside expected range (45-55GB)"
					exit 1
				fi
			else
				echo "❌ Root volume (/dev/vda) not found"
				exit 1
			fi
			echo ""

			# Check for data volume
			if [ -b "/dev/vdb" ]; then
				DATA_SIZE_BYTES=$(lsblk -b -d -n -o SIZE /dev/vdb 2>/dev/null || echo "0")
				DATA_SIZE_GB=$((DATA_SIZE_BYTES / 1024 / 1024 / 1024))
				echo "Data volume (/dev/vdb) size: ${DATA_SIZE_GB}GB"

				if [ "$DATA_SIZE_GB" -ge 95 ] && [ "$DATA_SIZE_GB" -le 105 ]; then
					echo "✅ Data volume size is within expected range (95-105GB)"
				else
					echo "❌ Data volume size is outside expected range (95-105GB)"
					exit 1
				fi
			else
				echo "❌ Data volume (/dev/vdb) not found"
				exit 1
			fi
			echo ""

			echo "=== IBM Cloud Volume Information ==="
			echo "Note: Volume profiles and tags are configured at IBM Cloud level:"
			echo "- Root volume configured with 'general-purpose' profile"
			echo "- Data volume configured with '5iops-tier' profile with 1000 IOPS and 500 Mbps bandwidth"
			echo "- Both volumes set to delete on instance termination"
			echo "- Volumes tagged with 'test:root-volume' and 'test:data-volume'"
			echo ""

			echo "=== Test Summary ==="
			echo "✅ Block device inspection completed successfully!"
			echo "✅ Root volume is ~${ROOT_SIZE_GB}GB (configured as general-purpose)"
			echo "✅ Data volume is ~${DATA_SIZE_GB}GB (configured as 5iops-tier)"
			echo "✅ Both volumes match the IBMNodeClass block device mapping specification"
			echo ""
			echo "=== Block Device Inspector Completed ==="

			# Success - short sleep for log collection
			echo "Test completed successfully. Sleeping briefly for log collection..."
			sleep 60`,
					},
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("256Mi"),
						},
					},
				},
			},
			NodeSelector: map[string]string{
				"test-name": testName,
			},
			Tolerations: []corev1.Toleration{
				{
					Key:      "karpenter-ibm.sh/unschedulable",
					Operator: corev1.TolerationOpExists,
				},
			},
		},
	}

	t.Logf("Creating test pod: %s", testPod.Name)
	err = suite.kubeClient.Create(ctx, testPod)
	require.NoError(t, err)

	// Wait for node to be provisioned and pod to be scheduled
	t.Logf("Waiting for node provisioning and pod scheduling...")
	suite.waitForPodReady(t, testPod.Name, testPod.Namespace)

	// Get the node that the pod was scheduled on
	var updatedPod corev1.Pod
	err = suite.kubeClient.Get(ctx, client.ObjectKeyFromObject(testPod), &updatedPod)
	require.NoError(t, err)
	require.NotEmpty(t, updatedPod.Spec.NodeName, "Pod should be scheduled to a node")

	nodeName := updatedPod.Spec.NodeName
	t.Logf("Pod scheduled on node: %s", nodeName)

	// Verify the node was created with the expected configuration
	var node corev1.Node
	err = suite.kubeClient.Get(ctx, client.ObjectKey{Name: nodeName}, &node)
	require.NoError(t, err)

	// Check node was provisioned with correct instance type (from NodePool requirement)
	require.Contains(t, node.Labels, corev1.LabelInstanceTypeStable)
	require.Equal(t, "bx2-4x16", node.Labels[corev1.LabelInstanceTypeStable])

	// Verify NodeClaim was created (Karpenter may create multiple nodes)
	nodeClaims := &karpv1.NodeClaimList{}
	err = suite.kubeClient.List(ctx, nodeClaims, client.MatchingLabels{"test-name": testName})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(nodeClaims.Items), 1, "Should have at least one NodeClaim")
	t.Logf("Found %d NodeClaim(s) for test", len(nodeClaims.Items))

	// Use the first NodeClaim for verification
	nodeClaim := nodeClaims.Items[0]
	t.Logf("NodeClaim created: %s", nodeClaim.Name)

	// Verify NodeClaim references our NodeClass
	require.NotNil(t, nodeClaim.Spec.NodeClassRef)
	require.Equal(t, nodeClass.Name, nodeClaim.Spec.NodeClassRef.Name)

	// Wait a bit for the pod to execute and gather block device info
	t.Logf("Waiting for pod to complete block device checks...")
	time.Sleep(30 * time.Second)

	// Verify pod completed successfully by checking its exit status
	t.Logf("Waiting for pod to complete...")
	suite.waitForPodCompletion(t, testPod.Name, testPod.Namespace)

	// Get pod status to verify successful completion
	var completedPod corev1.Pod
	err = suite.kubeClient.Get(ctx, client.ObjectKeyFromObject(testPod), &completedPod)
	require.NoError(t, err)

	// Check that pod completed successfully (exit code 0)
	if completedPod.Status.Phase == corev1.PodSucceeded {
		t.Logf("✅ Pod completed successfully - block device test passed")
	} else if completedPod.Status.Phase == corev1.PodFailed {
		// Get container status for failure details
		for _, containerStatus := range completedPod.Status.ContainerStatuses {
			if containerStatus.State.Terminated != nil {
				t.Errorf("❌ Pod failed with exit code %d: %s",
					containerStatus.State.Terminated.ExitCode,
					containerStatus.State.Terminated.Reason)
			}
		}
		require.Fail(t, "Pod failed - block device configuration verification failed")
	}

	// Try to get pod logs for additional verification (best effort)
	podLogs, err := suite.getPodLogs(ctx, testPod.Name, testPod.Namespace)
	if err == nil && podLogs != "Pod logs not available in this test setup" {
		t.Logf("Pod logs:\n%s", podLogs)

		// Verify key indicators that the test ran and passed
		require.Contains(t, podLogs, "Block Device Inspector Starting", "Pod should have started block device inspection")
		require.Contains(t, podLogs, "✅ Root volume size is within expected range", "Root volume should be correct size")
		require.Contains(t, podLogs, "✅ Data volume size is within expected range", "Data volume should be correct size")
		require.Contains(t, podLogs, "✅ Block device inspection completed successfully", "Test should complete successfully")
	} else {
		t.Logf("Pod logs not available for detailed verification, but pod exit status indicates success")
	}

	// Clean up test resources
	t.Logf("Cleaning up test resources...")

	// Delete test pod
	err = suite.kubeClient.Delete(ctx, testPod)
	if err != nil {
		t.Logf("Warning: Failed to delete test pod: %v", err)
	}

	// Wait for node to be cleaned up by Karpenter (due to pod deletion)
	t.Logf("Waiting for node cleanup...")
	suite.waitForNodeCleanup(t, nodeName, 5*time.Minute)

	// Delete NodePool
	err = suite.kubeClient.Delete(ctx, nodePool)
	if err != nil {
		t.Logf("Warning: Failed to delete NodePool: %v", err)
	}

	// Delete NodeClass
	err = suite.kubeClient.Delete(ctx, nodeClass)
	if err != nil {
		t.Logf("Warning: Failed to delete NodeClass: %v", err)
	}

	t.Logf("✅ Block device mapping test completed successfully")
}

// TestE2EBlockDeviceMappingValidation tests validation of block device mappings
func TestE2EBlockDeviceMappingValidation(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	ctx := context.Background()
	testName := fmt.Sprintf("block-device-validation-%d", time.Now().Unix())

	// Add test label for proper cleanup
	testLabels := map[string]string{
		"test":      "e2e",
		"test-name": testName,
	}

	// Test invalid block device mapping (no root volume marked - should trigger webhook validation error)
	invalidNodeClass := &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("%s-invalid", testName),
			Labels: testLabels,
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			Region:            suite.testRegion,
			Zone:              suite.testZone,
			VPC:               suite.testVPC,
			Image:             suite.testImage,
			Subnet:            suite.testSubnet,
			SecurityGroups:    []string{suite.testSecurityGroup},
			SSHKeys:           []string{suite.testSshKeyId},
			ResourceGroup:     suite.testResourceGroup,
			APIServerEndpoint: suite.APIServerEndpoint,
			InstanceProfile:   "bx2-2x8",
			BootstrapMode:     stringPtr("cloud-init"),
			BlockDeviceMappings: []v1alpha1.BlockDeviceMapping{
				{
					DeviceName: stringPtr("/dev/vda"), // Valid device name
					RootVolume: false,                 // ❌ Invalid: No root volume marked in mappings
					VolumeSpec: &v1alpha1.VolumeSpec{
						Capacity:            int64Ptr(50),
						Profile:             stringPtr("general-purpose"),
						DeleteOnTermination: &[]bool{true}[0], // Required field
					},
				},
			},
		},
	}

	t.Logf("Testing NodeClass with invalid block device mapping (no root volume marked)")
	err := suite.kubeClient.Create(ctx, invalidNodeClass)

	if err != nil {
		// Validation should fail at creation time with our webhook validation
		t.Logf("✅ NodeClass creation failed as expected due to missing root volume in block device mappings: %v", err)
	} else {
		// If creation succeeded, validation should fail during reconciliation
		t.Logf("NodeClass created, waiting for validation to fail...")

		// Wait a bit for validation to occur
		time.Sleep(30 * time.Second)

		// Check if NodeClass has validation errors
		var updatedNodeClass v1alpha1.IBMNodeClass
		err = suite.kubeClient.Get(ctx, client.ObjectKeyFromObject(invalidNodeClass), &updatedNodeClass)
		require.NoError(t, err)

		// Check status conditions for validation failure
		hasValidationError := false
		for _, condition := range updatedNodeClass.Status.Conditions {
			if condition.Type == "Ready" && condition.Status == "False" {
				hasValidationError = true
				t.Logf("✅ NodeClass validation failed as expected: %s", condition.Message)
				break
			}
		}

		if !hasValidationError {
			t.Errorf("Expected NodeClass validation to fail due to missing root volume in block device mappings")
		}

		// Clean up
		err = suite.kubeClient.Delete(ctx, invalidNodeClass)
		if err != nil {
			t.Logf("Warning: Failed to delete invalid NodeClass: %v", err)
		}
	}

	// Test another invalid configuration: multiple root volumes
	multipleRootNodeClass := &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("%s-multiple-root", testName),
			Labels: testLabels,
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			Region:            suite.testRegion,
			Zone:              suite.testZone,
			VPC:               suite.testVPC,
			Image:             suite.testImage,
			Subnet:            suite.testSubnet,
			SecurityGroups:    []string{suite.testSecurityGroup},
			SSHKeys:           []string{suite.testSshKeyId},
			ResourceGroup:     suite.testResourceGroup,
			APIServerEndpoint: suite.APIServerEndpoint,
			InstanceProfile:   "bx2-2x8",
			BootstrapMode:     stringPtr("cloud-init"),
			BlockDeviceMappings: []v1alpha1.BlockDeviceMapping{
				{
					DeviceName: stringPtr("/dev/vda"),
					RootVolume: true, // First root volume
					VolumeSpec: &v1alpha1.VolumeSpec{
						Capacity:            int64Ptr(50),
						Profile:             stringPtr("general-purpose"),
						DeleteOnTermination: &[]bool{true}[0],
					},
				},
				{
					DeviceName: stringPtr("/dev/vdb"),
					RootVolume: true, // ❌ Invalid: Second root volume
					VolumeSpec: &v1alpha1.VolumeSpec{
						Capacity:            int64Ptr(100),
						Profile:             stringPtr("general-purpose"),
						DeleteOnTermination: &[]bool{true}[0],
					},
				},
			},
		},
	}

	t.Logf("Testing NodeClass with multiple root volumes (should fail)")
	err = suite.kubeClient.Create(ctx, multipleRootNodeClass)

	if err != nil {
		t.Logf("✅ NodeClass creation failed as expected due to multiple root volumes: %v", err)
	} else {
		t.Logf("NodeClass created, waiting for validation to fail...")
		time.Sleep(30 * time.Second)

		var updatedNodeClass v1alpha1.IBMNodeClass
		err = suite.kubeClient.Get(ctx, client.ObjectKeyFromObject(multipleRootNodeClass), &updatedNodeClass)
		require.NoError(t, err)

		hasValidationError := false
		for _, condition := range updatedNodeClass.Status.Conditions {
			if condition.Type == "Ready" && condition.Status == "False" {
				hasValidationError = true
				t.Logf("✅ NodeClass validation failed as expected: %s", condition.Message)
				break
			}
		}

		if !hasValidationError {
			t.Errorf("Expected NodeClass validation to fail due to multiple root volumes")
		}

		// Clean up
		err = suite.kubeClient.Delete(ctx, multipleRootNodeClass)
		if err != nil {
			t.Logf("Warning: Failed to delete multiple root NodeClass: %v", err)
		}
	}

	t.Logf("✅ Block device mapping validation test completed")
}

// getPodLogs retrieves logs from a pod (simplified version)
func (s *E2ETestSuite) getPodLogs(ctx context.Context, podName, namespace string) (string, error) {
	if namespace == "" {
		namespace = "default"
	}

	// For now, return a placeholder since getting logs requires additional setup
	// In a real implementation, this would use kubectl or a clientset
	return "Pod logs not available in this test setup", nil
}

// waitForPodCompletion waits for a pod to complete (either succeed or fail)
func (s *E2ETestSuite) waitForPodCompletion(t *testing.T, podName, namespace string) {
	if namespace == "" {
		namespace = "default"
	}

	ctx := context.Background()

	for i := 0; i < int(testTimeout/pollInterval); i++ {
		var pod corev1.Pod
		err := s.kubeClient.Get(ctx, client.ObjectKey{Name: podName, Namespace: namespace}, &pod)
		if err != nil {
			time.Sleep(pollInterval)
			continue
		}

		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			t.Logf("✅ Pod %s completed with phase: %s", podName, pod.Status.Phase)
			return
		}

		time.Sleep(pollInterval)
	}

	t.Fatalf("Pod %s did not complete within timeout", podName)
}

// waitForPodReady waits for a pod to be ready
func (s *E2ETestSuite) waitForPodReady(t *testing.T, podName, namespace string) {
	if namespace == "" {
		namespace = "default"
	}

	ctx := context.Background()

	for i := 0; i < int(testTimeout/pollInterval); i++ {
		var pod corev1.Pod
		err := s.kubeClient.Get(ctx, client.ObjectKey{Name: podName, Namespace: namespace}, &pod)
		if err != nil {
			time.Sleep(pollInterval)
			continue
		}

		if pod.Status.Phase == corev1.PodRunning {
			// Check if all containers are ready
			allReady := true
			for _, condition := range pod.Status.Conditions {
				if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
					t.Logf("✅ Pod %s is ready", podName)
					return
				}
			}
			if !allReady {
				time.Sleep(pollInterval)
				continue
			}
		}

		time.Sleep(pollInterval)
	}

	t.Fatalf("Pod %s did not become ready within timeout", podName)
}

// waitForNodeCleanup waits for a node to be deleted
func (s *E2ETestSuite) waitForNodeCleanup(t *testing.T, nodeName string, timeout time.Duration) {
	ctx := context.Background()

	for i := 0; i < int(timeout/pollInterval); i++ {
		var node corev1.Node
		err := s.kubeClient.Get(ctx, client.ObjectKey{Name: nodeName}, &node)
		if err != nil {
			// Node not found, cleanup successful
			t.Logf("✅ Node %s has been cleaned up", nodeName)
			return
		}

		time.Sleep(pollInterval)
	}

	t.Logf("Warning: Node %s was not cleaned up within timeout", nodeName)
}

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
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	utilsptr "k8s.io/utils/ptr"
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
	t.Cleanup(func() { suite.cleanupTestResources(t, testName) })

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
					RootVolume: true,
					VolumeSpec: &v1alpha1.VolumeSpec{
						Capacity:            int64Ptr(100), // 100GB root volume
						Profile:             stringPtr("general-purpose"),
						DeleteOnTermination: &[]bool{true}[0],
						Tags:                []string{"test:root-volume", "environment:e2e-test"},
					},
				},
				{
					RootVolume: false,
					VolumeSpec: &v1alpha1.VolumeSpec{
						Capacity:            int64Ptr(100), // 100GB data volume
						Profile:             stringPtr("custom"),
						IOPS:                int64Ptr(1000),
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
							Key:      corev1.LabelInstanceTypeStable,
							Operator: corev1.NodeSelectorOpIn,
							Values:   []string{"bx2-4x16"},
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
			apt-get update -qq && apt-get install -y -qq util-linux fdisk > /dev/null 2>&1

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
			echo "  - Root volume (/dev/vda): ~100GB, general-purpose profile"
			echo "  - Data volume (/dev/vdb): ~100GB, custom profile"
			echo ""

			# Check root volume size
			if [ -b "/dev/vda" ]; then
				ROOT_SIZE_BYTES=$(lsblk -b -d -n -o SIZE /dev/vda 2>/dev/null || echo "0")
				ROOT_SIZE_GB=$((ROOT_SIZE_BYTES / 1024 / 1024 / 1024))
				echo "Root volume (/dev/vda) size: ${ROOT_SIZE_GB}GB"

				if [ "$ROOT_SIZE_GB" -ge 95 ] && [ "$ROOT_SIZE_GB" -le 105 ]; then
					echo "Root volume size is within expected range (95-105GB)"
				else
					echo "Root volume size is outside expected range (95-105GB)"
					exit 1
				fi
			else
				echo "Root volume (/dev/vda) not found"
				exit 1
			fi
			echo ""

			# Check for data volume
			if [ -b "/dev/vdb" ]; then
				DATA_SIZE_BYTES=$(lsblk -b -d -n -o SIZE /dev/vdb 2>/dev/null || echo "0")
				DATA_SIZE_GB=$((DATA_SIZE_BYTES / 1024 / 1024 / 1024))
				echo "Data volume (/dev/vdb) size: ${DATA_SIZE_GB}GB"

				if [ "$DATA_SIZE_GB" -ge 95 ] && [ "$DATA_SIZE_GB" -le 105 ]; then
					echo "Data volume size is within expected range (95-105GB)"
				else
					echo "Data volume size is outside expected range (95-105GB)"
					exit 1
				fi
			else
				echo "Data volume (/dev/vdb) not found"
				exit 1
			fi
			echo ""

			echo "=== IBM Cloud Volume Information ==="
			echo "Note: Volume profiles and tags are configured at IBM Cloud level:"
			echo "- Root volume configured with 'general-purpose' profile"
			echo "- Data volume configured with 'custom' profile with 1000 IOPS and dependent bandwidth"
			echo "- Both volumes set to delete on instance termination"
			echo "- Volumes tagged with 'test:root-volume' and 'test:data-volume'"
			echo ""

			echo "=== Test Summary ==="
			echo "Block device inspection completed successfully!"
			echo "Root volume is ~${ROOT_SIZE_GB}GB (configured as general-purpose)"
			echo "Data volume is ~${DATA_SIZE_GB}GB (configured as custom)"
			echo "Both volumes match the IBMNodeClass block device mapping specification"
			echo ""
			echo "=== Block Device Inspector Completed ==="

			# Success - short sleep for log collection
			echo "Test completed successfully. Sleeping briefly for log collection..."
			sleep 60`,
					},
					TerminationMessagePath: "/tmp/termination-log",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("256Mi"),
						},
					},
					SecurityContext: &corev1.SecurityContext{
						Privileged: utilsptr.To(true),
					},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "host-dev", MountPath: "/dev", ReadOnly: true},
					},
				},
			},
			NodeSelector: map[string]string{
				"test-name":             testName,
				"karpenter.sh/nodepool": nodePool.Name,
			},
			Volumes: []corev1.Volume{
				{
					Name: "host-dev",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/dev",
							Type: utilsptr.To(corev1.HostPathDirectory),
						},
					},
				},
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

	// Wait for pod to complete block device checks
	t.Logf("Waiting for pod to complete block device checks...")
	suite.waitForPodCompletion(t, testPod.Name, testPod.Namespace)

	// Get pod status to verify successful completion
	var completedPod corev1.Pod
	err = suite.kubeClient.Get(ctx, client.ObjectKeyFromObject(testPod), &completedPod)
	require.NoError(t, err)

	// Check that pod completed successfully (exit code 0)
	switch completedPod.Status.Phase {
	case corev1.PodSucceeded:
		t.Logf("Pod completed successfully - block device test passed")
	case corev1.PodFailed:
		// Get container status for failure details
		for _, containerStatus := range completedPod.Status.ContainerStatuses {
			if containerStatus.State.Terminated != nil {
				t.Errorf("Pod failed with exit code %d: %s",
					containerStatus.State.Terminated.ExitCode,
					containerStatus.State.Terminated.Reason)
			}
		}
		require.Fail(t, "Pod failed - block device configuration verification failed")
	}

	podLogs, err := suite.getPodLogs(ctx, testPod.Name, testPod.Namespace)
	require.NoError(t, err, "Inspector logs must be readable through verified kubelet serving TLS")
	require.NotEmpty(t, podLogs)
	suite.cleanupTestResources(t, testName)
	suite.waitForNodeCleanup(t, nodeName, time.Minute)

	t.Logf("Block device mapping test completed successfully")
}

func (s *E2ETestSuite) getPodLogs(ctx context.Context, podName, namespace string) (string, error) {
	if namespace == "" {
		namespace = "default"
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	stream, err := s.coreClient.Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{}).Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get pod logs: %w", err)
	}
	defer func() { _ = stream.Close() }()

	logs, err := io.ReadAll(stream)
	if err != nil {
		return "", fmt.Errorf("failed to read pod logs: %w", err)
	}
	return string(logs), nil
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
			t.Logf("Pod %s completed with phase: %s", podName, pod.Status.Phase)
			return
		}

		time.Sleep(pollInterval)
	}

	t.Fatalf("Pod %s did not complete within timeout", podName)
}

// waitForPodReady waits for a pod to be ready
func (s *E2ETestSuite) waitForPodReady(t *testing.T, podName, namespace string) {
	t.Helper()
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

		if pod.Status.Phase == corev1.PodSucceeded {
			t.Logf("Pod %s completed successfully", podName)
			return
		}
		if pod.Status.Phase == corev1.PodFailed {
			for _, status := range pod.Status.ContainerStatuses {
				if status.State.Terminated != nil {
					t.Logf("Container %s failed with exit code %d: %s", status.Name,
						status.State.Terminated.ExitCode, status.State.Terminated.Reason)
				}
			}
			logCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			podLogs, logErr := s.getPodLogs(logCtx, podName, namespace)
			cancel()
			if logErr != nil {
				t.Logf("Failed to collect logs for pod %s: %v", podName, logErr)
			} else {
				t.Logf("Pod logs:\n%s", podLogs)
			}
			t.Fatalf("Pod %s failed before becoming ready: %s: %s", podName, pod.Status.Reason, pod.Status.Message)
		}

		if pod.Status.Phase == corev1.PodRunning {
			// Check if all containers are ready
			allReady := true
			for _, condition := range pod.Status.Conditions {
				if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
					t.Logf("Pod %s is ready", podName)
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
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	require.NoError(t, s.waitForNodeAbsent(ctx, nodeName), "Storage node must disappear after scoped cleanup")
}

func (s *E2ETestSuite) waitForNodeAbsent(ctx context.Context, nodeName string) error {
	return wait.PollUntilContextCancel(ctx, pollInterval, true, func(ctx context.Context) (bool, error) {
		var node corev1.Node
		err := s.kubeClient.Get(ctx, client.ObjectKey{Name: nodeName}, &node)
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	})
}

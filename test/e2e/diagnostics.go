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
	"os/exec"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

// logDeploymentDiagnostics logs detailed diagnostic information for a deployment
func (s *E2ETestSuite) logDeploymentDiagnostics(t *testing.T, deploymentName, namespace string) {
	ctx := context.Background()

	// Log pod status
	var podList corev1.PodList
	err := s.kubeClient.List(ctx, &podList, client.InNamespace(namespace), client.MatchingLabels{"app": deploymentName})
	if err != nil {
		t.Logf("Failed to list pods for diagnostics: %v", err)
		return
	}

	t.Logf("Diagnostic information for deployment %s:", deploymentName)

	for _, pod := range podList.Items {
		t.Logf("Pod %s status: Phase=%s, Reason=%s", pod.Name, pod.Status.Phase, pod.Status.Reason)

		// Log conditions that are not True
		for _, condition := range pod.Status.Conditions {
			if condition.Status != corev1.ConditionTrue {
				t.Logf("Pod %s condition %s: %s - %s", pod.Name, condition.Type, condition.Status, condition.Message)
			}
		}

		// Log container statuses
		for _, containerStatus := range pod.Status.ContainerStatuses {
			if !containerStatus.Ready {
				t.Logf("Container %s not ready: %+v", containerStatus.Name, containerStatus.State)
			}
		}

		// Log events for this pod
		var eventList corev1.EventList
		err := s.kubeClient.List(ctx, &eventList, client.InNamespace(namespace))
		if err == nil {
			for _, event := range eventList.Items {
				if event.InvolvedObject.Kind == "Pod" && event.InvolvedObject.Name == pod.Name {
					t.Logf("Pod %s event: %s - %s", pod.Name, event.Reason, event.Message)
				}
			}
		}
	}
}

// extractInstanceIDFromProviderID extracts the instance ID from a Kubernetes node's ProviderID
func extractInstanceIDFromProviderID(providerID string) string {
	// ProviderID format: "ibm:///region/instance_id"
	parts := strings.Split(providerID, "/")
	if len(parts) >= 4 {
		return parts[len(parts)-1]
	}
	return ""
}

// dumpBootstrapLogs attempts to SSH into the instance and dump bootstrap logs
func (s *E2ETestSuite) dumpBootstrapLogs(t *testing.T, nodeClaimName string) {
	ctx := context.Background()

	// Get the NodeClaim to extract ProviderID
	var nodeClaim karpv1.NodeClaim
	err := s.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClaimName}, &nodeClaim)
	if err != nil {
		t.Logf("Failed to get NodeClaim for bootstrap log dump: %v", err)
		return
	}

	if nodeClaim.Status.ProviderID == "" {
		t.Logf("NodeClaim has no ProviderID, cannot dump bootstrap logs")
		return
	}

	instanceID := extractInstanceIDFromProviderID(nodeClaim.Status.ProviderID)
	if instanceID == "" {
		t.Logf("Could not extract instance ID from ProviderID: %s", nodeClaim.Status.ProviderID)
		return
	}

	t.Logf("Attempting to dump bootstrap logs for instance %s", instanceID)

	// Create floating IP for the instance
	floatingIP, err := s.createFloatingIPForInstance(t, instanceID, nodeClaimName)
	if err != nil {
		t.Logf("Failed to create floating IP: %v", err)
		return
	}

	// Clean up floating IP when done
	defer s.cleanupFloatingIP(t, floatingIP)

	// Wait a bit for the floating IP to be ready
	time.Sleep(30 * time.Second)

	// Attempt to dump bootstrap logs with retries
	s.attemptBootstrapLogDump(t, floatingIP, instanceID, 3)
}

// createFloatingIPForInstance creates a floating IP and attaches it to an instance for debugging
func (s *E2ETestSuite) createFloatingIPForInstance(t *testing.T, instanceID, instanceName string) (string, error) {
	t.Logf("Creating floating IP for instance %s", instanceID)

	// First check if instance exists and get its details
	cmd := exec.Command("ibmcloud", "is", "instance", instanceID, "--output", "json")
	instanceOutput, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get instance details: %v", err)
	}

	// Use IBM Cloud CLI to create floating IP
	fipName := fmt.Sprintf("e2e-debug-%s", instanceName)
	cmd = exec.Command("ibmcloud", "is", "floating-ip-reserve",
		fipName,
		"--zone", s.testZone,
		"--output", "json")

	fipOutput, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to create floating IP: %v", err)
	}

	// Extract floating IP address from output
	fipOutputStr := string(fipOutput)
	lines := strings.Split(fipOutputStr, "\n")
	var floatingIP string
	for _, line := range lines {
		if strings.Contains(line, "\"address\"") {
			parts := strings.Split(line, "\"")
			for i, part := range parts {
				if part == "address" && i+2 < len(parts) {
					floatingIP = parts[i+2]
					break
				}
			}
			break
		}
	}

	if floatingIP == "" {
		return "", fmt.Errorf("could not extract floating IP address from output")
	}

	t.Logf("Created floating IP: %s", floatingIP)

	// Attach the floating IP to the instance's primary network interface
	// Get the instance's network interface
	instanceOutputStr := string(instanceOutput)
	var networkInterfaceID string
	lines = strings.Split(instanceOutputStr, "\n")
	for _, line := range lines {
		if strings.Contains(line, "\"id\"") && strings.Contains(strings.ToLower(line), "interface") {
			parts := strings.Split(line, "\"")
			for i, part := range parts {
				if part == "id" && i+2 < len(parts) {
					networkInterfaceID = parts[i+2]
					break
				}
			}
			break
		}
	}

	if networkInterfaceID != "" {
		// Attach floating IP to the network interface
		attachCmd := exec.Command("ibmcloud", "is", "floating-ip-update", fipName,
			"--nic", networkInterfaceID)
		if err := attachCmd.Run(); err != nil {
			t.Logf("Warning: Failed to attach floating IP to network interface: %v", err)
		} else {
			t.Logf("Attached floating IP %s to network interface %s", floatingIP, networkInterfaceID)
		}
	}

	return floatingIP, nil
}

// attemptBootstrapLogDump attempts to SSH into the instance and retrieve bootstrap logs
func (s *E2ETestSuite) attemptBootstrapLogDump(t *testing.T, floatingIP, instanceID string, maxRetries int) {
	sshKey := "~/.ssh/eb"

	logFiles := []string{
		"/var/log/cloud-init.log",
		"/var/log/cloud-init-output.log",
		"/var/log/karpenter-bootstrap.log",
		"/var/log/apt/history.log",
	}

	commands := []struct {
		name string
		cmd  string
	}{
		{"cloud-init status", "cloud-init status --long"},
		{"systemd failed services", "systemctl --failed"},
		{"kubelet status", "systemctl status kubelet"},
		{"docker status", "systemctl status docker"},
		{"disk usage", "df -h"},
		{"memory usage", "free -h"},
		{"recent dmesg", "dmesg | tail -50"},
	}

	for retry := 1; retry <= maxRetries; retry++ {
		t.Logf("Attempting to SSH into instance (attempt %d/%d): %s", retry, maxRetries, floatingIP)

		// Test SSH connectivity first
		testCmd := exec.Command("ssh", "-i", sshKey, "-o", "StrictHostKeyChecking=no",
			"-o", "ConnectTimeout=30", "-o", "BatchMode=yes",
			fmt.Sprintf("root@%s", floatingIP), "echo 'SSH connected'")

		if err := testCmd.Run(); err != nil {
			t.Logf("SSH connectivity test failed (attempt %d): %v", retry, err)
			if retry < maxRetries {
				time.Sleep(30 * time.Second)
				continue
			} else {
				t.Logf("Failed to establish SSH connection after %d attempts", maxRetries)
				return
			}
		}

		t.Logf("SSH connection established to %s", floatingIP)
		break
	}

	// Dump log files
	t.Logf("Dumping bootstrap log files from instance %s", instanceID)
	for _, logFile := range logFiles {
		cmd := exec.Command("ssh", "-i", sshKey, "-o", "StrictHostKeyChecking=no",
			fmt.Sprintf("root@%s", floatingIP),
			fmt.Sprintf("if [ -f %s ]; then echo '=== %s ==='; cat %s; else echo 'File %s not found'; fi",
				logFile, logFile, logFile, logFile))

		output, err := cmd.Output()
		if err != nil {
			t.Logf("Failed to read %s: %v", logFile, err)
		} else {
			t.Logf("Content of %s:\n%s", logFile, string(output))
		}
	}

	// Run diagnostic commands
	t.Logf("Running diagnostic commands on instance %s", instanceID)
	for _, diagCmd := range commands {
		cmd := exec.Command("ssh", "-i", sshKey, "-o", "StrictHostKeyChecking=no",
			fmt.Sprintf("root@%s", floatingIP), diagCmd.cmd)

		output, err := cmd.Output()
		if err != nil {
			t.Logf("Failed to run '%s': %v", diagCmd.name, err)
		} else {
			t.Logf("%s output:\n%s", diagCmd.name, string(output))
		}
	}
}

// logNodeClassEvents logs all events related to a specific NodeClass
func (s *E2ETestSuite) logNodeClassEvents(t *testing.T, nodeClassName string) {
	ctx := context.Background()

	// List events in all namespaces
	var eventList corev1.EventList
	err := s.kubeClient.List(ctx, &eventList)
	if err != nil {
		t.Logf("Failed to list events: %v", err)
		return
	}

	t.Logf("Events related to NodeClass %s:", nodeClassName)
	eventFound := false

	for _, event := range eventList.Items {
		// Check if event is related to our NodeClass
		if event.InvolvedObject.Kind == "IBMNodeClass" && event.InvolvedObject.Name == nodeClassName {
			eventFound = true
			t.Logf("  [%s] %s: %s - %s",
				event.FirstTimestamp.Format("15:04:05"),
				event.Type, event.Reason, event.Message)
		}
	}

	if !eventFound {
		t.Logf("  No events found for NodeClass %s", nodeClassName)
	}
}

// logNodePoolStatus logs the current status of a NodePool
func (s *E2ETestSuite) logNodePoolStatus(t *testing.T, nodePoolName string) {
	ctx := context.Background()

	var nodePool karpv1.NodePool
	err := s.kubeClient.Get(ctx, types.NamespacedName{Name: nodePoolName}, &nodePool)
	if err != nil {
		t.Logf("Failed to get NodePool %s: %v", nodePoolName, err)
		return
	}

	t.Logf("NodePool %s status:", nodePoolName)
	t.Logf("  Conditions:")
	for _, condition := range nodePool.Status.Conditions {
		t.Logf("    - Type: %s, Status: %s, Reason: %s, Message: %s",
			condition.Type, condition.Status, condition.Reason, condition.Message)
	}

	// Log NodePool resources
	t.Logf("  Resources:")
	if nodePool.Status.Resources != nil {
		t.Logf("    - CPU: %v", nodePool.Status.Resources[corev1.ResourceCPU])
		t.Logf("    - Memory: %v", nodePool.Status.Resources[corev1.ResourceMemory])
		t.Logf("    - Pods: %v", nodePool.Status.Resources[corev1.ResourcePods])
	}
}

// logNodeClaimStatus logs the status of all NodeClaims, focusing on test-related ones
func (s *E2ETestSuite) logNodeClaimStatus(t *testing.T) {
	ctx := context.Background()

	var nodeClaimList karpv1.NodeClaimList
	err := s.kubeClient.List(ctx, &nodeClaimList)
	if err != nil {
		t.Logf("Failed to list NodeClaims: %v", err)
		return
	}

	t.Logf("NodeClaims in cluster: %d", len(nodeClaimList.Items))

	for _, nodeClaim := range nodeClaimList.Items {
		// Only log test-related NodeClaims
		if val, ok := nodeClaim.Labels["test"]; ok && val == "e2e" {
			t.Logf("  NodeClaim %s:", nodeClaim.Name)
			t.Logf("    ProviderID: %s", nodeClaim.Status.ProviderID)
			t.Logf("    NodeName: %s", nodeClaim.Status.NodeName)
			t.Logf("    Conditions:")

			for _, condition := range nodeClaim.Status.Conditions {
				t.Logf("      - %s: %s (%s)", condition.Type, condition.Status, condition.Reason)
				if condition.Message != "" {
					t.Logf("        Message: %s", condition.Message)
				}
			}

			// Log allocatable resources
			if len(nodeClaim.Status.Allocatable) > 0 {
				t.Logf("    Allocatable:")
				for resource, quantity := range nodeClaim.Status.Allocatable {
					t.Logf("      - %s: %v", resource, quantity)
				}
			}
		}
	}
}

// logClusterState logs overall cluster state for debugging
func (s *E2ETestSuite) logClusterState(t *testing.T) {
	ctx := context.Background()
	t.Logf("Current cluster state:")

	// Log all nodes
	var nodeList corev1.NodeList
	err := s.kubeClient.List(ctx, &nodeList)
	if err != nil {
		t.Logf("Failed to list nodes: %v", err)
		return
	}

	karpenterNodes := 0
	for _, node := range nodeList.Items {
		if _, exists := node.Labels["karpenter.sh/nodepool"]; exists {
			karpenterNodes++
		}
	}

	t.Logf("  Total nodes: %d (Karpenter-managed: %d)", len(nodeList.Items), karpenterNodes)

	// Log pending pods
	var podList corev1.PodList
	err = s.kubeClient.List(ctx, &podList)
	if err != nil {
		t.Logf("Failed to list pods: %v", err)
		return
	}

	pendingPods := 0
	runningPods := 0
	for _, pod := range podList.Items {
		switch pod.Status.Phase {
		case corev1.PodPending:
			pendingPods++
		case corev1.PodRunning:
			runningPods++
		}
	}

	t.Logf("  Total pods: %d (Running: %d, Pending: %d)", len(podList.Items), runningPods, pendingPods)
}

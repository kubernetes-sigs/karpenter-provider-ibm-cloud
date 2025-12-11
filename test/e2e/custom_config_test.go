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
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestE2ECustomConfig demonstrates running E2E tests with custom configurations
func TestE2ECustomConfig(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	defer suite.cleanupAllStaleResources(t)

	testName := GenerateTestName("custom-config")

	// Check if a custom config file path is provided
	configPath := os.Getenv("E2E_CONFIG_PATH")
	if configPath == "" {
		t.Skip("E2E_CONFIG_PATH not set - skipping custom config test")
	}

	t.Logf("Loading test configuration from: %s", configPath)
	config, err := LoadTestConfigFromFile(configPath)
	require.NoError(t, err, "Failed to load test configuration")

	// Override test name if not set in config
	if config.NodeClass.Name == "" {
		config.NodeClass.Name = testName
	}
	if config.NodePool.Name == "" {
		config.NodePool.Name = testName + "-nodepool"
	}
	if config.Workload.Name == "" {
		config.Workload.Name = testName + "-workload"
	}

	// Add node selector for the workload to target the nodepool
	if config.Workload.NodeSelector == nil {
		config.Workload.NodeSelector = make(map[string]string)
	}
	config.Workload.NodeSelector["karpenter.sh/nodepool"] = config.NodePool.Name

	var nodeClassName string
	if config.SkipNodeClassCreation {
		// Use existing NodeClass
		nodeClassName = config.NodeClass.Name
		t.Logf("Using existing NodeClass: %s", nodeClassName)

		// Verify it exists
		suite.waitForNodeClassReady(t, nodeClassName)
		t.Logf("Existing NodeClass is ready: %s", nodeClassName)
	} else {
		// Create NodeClass from config
		nodeClass := suite.CreateNodeClassFromConfig(t, config.NodeClass)
		require.NotNil(t, nodeClass, "Failed to create NodeClass")

		// Wait for NodeClass to be ready
		suite.waitForNodeClassReady(t, nodeClass.Name)
		t.Logf("NodeClass is ready: %s", nodeClass.Name)
		nodeClassName = nodeClass.Name
	}

	// Create NodePool from config
	nodePool := suite.CreateNodePoolFromConfig(t, config.NodePool, nodeClassName)
	require.NotNil(t, nodePool, "Failed to create NodePool")
	t.Logf("Created NodePool: %s", nodePool.Name)

	// Create workload from config
	suite.CreateWorkloadFromConfig(t, config.Workload)
	t.Logf("Created workload: %s", config.Workload.Name)

	// Wait for pods to be scheduled
	suite.waitForPodsToBeScheduled(t, config.Workload.Name, config.Workload.Namespace)
	t.Logf("Workload deployment is ready: %s/%s", config.Workload.Namespace, config.Workload.Name)

	// Verify nodes were provisioned
	nodes := suite.getKarpenterNodes(t, nodePool.Name)
	require.NotEmpty(t, nodes, "No nodes were provisioned for NodePool")
	t.Logf("Successfully provisioned %d nodes for custom configuration", len(nodes))

	// Cleanup
	t.Log("Cleaning up test resources...")
	suite.cleanupTestWorkload(t, config.Workload.Name, config.Workload.Namespace)
	suite.cleanupTestResources(t, nodePool.Name)

	// Only cleanup NodeClass if we created it
	if !config.SkipNodeClassCreation {
		t.Logf("Cleaning up NodeClass: %s", nodeClassName)
		// Note: cleanupTestResources already handles NodeClass cleanup via test labels
	} else {
		t.Logf("Skipping NodeClass cleanup (was pre-existing): %s", nodeClassName)
	}
}

// TestE2ECustomConfigFromEnv demonstrates running E2E tests with config from environment variable
func TestE2ECustomConfigFromEnv(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	defer suite.cleanupAllStaleResources(t)

	testName := GenerateTestName("custom-config-env")

	// Check if custom config is provided via environment variable
	if os.Getenv("E2E_CONFIG_JSON") == "" {
		t.Skip("E2E_CONFIG_JSON not set - skipping custom config from env test")
	}

	t.Log("Loading test configuration from E2E_CONFIG_JSON environment variable")
	config, err := LoadTestConfigFromEnv("E2E_CONFIG_JSON")
	require.NoError(t, err, "Failed to load test configuration from env")

	// Override test name if not set in config
	if config.NodeClass.Name == "" {
		config.NodeClass.Name = testName
	}
	if config.NodePool.Name == "" {
		config.NodePool.Name = testName + "-nodepool"
	}
	if config.Workload.Name == "" {
		config.Workload.Name = testName + "-workload"
	}

	// Add node selector
	if config.Workload.NodeSelector == nil {
		config.Workload.NodeSelector = make(map[string]string)
	}
	config.Workload.NodeSelector["karpenter.sh/nodepool"] = config.NodePool.Name

	// Create resources
	nodeClass := suite.CreateNodeClassFromConfig(t, config.NodeClass)
	suite.waitForNodeClassReady(t, nodeClass.Name)

	nodePool := suite.CreateNodePoolFromConfig(t, config.NodePool, nodeClass.Name)
	suite.CreateWorkloadFromConfig(t, config.Workload)

	// Wait and verify
	suite.waitForPodsToBeScheduled(t, config.Workload.Name, config.Workload.Namespace)
	nodes := suite.getKarpenterNodes(t, nodePool.Name)
	require.NotEmpty(t, nodes, "No nodes were provisioned")
	t.Logf("Successfully provisioned %d nodes from environment config", len(nodes))

	// Cleanup
	suite.cleanupTestWorkload(t, config.Workload.Name, config.Workload.Namespace)
	suite.cleanupTestResources(t, nodePool.Name)
}

// TestE2EProgrammaticConfig demonstrates creating custom config programmatically
func TestE2EProgrammaticConfig(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	defer suite.cleanupAllStaleResources(t)

	testName := GenerateTestName("programmatic-config")

	// Build custom configuration programmatically
	instanceProfile := "mx2-4x32" // High-memory instance
	config := TestConfig{
		NodeClass: NodeClassConfig{
			Name:            testName,
			InstanceProfile: &instanceProfile,
			Tags: map[string]string{
				"memory-optimized": "true",
				"purpose":          "data-processing",
			},
		},
		NodePool: NodePoolConfig{
			Name:          testName + "-nodepool",
			InstanceTypes: []string{"mx2-4x32", "mx2-8x64"},
			Architecture:  []string{"amd64"},
			CapacityTypes: []string{"on-demand"},
			Labels: map[string]string{
				"workload-type": "memory-intensive",
			},
		},
		Workload: WorkloadConfig{
			Name:          testName + "-workload",
			Namespace:     "default",
			Replicas:      2,
			CPURequest:    "2000m",
			MemoryRequest: "8Gi",
			CPULimit:      "4000m",
			MemoryLimit:   "16Gi",
			NodeSelector: map[string]string{
				"karpenter.sh/nodepool": testName + "-nodepool",
			},
			PodAntiAffinity: true,
			AdditionalLabels: map[string]string{
				"app-type": "database",
			},
		},
	}

	// Create resources
	nodeClass := suite.CreateNodeClassFromConfig(t, config.NodeClass)
	suite.waitForNodeClassReady(t, nodeClass.Name)
	t.Logf("Created memory-optimized NodeClass: %s", nodeClass.Name)

	nodePool := suite.CreateNodePoolFromConfig(t, config.NodePool, nodeClass.Name)
	t.Logf("Created memory-intensive NodePool: %s", nodePool.Name)

	suite.CreateWorkloadFromConfig(t, config.Workload)
	t.Logf("Created high-memory workload: %s", config.Workload.Name)

	// Wait and verify
	suite.waitForPodsToBeScheduled(t, config.Workload.Name, config.Workload.Namespace)
	nodes := suite.getKarpenterNodes(t, nodePool.Name)
	require.NotEmpty(t, nodes, "No nodes were provisioned")

	// Verify we got memory-optimized instances
	for _, node := range nodes {
		instanceType := node.Labels["node.kubernetes.io/instance-type"]
		t.Logf("Provisioned node with instance type: %s", instanceType)
		require.Contains(t, []string{"mx2-4x32", "mx2-8x64"}, instanceType,
			"Node should use memory-optimized instance type")
	}

	// Cleanup
	suite.cleanupTestWorkload(t, config.Workload.Name, config.Workload.Namespace)
	suite.cleanupTestResources(t, nodePool.Name)
}

// Helper to generate unique test names
func GenerateTestName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().Unix())
}

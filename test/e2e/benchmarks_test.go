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
)

// BenchmarkE2EInstanceCreation benchmarks the time it takes to create and launch instances
func BenchmarkE2EInstanceCreation(b *testing.B) {
	if os.Getenv("RUN_E2E_BENCHMARKS") != "true" {
		b.Skip("Skipping E2E benchmarks - set RUN_E2E_BENCHMARKS=true to run")
	}

	// Create a test suite - we need to convert testing.B to testing.T for setup
	// This is a bit of a hack but necessary for the current setup function
	mockT := &testing.T{}
	suite := SetupE2ETestSuite(mockT)

	// Reset timer after setup
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			testName := fmt.Sprintf("bench-%d-%d", time.Now().UnixNano(), b.N)

			// Create NodeClass
			nodeClass := suite.createTestNodeClass(mockT, testName)

			// Create NodeClaim
			nodeClaim := suite.createTestNodeClaim(mockT, testName, nodeClass.Name)

			// Wait for creation (this is what we're benchmarking)
			start := time.Now()
			suite.waitForInstanceCreation(mockT, nodeClaim.Name)
			duration := time.Since(start)

			b.Logf("Instance creation took: %v", duration)

			// Clean up
			suite.deleteNodeClaim(mockT, nodeClaim.Name)
			suite.waitForInstanceDeletion(mockT, nodeClaim.Name)
			suite.cleanupTestResources(mockT, testName)
		}
	})
}

// BenchmarkE2ENodeClassValidation benchmarks NodeClass validation time
func BenchmarkE2ENodeClassValidation(b *testing.B) {
	if os.Getenv("RUN_E2E_BENCHMARKS") != "true" {
		b.Skip("Skipping E2E benchmarks - set RUN_E2E_BENCHMARKS=true to run")
	}

	mockT := &testing.T{}
	suite := SetupE2ETestSuite(mockT)

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		testName := fmt.Sprintf("validation-bench-%d-%d", time.Now().UnixNano(), i)

		start := time.Now()

		// Create and validate NodeClass
		nodeClass := suite.createTestNodeClass(mockT, testName)
		suite.waitForNodeClassReady(mockT, nodeClass.Name)

		duration := time.Since(start)
		b.Logf("NodeClass validation took: %v", duration)

		// Cleanup
		suite.cleanupTestResources(mockT, testName)
	}
}

// BenchmarkE2EPodScheduling benchmarks pod scheduling time on Karpenter nodes
func BenchmarkE2EPodScheduling(b *testing.B) {
	if os.Getenv("RUN_E2E_BENCHMARKS") != "true" {
		b.Skip("Skipping E2E benchmarks - set RUN_E2E_BENCHMARKS=true to run")
	}

	mockT := &testing.T{}
	suite := SetupE2ETestSuite(mockT)

	// Setup infrastructure once for all benchmark iterations
	testName := fmt.Sprintf("scheduling-bench-%d", time.Now().Unix())
	nodeClass := suite.createTestNodeClass(mockT, testName)
	suite.waitForNodeClassReady(mockT, nodeClass.Name)
	_ = suite.createTestNodePool(mockT, testName, nodeClass.Name)

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		workloadName := fmt.Sprintf("%s-workload-%d", testName, i)

		start := time.Now()

		// Create workload and measure scheduling time
		deployment := suite.createTestWorkload(mockT, workloadName)
		suite.waitForPodsToBeScheduled(mockT, deployment.Name, deployment.Namespace)

		duration := time.Since(start)
		b.Logf("Pod scheduling took: %v", duration)

		// Cleanup workload
		suite.cleanupTestWorkload(mockT, deployment.Name, deployment.Namespace)
		suite.waitForPodsGone(mockT, deployment.Name+"-workload")
	}

	// Cleanup infrastructure
	suite.cleanupTestResources(mockT, testName)
}

// BenchmarkE2EFullWorkflow benchmarks the complete E2E workflow
func BenchmarkE2EFullWorkflow(b *testing.B) {
	if os.Getenv("RUN_E2E_BENCHMARKS") != "true" {
		b.Skip("Skipping E2E benchmarks - set RUN_E2E_BENCHMARKS=true to run")
	}

	mockT := &testing.T{}
	suite := SetupE2ETestSuite(mockT)

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		testName := fmt.Sprintf("full-workflow-bench-%d-%d", time.Now().UnixNano(), i)

		start := time.Now()

		// Complete workflow
		nodeClass := suite.createTestNodeClass(mockT, testName)
		suite.waitForNodeClassReady(mockT, nodeClass.Name)

		_ = suite.createTestNodePool(mockT, testName, nodeClass.Name)
		deployment := suite.createTestWorkload(mockT, testName)
		suite.waitForPodsToBeScheduled(mockT, deployment.Name, deployment.Namespace)

		// Verify the workflow completed successfully
		suite.verifyKarpenterNodesExist(mockT)
		suite.verifyInstancesInIBMCloud(mockT)

		duration := time.Since(start)
		b.Logf("Full E2E workflow took: %v", duration)

		// Cleanup
		suite.cleanupTestWorkload(mockT, deployment.Name, deployment.Namespace)
		suite.cleanupTestResources(mockT, testName)
	}
}

// BenchmarkE2ECleanup benchmarks cleanup operation performance
func BenchmarkE2ECleanup(b *testing.B) {
	if os.Getenv("RUN_E2E_BENCHMARKS") != "true" {
		b.Skip("Skipping E2E benchmarks - set RUN_E2E_BENCHMARKS=true to run")
	}

	mockT := &testing.T{}
	suite := SetupE2ETestSuite(mockT)

	// Pre-create resources for cleanup benchmarking
	var testResources []string
	for i := 0; i < b.N; i++ {
		testName := fmt.Sprintf("cleanup-bench-%d-%d", time.Now().UnixNano(), i)
		testResources = append(testResources, testName)

		// Create resources to be cleaned up
		nodeClass := suite.createTestNodeClass(mockT, testName)
		suite.waitForNodeClassReady(mockT, nodeClass.Name)
		_ = suite.createTestNodePool(mockT, testName, nodeClass.Name)
		deployment := suite.createTestWorkload(mockT, testName)
		suite.waitForPodsToBeScheduled(mockT, deployment.Name, deployment.Namespace)
	}

	b.ResetTimer()

	// Benchmark cleanup of all resources
	for i, testName := range testResources {
		start := time.Now()

		suite.cleanupTestResources(mockT, testName)

		duration := time.Since(start)
		b.Logf("Cleanup iteration %d took: %v", i, duration)
	}
}

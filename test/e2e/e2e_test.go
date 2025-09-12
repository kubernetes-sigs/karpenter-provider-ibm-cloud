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

// This file serves as the main entry point for the E2E test package.
// All test functionality has been organized into separate files:
//
// - suite.go: Test suite setup, configuration, and shared types
// - resources.go: Resource creation functions (NodeClass, NodePool, Workloads)
// - waiting.go: Waiting and polling functions
// - verification.go: Verification and validation functions
// - cleanup.go: Cleanup and deletion functions
// - diagnostics.go: Diagnostic and logging functions
// - instance_discovery.go: IBM Cloud instance discovery
//
// Test files organized by functionality:
// - basic_workflow_test.go: Core E2E workflow tests
// - validation_test.go: NodeClass and validation tests
// - cleanup_test.go: Resource cleanup tests
// - scheduling_test.go: Pod scheduling and affinity tests
// - benchmarks_test.go: Performance benchmarks
//
// This reorganization improves maintainability and reduces code duplication
// that was present in the original 2000+ line monolithic file.

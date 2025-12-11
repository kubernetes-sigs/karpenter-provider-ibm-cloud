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
	"testing"
)

// GetAvailableInstanceType returns a suitable instance type for testing
func (s *E2ETestSuite) GetAvailableInstanceType(t *testing.T) string {
	// Use hardcoded instance types that are commonly available
	availableTypes := []string{
		"bx2-2x8",   // 2 vCPU, 8 GB RAM
		"bx3d-2x10", // 2 vCPU, 10 GB RAM
		"cx2-2x4",   // 2 vCPU, 4 GB RAM
		"mx2-2x16",  // 2 vCPU, 16 GB RAM
	}

	selected := availableTypes[0]
	t.Logf("Selected instance type for testing: %s", selected)
	return selected
}

// GetMultipleInstanceTypes returns multiple instance types for testing NodePool with multiple types
func (s *E2ETestSuite) GetMultipleInstanceTypes(t *testing.T, count int) []string {
	if count <= 0 {
		count = 3
	}

	availableTypes := []string{
		"bx2-2x8",  // 2 vCPU, 8 GB RAM
		"bx2-4x16", // 4 vCPU, 16 GB RAM
		"mx2-2x16", // 2 vCPU, 16 GB RAM
		"cx2-4x8",  // 4 vCPU, 8 GB RAM
		"bx2-8x32", // 8 vCPU, 32 GB RAM
	}

	// Return requested count, up to available types
	maxCount := count
	if maxCount > len(availableTypes) {
		maxCount = len(availableTypes)
	}
	selected := availableTypes[:maxCount]

	t.Logf("Selected %d instance types for testing: %v", len(selected), selected)
	return selected
}

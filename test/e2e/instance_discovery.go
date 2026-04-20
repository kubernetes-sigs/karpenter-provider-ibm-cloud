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

	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/stretchr/testify/require"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
)

// GetAvailableInstanceType returns the smallest 2-4 vCPU / 4-16 GB profile
// offered in the test region.
func (s *E2ETestSuite) GetAvailableInstanceType(t *testing.T) string {
	profiles := s.discoverInstanceProfiles(t)
	t.Logf("Selected instance type for testing: %s", profiles[0])
	return profiles[0]
}

// GetMultipleInstanceTypes returns up to count small profiles.
func (s *E2ETestSuite) GetMultipleInstanceTypes(t *testing.T, count int) []string {
	if count <= 0 {
		count = 3
	}
	profiles := s.discoverInstanceProfiles(t)
	if count > len(profiles) {
		count = len(profiles)
	}
	selected := profiles[:count]
	t.Logf("Selected %d instance types for testing: %v", len(selected), selected)
	return selected
}

// discoverInstanceProfiles queries VPC for 2-4 vCPU / 4-16 GB profiles. A VPC
// API failure is fatal: the provider needs the same API at runtime, so
// falling back to a static list would just mask the real problem.
func (s *E2ETestSuite) discoverInstanceProfiles(t *testing.T) []string {
	s.profileOnce.Do(func() {
		baseURL := "https://" + s.testRegion + ".iaas.cloud.ibm.com/v1"
		client, err := ibm.NewVPCClient(baseURL, "iam", s.apiKey, s.testRegion, s.testResourceGroup)
		require.NoError(t, err, "create VPC client for instance profile discovery")
		// VPCClient.ListInstanceProfiles uses context.Background internally; no ctx wiring.
		coll, _, err := client.ListInstanceProfiles(&vpcv1.ListInstanceProfilesOptions{})
		require.NoError(t, err, "list VPC instance profiles")
		s.profiles = filterSmallProfiles(coll.Profiles)
		require.NotEmpty(t, s.profiles, "VPC returned no profiles matching 2-4 vCPU / 4-16 GB in %s", s.testRegion)
	})
	return s.profiles
}

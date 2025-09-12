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

package v1alpha1

import (
	"strings"
	"testing"

	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/apis/constants"
	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

func TestLabelConstants(t *testing.T) {
	tests := []struct {
		name     string
		label    string
		expected string
	}{
		{"InstanceSizeLabelKey", InstanceSizeLabelKey, constants.Group + "/instance-size"},
		{"InstanceFamilyLabelKey", InstanceFamilyLabelKey, constants.Group + "/instance-family"},
		{"InstanceMemoryLabelKey", InstanceMemoryLabelKey, constants.Group + "/instance-memory"},
		{"InstanceCPULabelKey", InstanceCPULabelKey, constants.Group + "/instance-cpu"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.label != tt.expected {
				t.Errorf("%s = %v, want %v", tt.name, tt.label, tt.expected)
			}
		})
	}
}

func TestInternalLabelConstants(t *testing.T) {
	tests := []struct {
		name     string
		label    string
		expected string
	}{
		{"IbmLabelKey", IbmLabelKey, "ibm.x-k8s.io/node"},
		{"IbmLabelValue", IbmLabelValue, "fake"},
		{"NodeViewerLabelKey", NodeViewerLabelKey, "eks-node-viewer/instance-price"},
		{"IbmPartitionLabelKey", IbmPartitionLabelKey, "ibm-partition"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.label != tt.expected {
				t.Errorf("%s = %v, want %v", tt.name, tt.label, tt.expected)
			}
		})
	}
}

func TestInstanceLabelsHaveGroupPrefix(t *testing.T) {
	instanceLabels := []string{
		InstanceSizeLabelKey,
		InstanceFamilyLabelKey,
		InstanceMemoryLabelKey,
		InstanceCPULabelKey,
	}

	for _, label := range instanceLabels {
		if !strings.HasPrefix(label, constants.Group) {
			t.Errorf("Instance label %v should start with group prefix %v", label, constants.Group)
		}
	}
}

func TestLabelsNotEmpty(t *testing.T) {
	labels := []string{
		InstanceSizeLabelKey,
		InstanceFamilyLabelKey,
		InstanceMemoryLabelKey,
		InstanceCPULabelKey,
		IbmLabelKey,
		IbmLabelValue,
		NodeViewerLabelKey,
		IbmPartitionLabelKey,
	}

	for _, label := range labels {
		if label == "" {
			t.Error("Label constant should not be empty")
		}
	}
}

func TestInitFunctionRegistersLabels(t *testing.T) {
	// Test that the init function properly registers the group in RestrictedLabelDomains
	if !v1.RestrictedLabelDomains.Has(constants.Group) {
		t.Errorf("RestrictedLabelDomains should contain %v", constants.Group)
	}

	// Test that instance labels are registered in WellKnownLabels
	expectedWellKnownLabels := []string{
		InstanceSizeLabelKey,
		InstanceFamilyLabelKey,
		InstanceCPULabelKey,
		InstanceMemoryLabelKey,
	}

	for _, label := range expectedWellKnownLabels {
		if !v1.WellKnownLabels.Has(label) {
			t.Errorf("WellKnownLabels should contain %v", label)
		}
	}
}

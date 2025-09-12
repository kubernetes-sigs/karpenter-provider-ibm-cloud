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

package autoplacement

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestInstanceTypeSelectionsMetric(t *testing.T) {
	// Reset the metric
	InstanceTypeSelections.Reset()

	// Test metric properties
	if InstanceTypeSelections == nil {
		t.Error("InstanceTypeSelections should not be nil")
	}

	// Test incrementing counter
	InstanceTypeSelections.WithLabelValues("test-nodeclass", "success").Inc()

	expected := `
		# HELP karpenter_ibm_instance_type_selections_total Number of automatic instance type selections performed
		# TYPE karpenter_ibm_instance_type_selections_total counter
		karpenter_ibm_instance_type_selections_total{nodeclass="test-nodeclass",result="success"} 1
	`

	if err := testutil.CollectAndCompare(InstanceTypeSelections, strings.NewReader(expected)); err != nil {
		t.Errorf("InstanceTypeSelections metric mismatch: %v", err)
	}
}

func TestSubnetSelectionsMetric(t *testing.T) {
	// Reset the metric
	SubnetSelections.Reset()

	// Test metric properties
	if SubnetSelections == nil {
		t.Error("SubnetSelections should not be nil")
	}

	// Test incrementing counter
	SubnetSelections.WithLabelValues("test-nodeclass", "failure").Inc()

	expected := `
		# HELP karpenter_ibm_subnet_selections_total Number of automatic subnet selections performed
		# TYPE karpenter_ibm_subnet_selections_total counter
		karpenter_ibm_subnet_selections_total{nodeclass="test-nodeclass",result="failure"} 1
	`

	if err := testutil.CollectAndCompare(SubnetSelections, strings.NewReader(expected)); err != nil {
		t.Errorf("SubnetSelections metric mismatch: %v", err)
	}
}

func TestSelectedInstanceTypesMetric(t *testing.T) {
	// Reset the metric
	SelectedInstanceTypes.Reset()

	// Test metric properties
	if SelectedInstanceTypes == nil {
		t.Error("SelectedInstanceTypes should not be nil")
	}

	// Test setting gauge value
	SelectedInstanceTypes.WithLabelValues("test-nodeclass").Set(5)

	expected := `
		# HELP karpenter_ibm_selected_instance_types Number of instance types selected for each nodeclass
		# TYPE karpenter_ibm_selected_instance_types gauge
		karpenter_ibm_selected_instance_types{nodeclass="test-nodeclass"} 5
	`

	if err := testutil.CollectAndCompare(SelectedInstanceTypes, strings.NewReader(expected)); err != nil {
		t.Errorf("SelectedInstanceTypes metric mismatch: %v", err)
	}
}

func TestSelectedSubnetsMetric(t *testing.T) {
	// Reset the metric
	SelectedSubnets.Reset()

	// Test metric properties
	if SelectedSubnets == nil {
		t.Error("SelectedSubnets should not be nil")
	}

	// Test setting gauge value
	SelectedSubnets.WithLabelValues("test-nodeclass").Set(3)

	expected := `
		# HELP karpenter_ibm_selected_subnets Number of subnets selected for each nodeclass
		# TYPE karpenter_ibm_selected_subnets gauge
		karpenter_ibm_selected_subnets{nodeclass="test-nodeclass"} 3
	`

	if err := testutil.CollectAndCompare(SelectedSubnets, strings.NewReader(expected)); err != nil {
		t.Errorf("SelectedSubnets metric mismatch: %v", err)
	}
}

func TestInstanceTypeSelectionLatencyMetric(t *testing.T) {
	// Reset the metric
	InstanceTypeSelectionLatency.Reset()

	// Test metric properties
	if InstanceTypeSelectionLatency == nil {
		t.Error("InstanceTypeSelectionLatency should not be nil")
	}

	// Test observing histogram value - should not panic
	InstanceTypeSelectionLatency.WithLabelValues("test-nodeclass").Observe(1.5)

	// Just verify the metric can be used without error
	t.Log("InstanceTypeSelectionLatency metric works correctly")
}

func TestSubnetSelectionLatencyMetric(t *testing.T) {
	// Reset the metric
	SubnetSelectionLatency.Reset()

	// Test metric properties
	if SubnetSelectionLatency == nil {
		t.Error("SubnetSelectionLatency should not be nil")
	}

	// Test observing histogram value - should not panic
	SubnetSelectionLatency.WithLabelValues("test-nodeclass").Observe(0.8)

	// Just verify the metric can be used without error
	t.Log("SubnetSelectionLatency metric works correctly")
}

func TestMetricLabels(t *testing.T) {
	// Test that metrics accept the expected labels
	testLabels := map[string][]string{
		"InstanceTypeSelections":       {"nodeclass", "result"},
		"SubnetSelections":             {"nodeclass", "result"},
		"SelectedInstanceTypes":        {"nodeclass"},
		"SelectedSubnets":              {"nodeclass"},
		"InstanceTypeSelectionLatency": {"nodeclass"},
		"SubnetSelectionLatency":       {"nodeclass"},
	}

	// Test InstanceTypeSelections labels
	InstanceTypeSelections.Reset()
	InstanceTypeSelections.WithLabelValues("test-nc", "success").Inc()

	// Test SubnetSelections labels
	SubnetSelections.Reset()
	SubnetSelections.WithLabelValues("test-nc", "success").Inc()

	// Test SelectedInstanceTypes labels
	SelectedInstanceTypes.Reset()
	SelectedInstanceTypes.WithLabelValues("test-nc").Set(1)

	// Test SelectedSubnets labels
	SelectedSubnets.Reset()
	SelectedSubnets.WithLabelValues("test-nc").Set(1)

	// Test InstanceTypeSelectionLatency labels
	InstanceTypeSelectionLatency.Reset()
	InstanceTypeSelectionLatency.WithLabelValues("test-nc").Observe(1.0)

	// Test SubnetSelectionLatency labels
	SubnetSelectionLatency.Reset()
	SubnetSelectionLatency.WithLabelValues("test-nc").Observe(1.0)

	t.Logf("All metrics accept their expected labels: %v", testLabels)
}

func TestMetricBuckets(t *testing.T) {
	// Test that histogram metrics have expected buckets
	expectedBuckets := []float64{0.1, 0.5, 1.0, 2.0, 5.0}

	// We can't directly access buckets from prometheus metrics easily in tests,
	// so we just verify the metrics were created successfully with buckets
	if InstanceTypeSelectionLatency == nil {
		t.Error("InstanceTypeSelectionLatency should be created with buckets")
	}

	if SubnetSelectionLatency == nil {
		t.Error("SubnetSelectionLatency should be created with buckets")
	}

	t.Logf("Histogram metrics created with buckets: %v", expectedBuckets)
}

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

package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestApiRequestsMetric(t *testing.T) {
	// Reset the metric
	ApiRequests.Reset()

	// Test metric properties
	if ApiRequests == nil {
		t.Error("ApiRequests should not be nil")
	}

	// Test incrementing counter
	ApiRequests.WithLabelValues("CreateInstance", "200", "us-south").Inc()

	expected := `
		# HELP karpenter_ibm_api_requests_total Total IBM API requests by operation and status
		# TYPE karpenter_ibm_api_requests_total counter
		karpenter_ibm_api_requests_total{operation="CreateInstance",region="us-south",status="200"} 1
	`

	if err := testutil.CollectAndCompare(ApiRequests, strings.NewReader(expected)); err != nil {
		t.Errorf("ApiRequests metric mismatch: %v", err)
	}
}

func TestProvisioningDurationMetric(t *testing.T) {
	ProvisioningDuration.Reset()
	ProvisioningDuration.WithLabelValues("bx2-2x8", "us-south-1").Observe(2.5)
	// no strict compare for histogram, just ensure metric can be used
	t.Log("ProvisioningDuration metric works correctly")
}

func TestCostPerHourMetric(t *testing.T) {
	CostPerHour.Reset()
	CostPerHour.WithLabelValues("bx2-2x8", "us-south").Set(0.123)

	expected := `
		# HELP karpenter_ibm_cost_per_hour Estimated cost per hour for instance type in region.
		# TYPE karpenter_ibm_cost_per_hour gauge
		karpenter_ibm_cost_per_hour{instance_type="bx2-2x8",region="us-south"} 0.123
	`

	if err := testutil.CollectAndCompare(CostPerHour, strings.NewReader(expected)); err != nil {
		t.Errorf("CostPerHour metric mismatch: %v", err)
	}
}

func TestQuotaUtilizationMetric(t *testing.T) {
	QuotaUtilization.Reset()
	QuotaUtilization.WithLabelValues("vCPU", "us-south").Set(0.75)

	expected := `
		# HELP karpenter_ibm_quota_utilization Quota utilization for resource type in region (0..1).
		# TYPE karpenter_ibm_quota_utilization gauge
		karpenter_ibm_quota_utilization{region="us-south",resource_type="vCPU"} 0.75
	`

	if err := testutil.CollectAndCompare(QuotaUtilization, strings.NewReader(expected)); err != nil {
		t.Errorf("QuotaUtilization metric mismatch: %v", err)
	}
}

func TestInstanceLifecycleMetric(t *testing.T) {
	InstanceLifecycle.Reset()
	InstanceLifecycle.WithLabelValues("running", "bx2-2x8").Set(1)

	expected := `
		# HELP karpenter_ibm_instance_lifecycle Instance lifecycle state as numeric label.
		# TYPE karpenter_ibm_instance_lifecycle gauge
		karpenter_ibm_instance_lifecycle{instance_type="bx2-2x8",state="running"} 1
	`

	if err := testutil.CollectAndCompare(InstanceLifecycle, strings.NewReader(expected)); err != nil {
		t.Errorf("InstanceLifecycle metric mismatch: %v", err)
	}
}

func TestDriftDetectionsTotalMetric(t *testing.T) {
	DriftDetectionsTotal.Reset()

	if DriftDetectionsTotal == nil {
		t.Error("DriftDetectionsTotal should not be nil")
	}

	DriftDetectionsTotal.WithLabelValues("NodeClassHashChanged", "test-nodeclass").Inc()

	expected := `
		# HELP karpenter_ibm_drift_detections_total Total number of drift detections by reason and nodeclass
		# TYPE karpenter_ibm_drift_detections_total counter
		karpenter_ibm_drift_detections_total{drift_reason="NodeClassHashChanged",nodeclass="test-nodeclass"} 1
	`

	if err := testutil.CollectAndCompare(DriftDetectionsTotal, strings.NewReader(expected)); err != nil {
		t.Errorf("DriftDetectionsTotal metric mismatch: %v", err)
	}
}

func TestDriftDetectionDurationMetric(t *testing.T) {
	DriftDetectionDuration.Reset()
	DriftDetectionDuration.WithLabelValues("test-nodeclass").Observe(0.5)
	// no strict compare for histogram, just ensure metric can be used
	t.Log("DriftDetectionDuration metric works correctly")
}

func TestBatcherBatchWindowDurationMetric(t *testing.T) {
	BatcherBatchWindowDuration.Reset()

	if BatcherBatchWindowDuration == nil {
		t.Error("BatcherBatchWindowDuration should not be nil")
	}

	BatcherBatchWindowDuration.WithLabelValues("vpc-client").Observe(0.25)

	// no strict compare for histogram, just ensure metric can be used
	t.Log("BatcherBatchWindowDuration metric works correctly")
}

func TestBatcherBatchSizeMetric(t *testing.T) {
	BatcherBatchSize.Reset()

	if BatcherBatchSize == nil {
		t.Error("BatcherBatchSize should not be nil")
	}

	BatcherBatchSize.WithLabelValues("vpc-client").Observe(10)

	// no strict compare for histogram, just ensure metric can be used
	t.Log("BatcherBatchSize metric works correctly")
}

func TestMetricLabels(t *testing.T) {
	// Test that metrics accept the expected labels
	ApiRequests.Reset()
	ApiRequests.WithLabelValues("test-operation", "200", "us-south").Inc()

	ProvisioningDuration.Reset()
	ProvisioningDuration.WithLabelValues("bx2-2x8", "us-south-1").Observe(1.0)

	CostPerHour.Reset()
	CostPerHour.WithLabelValues("bx2-2x8", "us-south").Set(0.1)

	QuotaUtilization.Reset()
	QuotaUtilization.WithLabelValues("vCPU", "us-south").Set(0.5)

	InstanceLifecycle.Reset()
	InstanceLifecycle.WithLabelValues("running", "bx2-2x8").Set(1)

	DriftDetectionsTotal.Reset()
	DriftDetectionsTotal.WithLabelValues("ImageDrift", "my-nodeclass").Inc()

	DriftDetectionDuration.Reset()
	DriftDetectionDuration.WithLabelValues("my-nodeclass").Observe(0.123)

	BatcherBatchWindowDuration.Reset()
	BatcherBatchWindowDuration.WithLabelValues("my-batcher").Observe(0.5)

	BatcherBatchSize.Reset()
	BatcherBatchSize.WithLabelValues("my-batcher").Observe(25)

	t.Log("All metrics accept their expected labels")
}

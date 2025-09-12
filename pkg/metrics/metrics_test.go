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
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
)

func TestRecordAPIRequest(t *testing.T) {
	// Reset the counter for testing
	APIRequestsTotal.Reset()

	RecordAPIRequest("vpc", "create_instance", "200")
	RecordAPIRequest("vpc", "create_instance", "429")
	RecordAPIRequest("iam", "get_token", "200")

	// Check that metrics were recorded correctly
	assert.Equal(t, 1.0, testutil.ToFloat64(APIRequestsTotal.WithLabelValues("vpc", "create_instance", "200")))
	assert.Equal(t, 1.0, testutil.ToFloat64(APIRequestsTotal.WithLabelValues("vpc", "create_instance", "429")))
	assert.Equal(t, 1.0, testutil.ToFloat64(APIRequestsTotal.WithLabelValues("iam", "get_token", "200")))
}

func TestRecordInstanceProvisioningDuration(t *testing.T) {
	// Reset the histogram for testing
	InstanceProvisioningDuration.Reset()

	duration := 30 * time.Second
	RecordInstanceProvisioningDuration("bx2.2x8", "us-south-1", "success", duration)

	// Verify that the observation was recorded
	// For histograms, we check the _count metric instead of the histogram itself
	metricString := `
		# HELP karpenter_ibm_instance_provisioning_duration_seconds Time taken to provision instances in seconds
		# TYPE karpenter_ibm_instance_provisioning_duration_seconds histogram
		karpenter_ibm_instance_provisioning_duration_seconds_count{instance_type="bx2.2x8",result="success",zone="us-south-1"} 1
	`

	err := testutil.CollectAndCompare(InstanceProvisioningDuration, strings.NewReader(metricString), "karpenter_ibm_instance_provisioning_duration_seconds_count")
	assert.NoError(t, err)
}

func TestUpdateCacheHitRate(t *testing.T) {
	// Reset the gauge for testing
	CacheHitRate.Reset()

	UpdateCacheHitRate("pricing", 85.5)
	UpdateCacheHitRate("instance_types", 92.3)

	assert.Equal(t, 85.5, testutil.ToFloat64(CacheHitRate.WithLabelValues("pricing")))
	assert.Equal(t, 92.3, testutil.ToFloat64(CacheHitRate.WithLabelValues("instance_types")))
}

func TestSetInstanceTypeOfferingAvailability(t *testing.T) {
	// Reset the gauge for testing
	InstanceTypeOfferingAvailable.Reset()

	SetInstanceTypeOfferingAvailability("bx2.2x8", "on-demand", "us-south-1", true)
	SetInstanceTypeOfferingAvailability("bx2.4x16", "spot", "us-south-2", false)

	assert.Equal(t, 1.0, testutil.ToFloat64(InstanceTypeOfferingAvailable.WithLabelValues("bx2.2x8", "on-demand", "us-south-1")))
	assert.Equal(t, 0.0, testutil.ToFloat64(InstanceTypeOfferingAvailable.WithLabelValues("bx2.4x16", "spot", "us-south-2")))
}

func TestSetInstanceTypeOfferingPrice(t *testing.T) {
	// Reset the gauge for testing
	InstanceTypeOfferingPriceEstimate.Reset()

	SetInstanceTypeOfferingPrice("bx2.2x8", "us-south-1", 0.095)
	SetInstanceTypeOfferingPrice("bx2.4x16", "us-south-2", 0.190)

	assert.Equal(t, 0.095, testutil.ToFloat64(InstanceTypeOfferingPriceEstimate.WithLabelValues("bx2.2x8", "us-south-1")))
	assert.Equal(t, 0.190, testutil.ToFloat64(InstanceTypeOfferingPriceEstimate.WithLabelValues("bx2.4x16", "us-south-2")))
}

func TestRecordNodeProvisioningError(t *testing.T) {
	// Reset the counter for testing
	NodeProvisioningErrors.Reset()

	RecordNodeProvisioningError("quota_exceeded", "bx2.2x8", "us-south-1")
	RecordNodeProvisioningError("api_error", "bx2.4x16", "us-south-2")
	RecordNodeProvisioningError("quota_exceeded", "bx2.2x8", "us-south-1") // Same error again

	assert.Equal(t, 2.0, testutil.ToFloat64(NodeProvisioningErrors.WithLabelValues("quota_exceeded", "bx2.2x8", "us-south-1")))
	assert.Equal(t, 1.0, testutil.ToFloat64(NodeProvisioningErrors.WithLabelValues("api_error", "bx2.4x16", "us-south-2")))
}

func TestSetActiveNodes(t *testing.T) {
	// Reset the gauge for testing
	ActiveNodes.Reset()

	SetActiveNodes("bx2.2x8", "us-south-1", "on-demand", 5.0)
	SetActiveNodes("bx2.4x16", "us-south-2", "spot", 3.0)

	assert.Equal(t, 5.0, testutil.ToFloat64(ActiveNodes.WithLabelValues("bx2.2x8", "us-south-1", "on-demand")))
	assert.Equal(t, 3.0, testutil.ToFloat64(ActiveNodes.WithLabelValues("bx2.4x16", "us-south-2", "spot")))
}

func TestMetricsNamespace(t *testing.T) {
	assert.Equal(t, "karpenter", Namespace)
	assert.Equal(t, "ibm", IBMSubsystem)
}

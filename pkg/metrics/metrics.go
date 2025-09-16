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
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	ApiRequests = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "karpenter_ibm_api_requests_total",
			Help: "Total IBM API requests by operation and status",
		},
		[]string{"operation", "status", "region"},
	)

	ProvisioningDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "karpenter_ibm_provisioning_duration_seconds",
			Help:    "Provisioning duration in seconds.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"instance_type", "zone"},
	)

	CostPerHour = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "karpenter_ibm_cost_per_hour",
			Help: "Estimated cost per hour for instance type in region.",
		},
		[]string{"instance_type", "region"},
	)

	QuotaUtilization = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "karpenter_ibm_quota_utilization",
			Help: "Quota utilization for resource type in region (0..1).",
		},
		[]string{"resource_type", "region"},
	)

	InstanceLifecycle = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "karpenter_ibm_instance_lifecycle",
			Help: "Instance lifecycle state as numeric label.",
		},
		[]string{"state", "instance_type"},
	)
)

func init() {
	metrics.Registry.MustRegister(
		ApiRequests,
		ProvisioningDuration,
		CostPerHour,
		QuotaUtilization,
		InstanceLifecycle,
	)
}

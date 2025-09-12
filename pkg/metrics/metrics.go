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
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

const (
	// Namespace for all karpenter IBM metrics
	Namespace = "karpenter"
	// Subsystem for IBM cloud provider specific metrics
	IBMSubsystem = "ibm"
)

var (
	// APIRequestsTotal tracks total number of IBM Cloud API requests
	APIRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: IBMSubsystem,
			Name:      "api_requests_total",
			Help:      "Total number of IBM Cloud API requests by service, operation, and status code",
		},
		[]string{"service", "operation", "status_code"},
	)

	// InstanceProvisioningDuration tracks time taken to provision instances
	InstanceProvisioningDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: Namespace,
			Subsystem: IBMSubsystem,
			Name:      "instance_provisioning_duration_seconds",
			Help:      "Time taken to provision instances in seconds",
			Buckets:   prometheus.ExponentialBuckets(1, 2, 12), // 1s to ~1hr
		},
		[]string{"instance_type", "zone", "result"},
	)

	// CacheHitRate tracks cache hit rate percentage by cache type
	CacheHitRate = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: Namespace,
			Subsystem: IBMSubsystem,
			Name:      "cache_hit_rate",
			Help:      "Cache hit rate percentage by cache type",
		},
		[]string{"cache_type"},
	)

	// InstanceTypeOfferingAvailable tracks instance type availability
	InstanceTypeOfferingAvailable = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: Namespace,
			Subsystem: IBMSubsystem,
			Name:      "instance_type_offering_available",
			Help:      "Instance type offering availability by type, capacity type, and zone",
		},
		[]string{"instance_type", "capacity_type", "zone"},
	)

	// InstanceTypeOfferingPriceEstimate tracks estimated pricing for instance types
	InstanceTypeOfferingPriceEstimate = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: Namespace,
			Subsystem: IBMSubsystem,
			Name:      "instance_type_offering_price_estimate",
			Help:      "Estimated hourly price for instance types in USD",
		},
		[]string{"instance_type", "zone"},
	)

	// NodeProvisioningErrors tracks node provisioning errors
	NodeProvisioningErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: IBMSubsystem,
			Name:      "node_provisioning_errors_total",
			Help:      "Total number of node provisioning errors by error type",
		},
		[]string{"error_type", "instance_type", "zone"},
	)

	// ActiveNodes tracks the number of active nodes managed by Karpenter IBM
	ActiveNodes = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: Namespace,
			Subsystem: IBMSubsystem,
			Name:      "active_nodes",
			Help:      "Number of active nodes managed by Karpenter IBM by instance type and zone",
		},
		[]string{"instance_type", "zone", "capacity_type"},
	)
)

// RecordAPIRequest increments the counter for IBM Cloud API requests
func RecordAPIRequest(service, operation, statusCode string) {
	APIRequestsTotal.WithLabelValues(service, operation, statusCode).Inc()
}

// RecordInstanceProvisioningDuration records the time taken to provision an instance
func RecordInstanceProvisioningDuration(instanceType, zone, result string, duration time.Duration) {
	InstanceProvisioningDuration.WithLabelValues(instanceType, zone, result).Observe(duration.Seconds())
}

// UpdateCacheHitRate updates the cache hit rate for a specific cache type
func UpdateCacheHitRate(cacheType string, hitRate float64) {
	CacheHitRate.WithLabelValues(cacheType).Set(hitRate)
}

// SetInstanceTypeOfferingAvailability sets the availability status for an instance type offering
func SetInstanceTypeOfferingAvailability(instanceType, capacityType, zone string, available bool) {
	var value float64
	if available {
		value = 1
	}
	InstanceTypeOfferingAvailable.WithLabelValues(instanceType, capacityType, zone).Set(value)
}

// SetInstanceTypeOfferingPrice sets the estimated price for an instance type
func SetInstanceTypeOfferingPrice(instanceType, zone string, price float64) {
	InstanceTypeOfferingPriceEstimate.WithLabelValues(instanceType, zone).Set(price)
}

// RecordNodeProvisioningError records a node provisioning error
func RecordNodeProvisioningError(errorType, instanceType, zone string) {
	NodeProvisioningErrors.WithLabelValues(errorType, instanceType, zone).Inc()
}

// SetActiveNodes sets the number of active nodes
func SetActiveNodes(instanceType, zone, capacityType string, count float64) {
	ActiveNodes.WithLabelValues(instanceType, zone, capacityType).Set(count)
}

// init registers all metrics with the controller-runtime metrics registry
func init() {
	metrics.Registry.MustRegister(
		APIRequestsTotal,
		InstanceProvisioningDuration,
		CacheHitRate,
		InstanceTypeOfferingAvailable,
		InstanceTypeOfferingPriceEstimate,
		NodeProvisioningErrors,
		ActiveNodes,
	)
}

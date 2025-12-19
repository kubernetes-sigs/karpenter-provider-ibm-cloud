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
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// InstanceTypeSelections tracks the number of automatic instance type selections performed
	InstanceTypeSelections = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "karpenter_ibm_instance_type_selections_total",
			Help: "Number of automatic instance type selections performed",
		},
		[]string{"nodeclass", "result"},
	)

	// SubnetSelections tracks the number of automatic subnet selections performed
	SubnetSelections = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "karpenter_ibm_subnet_selections_total",
			Help: "Number of automatic subnet selections performed",
		},
		[]string{"nodeclass", "result"},
	)

	// SelectedInstanceTypes tracks the number of instance types selected for each nodeclass
	SelectedInstanceTypes = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "karpenter_ibm_selected_instance_types",
			Help: "Number of instance types selected for each nodeclass",
		},
		[]string{"nodeclass"},
	)

	// SelectedSubnets tracks the number of subnets selected for each nodeclass
	SelectedSubnets = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "karpenter_ibm_selected_subnets",
			Help: "Number of subnets selected for each nodeclass",
		},
		[]string{"nodeclass"},
	)

	// InstanceTypeSelectionLatency tracks the duration of instance type selection operations
	InstanceTypeSelectionLatency = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "karpenter_ibm_instance_type_selection_duration_seconds",
			Help:    "Duration of instance type selection operations",
			Buckets: []float64{0.1, 0.5, 1.0, 2.0, 5.0},
		},
		[]string{"nodeclass"},
	)

	// SubnetSelectionLatency tracks the duration of subnet selection operations
	SubnetSelectionLatency = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "karpenter_ibm_subnet_selection_duration_seconds",
			Help:    "Duration of subnet selection operations",
			Buckets: []float64{0.1, 0.5, 1.0, 2.0, 5.0},
		},
		[]string{"nodeclass"},
	)
)

func init() {
	// Register metrics with the global prometheus registry
	metrics.Registry.MustRegister(
		InstanceTypeSelections,
		SubnetSelections,
		SelectedInstanceTypes,
		SelectedSubnets,
		InstanceTypeSelectionLatency,
		SubnetSelectionLatency,
	)
}

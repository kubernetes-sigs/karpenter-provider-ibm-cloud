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

package instance

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/metrics"
)

func TestInventoryMetricsTrackLiveCountsAndClearLastDeletion(t *testing.T) {
	profile := "audit-metrics-profile"
	defer metrics.InstanceLifecycle.DeleteLabelValues("running", profile)
	p := &VPCInstanceProvider{}
	p.publishInventoryCounts("us-south", map[string]float64{profile: 2})
	p.publishInventoryCounts("eu-de", map[string]float64{profile: 1})
	require.Equal(t, float64(3), testutil.ToFloat64(metrics.InstanceLifecycle.WithLabelValues("running", profile)))
	p.publishInventoryCounts("us-south", map[string]float64{})
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.InstanceLifecycle.WithLabelValues("running", profile)))
	p.publishInventoryCounts("eu-de", map[string]float64{})
	p.publishInventoryCounts("eu-de", map[string]float64{})
	registry := prometheus.NewRegistry()
	registry.MustRegister(metrics.InstanceLifecycle)
	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		for _, sample := range family.Metric {
			for _, label := range sample.Label {
				require.NotEqual(t, profile, label.GetValue())
			}
		}
	}
}

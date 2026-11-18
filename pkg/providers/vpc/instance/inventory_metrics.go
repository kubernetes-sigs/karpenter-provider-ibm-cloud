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
	"context"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/metrics"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/utils/ownership"
)

func WithMetricsContext(ctx context.Context) Option {
	return func(p *VPCInstanceProvider) error { p.metricsCtx = ctx; return nil }
}

func (p *VPCInstanceProvider) scheduleInventoryMetrics(region string) {
	if p.metricsCtx == nil {
		return
	}
	p.metricsMu.Lock()
	if p.metricsRegions == nil {
		p.metricsRegions = map[string]bool{}
	}
	p.metricsRegions[region] = true
	if p.metricsStarted {
		p.metricsMu.Unlock()
		return
	}
	p.metricsStarted = true
	p.metricsMu.Unlock()
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			p.metricsMu.Lock()
			regions := make([]string, 0, len(p.metricsRegions))
			for region := range p.metricsRegions {
				regions = append(regions, region)
			}
			p.metricsMu.Unlock()
			for _, region := range regions {
				ctx, cancel := context.WithTimeout(p.metricsCtx, 30*time.Second)
				if err := p.refreshInventoryMetrics(ctx, region); err != nil {
					log.FromContext(ctx).V(1).Info("Inventory metrics refresh failed", "region", region, "error", err)
				}
				cancel()
			}
			select {
			case <-p.metricsCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (p *VPCInstanceProvider) refreshInventoryMetrics(ctx context.Context, region string) error {
	vpc, err := p.clientForRegion(ctx, region)
	if err != nil {
		return err
	}
	instances, err := vpc.ListInstances(ctx)
	if err != nil {
		return err
	}
	clusterUID, err := ownership.ClusterUID(ctx, p.reader())
	if err != nil {
		return err
	}
	claims := &karpv1.NodeClaimList{}
	if listErr := p.reader().List(ctx, claims); listErr != nil {
		return listErr
	}
	owned := map[string]*launchConfig{}
	for _, claim := range claims.Items {
		if raw := claim.Annotations[LaunchAnnotation]; raw != "" {
			launch, decodeErr := decodeLaunch(raw)
			if decodeErr != nil {
				return decodeErr
			}
			if launch.ClusterUID == clusterUID && launch.ClaimUID == string(claim.UID) && launch.Region == region {
				owned[launch.Name] = launch
			}
		}
	}
	counts := map[string]float64{}
	for _, instance := range instances {
		if instance.Name == nil || instance.Profile == nil || instance.Profile.Name == nil || instance.Status == nil || *instance.Status != "running" {
			continue
		}
		launch := owned[*instance.Name]
		if launch != nil && *instance.Profile.Name == launch.Profile && verifyInstanceAccount(&instance, launch.AccountID) == nil {
			counts[launch.Profile]++
		}
	}
	p.publishInventoryCounts(region, counts)
	quota, err := p.getQuotaInfo(ctx, region)
	if err != nil {
		return err
	}
	metrics.QuotaUtilization.WithLabelValues("instances", region).Set(quota.InstanceUtilization)
	metrics.QuotaUtilization.WithLabelValues("vCPU", region).Set(quota.VCPUUtilization)
	return nil
}
func (p *VPCInstanceProvider) publishInventoryCounts(region string, counts map[string]float64) {
	p.metricsMu.Lock()
	if p.metricsSnapshots == nil {
		p.metricsSnapshots = map[string]map[string]float64{}
	}
	if p.metricsProfiles == nil {
		p.metricsProfiles = map[string]bool{}
	}
	p.metricsSnapshots[region] = counts
	combined := map[string]float64{}
	for _, snapshot := range p.metricsSnapshots {
		for profile, count := range snapshot {
			combined[profile] += count
		}
	}
	for profile := range p.metricsProfiles {
		if combined[profile] == 0 {
			metrics.InstanceLifecycle.DeleteLabelValues("running", profile)
			delete(p.metricsProfiles, profile)
		}
	}
	for profile, count := range combined {
		metrics.InstanceLifecycle.WithLabelValues("running", profile).Set(count)
		p.metricsProfiles[profile] = true
	}
	p.metricsMu.Unlock()
}

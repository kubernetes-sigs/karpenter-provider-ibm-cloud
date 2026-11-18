/*
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

package bootstrap

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/types"
)

var versionPattern = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:[-+][a-zA-Z0-9.-]+)?$`)
var cniVersionPattern = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]{0,255}$`)
var bootstrapTokenPattern = regexp.MustCompile(`^[a-z0-9]{6}\.[a-z0-9]{16}$`)
var environmentNamePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
var percentagePattern = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?%$`)

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func yamlQuote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func nodeLabels(labels map[string]string) string {
	parts := make([]string, 0, len(labels))
	for key, value := range labels {
		parts = append(parts, key+"="+value)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func validateBootstrapOptions(options types.Options) error {
	for key, value := range options.Labels {
		if len(validation.IsQualifiedName(key)) > 0 || len(validation.IsValidLabelValue(value)) > 0 {
			return fmt.Errorf("invalid bootstrap label %q", key)
		}
	}
	for _, taint := range options.Taints {
		if len(validation.IsQualifiedName(taint.Key)) > 0 || len(validation.IsValidLabelValue(taint.Value)) > 0 {
			return fmt.Errorf("invalid bootstrap taint %q", taint.Key)
		}
		switch taint.Effect {
		case corev1.TaintEffectNoSchedule, corev1.TaintEffectPreferNoSchedule, corev1.TaintEffectNoExecute:
		default:
			return fmt.Errorf("invalid bootstrap taint effect")
		}
	}
	for name, value := range map[string]string{"node name": options.NodeName, "region": options.Region, "zone": options.Zone, "bootstrap status record": options.BootstrapStatusConfigMap} {
		if value != "" && len(validation.IsDNS1123Subdomain(value)) > 0 {
			return fmt.Errorf("invalid bootstrap %s", name)
		}
	}
	if options.ClusterEndpoint != "" {
		endpoint, err := url.Parse(options.ClusterEndpoint)
		if err != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" || endpoint.User != nil ||
			endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Opaque != "" ||
			(endpoint.Path != "" && endpoint.Path != "/") ||
			(net.ParseIP(endpoint.Hostname()) == nil && len(validation.IsDNS1123Subdomain(endpoint.Hostname())) > 0) {
			return fmt.Errorf("bootstrap API endpoint must be an HTTPS server URL")
		}
		if endpoint.Port() != "" {
			port, err := strconv.Atoi(endpoint.Port())
			if err != nil || port < 1 || port > 65535 {
				return fmt.Errorf("bootstrap API endpoint has an invalid port")
			}
		}
	}
	if options.BootstrapToken != "" && !bootstrapTokenPattern.MatchString(options.BootstrapToken) {
		return fmt.Errorf("invalid bootstrap token format")
	}
	if options.DNSClusterIP != "" && net.ParseIP(options.DNSClusterIP) == nil {
		return fmt.Errorf("invalid bootstrap DNS address")
	}
	if options.ClusterCIDR != "" {
		if _, _, err := net.ParseCIDR(options.ClusterCIDR); err != nil {
			return fmt.Errorf("invalid bootstrap cluster CIDR")
		}
	}
	if options.KubernetesVersion != "" && !versionPattern.MatchString(options.KubernetesVersion) {
		return fmt.Errorf("invalid bootstrap Kubernetes version")
	}
	if options.CNIVersion != "" && !cniVersionPattern.MatchString(options.CNIVersion) {
		return fmt.Errorf("invalid bootstrap CNI version")
	}
	if options.Architecture != "" && options.Architecture != "amd64" && options.Architecture != "arm64" {
		return fmt.Errorf("unsupported bootstrap architecture")
	}
	if options.ContainerRuntime != "" && options.ContainerRuntime != "containerd" && options.ContainerRuntime != "cri-o" {
		return fmt.Errorf("unsupported bootstrap container runtime")
	}
	if options.CNIPlugin != "" && options.CNIPlugin != "calico" && options.CNIPlugin != "cilium" && options.CNIPlugin != "flannel" && options.CNIPlugin != "weave" {
		return fmt.Errorf("unsupported bootstrap CNI plugin")
	}
	config := options.KubeletConfig
	if config == nil {
		return nil
	}
	for _, address := range config.ClusterDNS {
		if net.ParseIP(address) == nil {
			return fmt.Errorf("invalid kubelet cluster DNS address")
		}
	}
	if config.MaxPods != nil && *config.MaxPods <= 0 || config.PodsPerCore != nil && *config.PodsPerCore < 0 {
		return fmt.Errorf("invalid kubelet pod limits")
	}
	for _, reservations := range []map[string]string{config.KubeReserved, config.SystemReserved} {
		for key, value := range reservations {
			switch key {
			case "cpu", "memory", "ephemeral-storage", "pid":
			default:
				return fmt.Errorf("invalid kubelet reserved resource %q", key)
			}
			quantity, err := resource.ParseQuantity(value)
			if err != nil || quantity.Sign() < 0 {
				return fmt.Errorf("invalid kubelet resource reservation %q", key)
			}
		}
	}
	for _, thresholds := range []map[string]string{config.EvictionHard, config.EvictionSoft} {
		for key, value := range thresholds {
			switch key {
			case "memory.available", "nodefs.available", "nodefs.inodesFree", "imagefs.available", "imagefs.inodesFree", "containerfs.available", "containerfs.inodesFree", "pid.available":
			default:
				return fmt.Errorf("invalid kubelet eviction signal %q", key)
			}
			if percentagePattern.MatchString(value) {
				percentage, _ := strconv.ParseFloat(strings.TrimSuffix(value, "%"), 64)
				if percentage > 100 {
					return fmt.Errorf("invalid kubelet eviction percentage %q", key)
				}
			} else if quantity, err := resource.ParseQuantity(value); err != nil || quantity.Sign() < 0 {
				return fmt.Errorf("invalid kubelet eviction threshold %q", key)
			}
		}
	}
	for key, duration := range config.EvictionSoftGracePeriod {
		if _, ok := config.EvictionSoft[key]; !ok || duration.Duration < 0 {
			return fmt.Errorf("invalid kubelet eviction grace period %q", key)
		}
	}
	if config.EvictionMaxPodGracePeriod != nil && *config.EvictionMaxPodGracePeriod < 0 {
		return fmt.Errorf("invalid kubelet eviction maximum grace period")
	}
	for _, value := range []*int32{config.ImageGCHighThresholdPercent, config.ImageGCLowThresholdPercent} {
		if value != nil && (*value < 0 || *value > 100) {
			return fmt.Errorf("invalid kubelet image garbage collection threshold")
		}
	}
	if config.ImageGCLowThresholdPercent != nil && config.ImageGCHighThresholdPercent != nil &&
		*config.ImageGCLowThresholdPercent >= *config.ImageGCHighThresholdPercent {
		return fmt.Errorf("kubelet image garbage collection low threshold must be below high threshold")
	}
	return nil
}

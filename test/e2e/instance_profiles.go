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
package e2e

import (
	"sort"
	"strings"

	"github.com/IBM/vpc-go-sdk/vpcv1"
)

// filterSmallProfiles picks 2-4 vCPU / 4-16 GB profiles sorted smallest first.
func filterSmallProfiles(profiles []vpcv1.InstanceProfile) []string {
	type ranked struct {
		name   string
		vcpu   int64
		memory int64
	}
	var keep []ranked
	for _, p := range profiles {
		if p.Name == nil {
			continue
		}
		vcpu := profileVCPU(&p)
		mem := profileMemory(&p)
		if vcpu < 2 || vcpu > 4 {
			continue
		}
		if mem < 4 || mem > 16 {
			continue
		}
		keep = append(keep, ranked{*p.Name, vcpu, mem})
	}
	sort.Slice(keep, func(i, j int) bool {
		if keep[i].vcpu != keep[j].vcpu {
			return keep[i].vcpu < keep[j].vcpu
		}
		if keep[i].memory != keep[j].memory {
			return keep[i].memory < keep[j].memory
		}
		// Tie-break to bx2 family to match karpenter's default selection.
		iBx2 := strings.HasPrefix(keep[i].name, "bx2-")
		jBx2 := strings.HasPrefix(keep[j].name, "bx2-")
		if iBx2 != jBx2 {
			return iBx2
		}
		return keep[i].name < keep[j].name
	})
	names := make([]string, 0, len(keep))
	for _, r := range keep {
		names = append(names, r.name)
	}
	return names
}

// profileVCPU returns the fixed vCPU count, or 0 for range/dependent profiles.
func profileVCPU(p *vpcv1.InstanceProfile) int64 {
	if p.VcpuCount == nil {
		return 0
	}
	v, ok := p.VcpuCount.(*vpcv1.InstanceProfileVcpu)
	if !ok || v.Value == nil {
		return 0
	}
	return *v.Value
}

// profileMemory returns the fixed memory in GB, or 0 for range/dependent profiles.
func profileMemory(p *vpcv1.InstanceProfile) int64 {
	if p.Memory == nil {
		return 0
	}
	m, ok := p.Memory.(*vpcv1.InstanceProfileMemory)
	if !ok || m.Value == nil {
		return 0
	}
	return *m.Value
}

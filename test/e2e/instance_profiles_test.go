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
	"testing"

	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/stretchr/testify/assert"
)

func profile(name string, vcpu, mem int64) vpcv1.InstanceProfile {
	return vpcv1.InstanceProfile{
		Name:      &name,
		VcpuCount: &vpcv1.InstanceProfileVcpu{Value: &vcpu},
		Memory:    &vpcv1.InstanceProfileMemory{Value: &mem},
	}
}

func TestFilterSmallProfiles(t *testing.T) {
	tests := []struct {
		name string
		in   []vpcv1.InstanceProfile
		want []string
	}{
		{
			name: "keeps 2-4 vCPU, 4-16 GB",
			in: []vpcv1.InstanceProfile{
				profile("bx2-2x8", 2, 8),
				profile("bx2-4x16", 4, 16),
				profile("cx2-2x4", 2, 4),
			},
			want: []string{"cx2-2x4", "bx2-2x8", "bx2-4x16"},
		},
		{
			name: "drops vCPU out of range",
			in: []vpcv1.InstanceProfile{
				profile("bx2-8x32", 8, 32),
				profile("bx2-2x8", 2, 8),
				profile("bx2-1x4", 1, 4),
			},
			want: []string{"bx2-2x8"},
		},
		{
			name: "drops memory out of range",
			in: []vpcv1.InstanceProfile{
				profile("bx2-2x64", 2, 64),
				profile("bx2-2x2", 2, 2),
				profile("bx2-2x8", 2, 8),
			},
			want: []string{"bx2-2x8"},
		},
		{
			name: "sort by vcpu then memory then bx2-first then name",
			in: []vpcv1.InstanceProfile{
				profile("bx4-2x8", 2, 8),
				profile("cx2-4x8", 4, 8),
				profile("bx2-2x8", 2, 8),
				profile("mx2-2x16", 2, 16),
			},
			want: []string{"bx2-2x8", "bx4-2x8", "mx2-2x16", "cx2-4x8"},
		},
		{
			name: "skips profiles with nil Name",
			in: []vpcv1.InstanceProfile{
				{VcpuCount: &vpcv1.InstanceProfileVcpu{Value: ptr[int64](2)}, Memory: &vpcv1.InstanceProfileMemory{Value: ptr[int64](8)}},
				profile("bx2-2x8", 2, 8),
			},
			want: []string{"bx2-2x8"},
		},
		{
			name: "empty input",
			in:   nil,
			want: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterSmallProfiles(tt.in)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestProfileVCPU_ZeroForRangeOrNil(t *testing.T) {
	assert.Equal(t, int64(0), profileVCPU(&vpcv1.InstanceProfile{}))
	rangeVal := vpcv1.InstanceProfileVcpuRange{}
	assert.Equal(t, int64(0), profileVCPU(&vpcv1.InstanceProfile{VcpuCount: &rangeVal}))
	fixed := int64(4)
	assert.Equal(t, int64(4), profileVCPU(&vpcv1.InstanceProfile{VcpuCount: &vpcv1.InstanceProfileVcpu{Value: &fixed}}))
}

func TestProfileMemory_ZeroForRangeOrNil(t *testing.T) {
	assert.Equal(t, int64(0), profileMemory(&vpcv1.InstanceProfile{}))
	rangeVal := vpcv1.InstanceProfileMemoryRange{}
	assert.Equal(t, int64(0), profileMemory(&vpcv1.InstanceProfile{Memory: &rangeVal}))
	fixed := int64(16)
	assert.Equal(t, int64(16), profileMemory(&vpcv1.InstanceProfile{Memory: &vpcv1.InstanceProfileMemory{Value: &fixed}}))
}

func ptr[T any](v T) *T { return &v }

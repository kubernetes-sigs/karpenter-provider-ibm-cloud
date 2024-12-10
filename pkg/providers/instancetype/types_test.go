package instancetype

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestInstanceTypeRequirements(t *testing.T) {
	tests := []struct {
		name     string
		req      InstanceTypeRequirements
		validate func(*testing.T, InstanceTypeRequirements)
	}{
		{
			name: "basic CPU and memory requirements",
			req: InstanceTypeRequirements{
				CPU:          4,
				Memory:       8,
				Architecture: "amd64",
				GPU:          false,
			},
			validate: func(t *testing.T, req InstanceTypeRequirements) {
				assert.Equal(t, 4, req.CPU)
				assert.Equal(t, 8, req.Memory)
				assert.Equal(t, "amd64", req.Architecture)
				assert.False(t, req.GPU)
			},
		},
		{
			name: "GPU requirements",
			req: InstanceTypeRequirements{
				CPU:          8,
				Memory:       16,
				Architecture: "amd64",
				GPU:          true,
			},
			validate: func(t *testing.T, req InstanceTypeRequirements) {
				assert.Equal(t, 8, req.CPU)
				assert.Equal(t, 16, req.Memory)
				assert.Equal(t, "amd64", req.Architecture)
				assert.True(t, req.GPU)
			},
		},
		{
			name: "arm64 architecture",
			req: InstanceTypeRequirements{
				CPU:          2,
				Memory:       4,
				Architecture: "arm64",
				GPU:          false,
			},
			validate: func(t *testing.T, req InstanceTypeRequirements) {
				assert.Equal(t, 2, req.CPU)
				assert.Equal(t, 4, req.Memory)
				assert.Equal(t, "arm64", req.Architecture)
				assert.False(t, req.GPU)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.validate(t, tt.req)
		})
	}
}

func TestInstanceTypeCapabilities(t *testing.T) {
	tests := []struct {
		name     string
		cap      InstanceTypeCapabilities
		validate func(*testing.T, InstanceTypeCapabilities)
	}{
		{
			name: "basic capabilities",
			cap: InstanceTypeCapabilities{
				CPU:              4,
				Memory:           8,
				Architecture:     "amd64",
				GPU:              false,
				NetworkBandwidth: 10,
				StorageType:      "ssd",
			},
			validate: func(t *testing.T, cap InstanceTypeCapabilities) {
				assert.Equal(t, 4, cap.CPU)
				assert.Equal(t, 8, cap.Memory)
				assert.Equal(t, "amd64", cap.Architecture)
				assert.False(t, cap.GPU)
				assert.Equal(t, 10, cap.NetworkBandwidth)
				assert.Equal(t, "ssd", cap.StorageType)
			},
		},
		{
			name: "high performance capabilities",
			cap: InstanceTypeCapabilities{
				CPU:              32,
				Memory:           128,
				Architecture:     "amd64",
				GPU:              true,
				NetworkBandwidth: 100,
				StorageType:      "nvme",
			},
			validate: func(t *testing.T, cap InstanceTypeCapabilities) {
				assert.Equal(t, 32, cap.CPU)
				assert.Equal(t, 128, cap.Memory)
				assert.Equal(t, "amd64", cap.Architecture)
				assert.True(t, cap.GPU)
				assert.Equal(t, 100, cap.NetworkBandwidth)
				assert.Equal(t, "nvme", cap.StorageType)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.validate(t, tt.cap)
		})
	}
}

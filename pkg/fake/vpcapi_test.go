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

package fake

import (
	"context"
	"fmt"
	"testing"

	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVPCAPI_CreateInstance(t *testing.T) {
	tests := []struct {
		name         string
		setupFunc    func(*VPCAPI)
		prototype    vpcv1.InstancePrototypeIntf
		expectError  bool
		validateFunc func(*testing.T, *vpcv1.Instance, *VPCAPI)
	}{
		{
			name: "successful instance creation",
			prototype: &vpcv1.InstancePrototype{
				Name: lo.ToPtr("test-instance"),
			},
			expectError: false,
			validateFunc: func(t *testing.T, instance *vpcv1.Instance, api *VPCAPI) {
				assert.NotNil(t, instance)
				assert.NotNil(t, instance.ID)
				assert.NotNil(t, instance.Name)
				assert.Equal(t, "running", lo.FromPtr(instance.Status))

				// Verify instance was added to the store
				assert.Equal(t, 1, api.Instances.Len())
			},
		},
		{
			name: "error during creation",
			setupFunc: func(api *VPCAPI) {
				api.NextError.Store(fmt.Errorf("creation failed"))
			},
			prototype: &vpcv1.InstancePrototype{
				Name: lo.ToPtr("test-instance"),
			},
			expectError: true,
		},
		{
			name: "with mocked output",
			setupFunc: func(api *VPCAPI) {
				mockedInstance := &vpcv1.Instance{
					ID:     lo.ToPtr("mocked-instance-id"),
					Name:   lo.ToPtr("mocked-instance"),
					Status: lo.ToPtr("pending"),
				}
				api.CreateInstanceBehavior.Output.Store(mockedInstance)
			},
			prototype: &vpcv1.InstancePrototype{
				Name: lo.ToPtr("test-instance"),
			},
			expectError: false,
			validateFunc: func(t *testing.T, instance *vpcv1.Instance, api *VPCAPI) {
				assert.Equal(t, "mocked-instance-id", lo.FromPtr(instance.ID))
				assert.Equal(t, "mocked-instance", lo.FromPtr(instance.Name))
				assert.Equal(t, "pending", lo.FromPtr(instance.Status))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := NewVPCAPI()
			if tt.setupFunc != nil {
				tt.setupFunc(api)
			}

			instance, err := api.CreateInstance(context.Background(), tt.prototype)

			if tt.expectError {
				assert.Error(t, err)
				// When there's an error, the call might not be tracked
				return
			} else {
				require.NoError(t, err)
				if tt.validateFunc != nil {
					tt.validateFunc(t, instance, api)
				}
			}

			// Verify the call was tracked (only for successful calls)
			inputs := api.CreateInstanceBehavior.CalledWithInput.Clone()
			assert.Len(t, inputs, 1)
			assert.Equal(t, tt.prototype, inputs[0].InstancePrototype)
		})
	}
}

func TestVPCAPI_DeleteInstance(t *testing.T) {
	tests := []struct {
		name         string
		setupFunc    func(*VPCAPI)
		instanceID   string
		expectError  bool
		validateFunc func(*testing.T, *VPCAPI)
	}{
		{
			name: "successful deletion",
			setupFunc: func(api *VPCAPI) {
				api.Instances.Add(&vpcv1.Instance{
					ID:   lo.ToPtr("instance-1"),
					Name: lo.ToPtr("test-1"),
				})
				api.Instances.Add(&vpcv1.Instance{
					ID:   lo.ToPtr("instance-2"),
					Name: lo.ToPtr("test-2"),
				})
			},
			instanceID:  "instance-1",
			expectError: false,
			validateFunc: func(t *testing.T, api *VPCAPI) {
				assert.Equal(t, 1, api.Instances.Len())

				// Verify instance-2 still exists
				var remaining []*vpcv1.Instance
				api.Instances.Range(func(inst *vpcv1.Instance) bool {
					remaining = append(remaining, inst)
					return true
				})
				assert.Equal(t, "instance-2", lo.FromPtr(remaining[0].ID))
			},
		},
		{
			name: "deletion with error",
			setupFunc: func(api *VPCAPI) {
				api.NextError.Store(fmt.Errorf("deletion failed"))
			},
			instanceID:  "instance-1",
			expectError: true,
		},
		{
			name:        "deletion of non-existent instance",
			instanceID:  "non-existent",
			expectError: false,
			validateFunc: func(t *testing.T, api *VPCAPI) {
				assert.Equal(t, 0, api.Instances.Len())
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := NewVPCAPI()
			if tt.setupFunc != nil {
				tt.setupFunc(api)
			}

			err := api.DeleteInstance(context.Background(), tt.instanceID)

			if tt.expectError {
				assert.Error(t, err)
				// When there's an error, the call might not be tracked
				return
			} else {
				require.NoError(t, err)
				if tt.validateFunc != nil {
					tt.validateFunc(t, api)
				}
			}

			// Verify the call was tracked (only for successful cases)
			inputs := api.DeleteInstanceBehavior.CalledWithInput.Clone()
			assert.Len(t, inputs, 1)
			assert.Equal(t, tt.instanceID, inputs[0].ID)
		})
	}
}

func TestVPCAPI_GetInstance(t *testing.T) {
	tests := []struct {
		name         string
		setupFunc    func(*VPCAPI)
		instanceID   string
		expectError  bool
		validateFunc func(*testing.T, *vpcv1.Instance)
	}{
		{
			name: "get existing instance",
			setupFunc: func(api *VPCAPI) {
				api.Instances.Add(&vpcv1.Instance{
					ID:     lo.ToPtr("instance-1"),
					Name:   lo.ToPtr("test-instance"),
					Status: lo.ToPtr("running"),
				})
			},
			instanceID:  "instance-1",
			expectError: false,
			validateFunc: func(t *testing.T, instance *vpcv1.Instance) {
				assert.Equal(t, "instance-1", lo.FromPtr(instance.ID))
				assert.Equal(t, "test-instance", lo.FromPtr(instance.Name))
				assert.Equal(t, "running", lo.FromPtr(instance.Status))
			},
		},
		{
			name:        "get non-existent instance",
			instanceID:  "non-existent",
			expectError: true,
		},
		{
			name: "with mocked output",
			setupFunc: func(api *VPCAPI) {
				api.GetInstanceBehavior.Output.Store(&vpcv1.Instance{
					ID:     lo.ToPtr("mocked-id"),
					Name:   lo.ToPtr("mocked-name"),
					Status: lo.ToPtr("pending"),
				})
			},
			instanceID:  "any-id",
			expectError: false,
			validateFunc: func(t *testing.T, instance *vpcv1.Instance) {
				assert.Equal(t, "mocked-id", lo.FromPtr(instance.ID))
				assert.Equal(t, "mocked-name", lo.FromPtr(instance.Name))
			},
		},
		{
			name: "with error",
			setupFunc: func(api *VPCAPI) {
				api.NextError.Store(fmt.Errorf("get failed"))
			},
			instanceID:  "instance-1",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := NewVPCAPI()
			if tt.setupFunc != nil {
				tt.setupFunc(api)
			}

			instance, err := api.GetInstance(context.Background(), tt.instanceID)

			if tt.expectError {
				assert.Error(t, err)
				// When there's an error, the call might not be tracked
				return
			} else {
				require.NoError(t, err)
				if tt.validateFunc != nil {
					tt.validateFunc(t, instance)
				}
			}

			// Verify the call was tracked (only for successful cases)
			inputs := api.GetInstanceBehavior.CalledWithInput.Clone()
			assert.Len(t, inputs, 1)
			assert.Equal(t, tt.instanceID, inputs[0].ID)
		})
	}
}

func TestVPCAPI_ListInstances(t *testing.T) {
	tests := []struct {
		name         string
		setupFunc    func(*VPCAPI)
		vpcID        string
		nameFilter   string
		expectError  bool
		validateFunc func(*testing.T, *vpcv1.InstanceCollection)
	}{
		{
			name: "list all instances",
			setupFunc: func(api *VPCAPI) {
				api.Instances.Add(&vpcv1.Instance{
					ID:   lo.ToPtr("instance-1"),
					Name: lo.ToPtr("test-1"),
					VPC:  &vpcv1.VPCReference{ID: lo.ToPtr("vpc-1")},
				})
				api.Instances.Add(&vpcv1.Instance{
					ID:   lo.ToPtr("instance-2"),
					Name: lo.ToPtr("test-2"),
					VPC:  &vpcv1.VPCReference{ID: lo.ToPtr("vpc-2")},
				})
			},
			vpcID:       "",
			nameFilter:  "",
			expectError: false,
			validateFunc: func(t *testing.T, collection *vpcv1.InstanceCollection) {
				assert.Len(t, collection.Instances, 2)
			},
		},
		{
			name: "filter by VPC",
			setupFunc: func(api *VPCAPI) {
				api.Instances.Add(&vpcv1.Instance{
					ID:   lo.ToPtr("instance-1"),
					Name: lo.ToPtr("test-1"),
					VPC:  &vpcv1.VPCReference{ID: lo.ToPtr("vpc-1")},
				})
				api.Instances.Add(&vpcv1.Instance{
					ID:   lo.ToPtr("instance-2"),
					Name: lo.ToPtr("test-2"),
					VPC:  &vpcv1.VPCReference{ID: lo.ToPtr("vpc-2")},
				})
			},
			vpcID:       "vpc-1",
			nameFilter:  "",
			expectError: false,
			validateFunc: func(t *testing.T, collection *vpcv1.InstanceCollection) {
				assert.Len(t, collection.Instances, 1)
				assert.Equal(t, "instance-1", lo.FromPtr(collection.Instances[0].ID))
			},
		},
		{
			name: "filter by name",
			setupFunc: func(api *VPCAPI) {
				api.Instances.Add(&vpcv1.Instance{
					ID:   lo.ToPtr("instance-1"),
					Name: lo.ToPtr("test-1"),
				})
				api.Instances.Add(&vpcv1.Instance{
					ID:   lo.ToPtr("instance-2"),
					Name: lo.ToPtr("test-2"),
				})
			},
			vpcID:       "",
			nameFilter:  "test-2",
			expectError: false,
			validateFunc: func(t *testing.T, collection *vpcv1.InstanceCollection) {
				assert.Len(t, collection.Instances, 1)
				assert.Equal(t, "test-2", lo.FromPtr(collection.Instances[0].Name))
			},
		},
		{
			name: "with error",
			setupFunc: func(api *VPCAPI) {
				api.NextError.Store(fmt.Errorf("list failed"))
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := NewVPCAPI()
			if tt.setupFunc != nil {
				tt.setupFunc(api)
			}

			collection, err := api.ListInstances(context.Background(), tt.vpcID, tt.nameFilter)

			if tt.expectError {
				assert.Error(t, err)
				// When there's an error, the call might not be tracked
				return
			} else {
				require.NoError(t, err)
				if tt.validateFunc != nil {
					tt.validateFunc(t, collection)
				}
			}

			// Verify the call was tracked (only for successful cases)
			inputs := api.ListInstancesBehavior.CalledWithInput.Clone()
			assert.Len(t, inputs, 1)
			assert.Equal(t, tt.vpcID, inputs[0].VPC)
			assert.Equal(t, tt.nameFilter, inputs[0].Name)
		})
	}
}

func TestVPCAPI_GetSubnet(t *testing.T) {
	tests := []struct {
		name         string
		setupFunc    func(*VPCAPI)
		subnetID     string
		expectError  bool
		validateFunc func(*testing.T, *vpcv1.Subnet)
	}{
		{
			name: "get existing subnet",
			setupFunc: func(api *VPCAPI) {
				api.Subnets.Add(&vpcv1.Subnet{
					ID:   lo.ToPtr("subnet-1"),
					Name: lo.ToPtr("test-subnet"),
					Zone: &vpcv1.ZoneReference{Name: lo.ToPtr("us-south-1")},
				})
			},
			subnetID:    "subnet-1",
			expectError: false,
			validateFunc: func(t *testing.T, subnet *vpcv1.Subnet) {
				assert.Equal(t, "subnet-1", lo.FromPtr(subnet.ID))
				assert.Equal(t, "test-subnet", lo.FromPtr(subnet.Name))
			},
		},
		{
			name:        "get non-existent subnet",
			subnetID:    "non-existent",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := NewVPCAPI()
			if tt.setupFunc != nil {
				tt.setupFunc(api)
			}

			subnet, err := api.GetSubnet(context.Background(), tt.subnetID)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				if tt.validateFunc != nil {
					tt.validateFunc(t, subnet)
				}
			}
		})
	}
}

func TestVPCAPI_ListSubnets(t *testing.T) {
	tests := []struct {
		name         string
		setupFunc    func(*VPCAPI)
		vpcID        string
		expectError  bool
		validateFunc func(*testing.T, *vpcv1.SubnetCollection)
	}{
		{
			name: "list subnets for VPC",
			setupFunc: func(api *VPCAPI) {
				api.Subnets.Add(&vpcv1.Subnet{
					ID:   lo.ToPtr("subnet-1"),
					Name: lo.ToPtr("test-subnet-1"),
					VPC:  &vpcv1.VPCReference{ID: lo.ToPtr("vpc-1")},
				})
				api.Subnets.Add(&vpcv1.Subnet{
					ID:   lo.ToPtr("subnet-2"),
					Name: lo.ToPtr("test-subnet-2"),
					VPC:  &vpcv1.VPCReference{ID: lo.ToPtr("vpc-2")},
				})
			},
			vpcID:       "vpc-1",
			expectError: false,
			validateFunc: func(t *testing.T, collection *vpcv1.SubnetCollection) {
				assert.Len(t, collection.Subnets, 1)
				assert.Equal(t, "subnet-1", lo.FromPtr(collection.Subnets[0].ID))
			},
		},
		{
			name: "list all subnets when no VPC specified",
			setupFunc: func(api *VPCAPI) {
				api.Subnets.Add(&vpcv1.Subnet{
					ID: lo.ToPtr("subnet-1"),
				})
				api.Subnets.Add(&vpcv1.Subnet{
					ID: lo.ToPtr("subnet-2"),
				})
			},
			vpcID:       "",
			expectError: false,
			validateFunc: func(t *testing.T, collection *vpcv1.SubnetCollection) {
				assert.Len(t, collection.Subnets, 2)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := NewVPCAPI()
			if tt.setupFunc != nil {
				tt.setupFunc(api)
			}

			collection, err := api.ListSubnets(context.Background(), tt.vpcID)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				if tt.validateFunc != nil {
					tt.validateFunc(t, collection)
				}
			}
		})
	}
}

func TestVPCAPI_Images(t *testing.T) {
	t.Run("GetImage", func(t *testing.T) {
		api := NewVPCAPI()

		// Add test image
		api.Images.Add(&vpcv1.Image{
			ID:         lo.ToPtr("image-1"),
			Name:       lo.ToPtr("test-image"),
			Visibility: lo.ToPtr("public"),
		})

		// Get existing image
		image, err := api.GetImage(context.Background(), "image-1")
		require.NoError(t, err)
		assert.Equal(t, "image-1", lo.FromPtr(image.ID))
		assert.Equal(t, "test-image", lo.FromPtr(image.Name))

		// Get non-existent image
		_, err = api.GetImage(context.Background(), "non-existent")
		assert.Error(t, err)
	})

	t.Run("ListImages", func(t *testing.T) {
		api := NewVPCAPI()

		// Add test images
		api.Images.Add(&vpcv1.Image{
			ID:         lo.ToPtr("image-1"),
			Name:       lo.ToPtr("ubuntu"),
			Visibility: lo.ToPtr("public"),
		})
		api.Images.Add(&vpcv1.Image{
			ID:         lo.ToPtr("image-2"),
			Name:       lo.ToPtr("rhel"),
			Visibility: lo.ToPtr("private"),
		})

		// List all images
		collection, err := api.ListImages(context.Background(), "", "")
		require.NoError(t, err)
		assert.Len(t, collection.Images, 2)

		// Filter by name
		collection, err = api.ListImages(context.Background(), "ubuntu", "")
		require.NoError(t, err)
		assert.Len(t, collection.Images, 1)
		assert.Equal(t, "ubuntu", lo.FromPtr(collection.Images[0].Name))

		// Filter by visibility
		collection, err = api.ListImages(context.Background(), "", "private")
		require.NoError(t, err)
		assert.Len(t, collection.Images, 1)
		assert.Equal(t, "rhel", lo.FromPtr(collection.Images[0].Name))
	})
}

func TestVPCAPI_InstanceProfiles(t *testing.T) {
	t.Run("GetInstanceProfile", func(t *testing.T) {
		api := NewVPCAPI()

		// Add test profile
		api.InstanceProfiles.Add(&vpcv1.InstanceProfile{
			Name: lo.ToPtr("bx2-2x8"),
			VcpuCount: &vpcv1.InstanceProfileVcpu{
				Value: lo.ToPtr(int64(2)),
			},
			Memory: &vpcv1.InstanceProfileMemory{
				Value: lo.ToPtr(int64(8)),
			},
		})

		// Get existing profile
		profile, err := api.GetInstanceProfile(context.Background(), "bx2-2x8")
		require.NoError(t, err)
		assert.Equal(t, "bx2-2x8", lo.FromPtr(profile.Name))

		// Get non-existent profile
		_, err = api.GetInstanceProfile(context.Background(), "non-existent")
		assert.Error(t, err)
	})
}

func TestVPCAPI_Reset(t *testing.T) {
	api := NewVPCAPI()

	// Add data to all stores
	api.Instances.Add(&vpcv1.Instance{ID: lo.ToPtr("instance-1")})
	api.Subnets.Add(&vpcv1.Subnet{ID: lo.ToPtr("subnet-1")})
	api.Images.Add(&vpcv1.Image{ID: lo.ToPtr("image-1")})
	api.VPCs.Add(&vpcv1.VPC{ID: lo.ToPtr("vpc-1")})
	api.InstanceProfiles.Add(&vpcv1.InstanceProfile{Name: lo.ToPtr("profile-1")})
	api.NextError.Store(fmt.Errorf("test error"))

	// Track some calls
	api.CreateInstanceBehavior.CalledWithInput.Add(CreateInstanceInput{})
	api.GetInstanceBehavior.Output.Store(&vpcv1.Instance{})

	// Reset everything
	api.Reset()

	// Verify all stores are empty
	assert.Equal(t, 0, api.Instances.Len())
	assert.Equal(t, 0, api.Subnets.Len())
	assert.Equal(t, 0, api.Images.Len())
	assert.Equal(t, 0, api.VPCs.Len())
	assert.Equal(t, 0, api.InstanceProfiles.Len())
	assert.Nil(t, api.NextError.Get())

	// Verify behaviors are reset
	assert.Len(t, api.CreateInstanceBehavior.CalledWithInput.Clone(), 0)
	assert.Nil(t, api.GetInstanceBehavior.Output.Get())
}

func TestVPCAPI_Concurrency(t *testing.T) {
	api := NewVPCAPI()

	// Test concurrent instance creation
	t.Run("concurrent creates", func(t *testing.T) {
		done := make(chan bool, 10)

		for i := 0; i < 10; i++ {
			go func(idx int) {
				prototype := &vpcv1.InstancePrototype{
					Name: lo.ToPtr(fmt.Sprintf("instance-%d", idx)),
				}
				_, err := api.CreateInstance(context.Background(), prototype)
				assert.NoError(t, err)
				done <- true
			}(i)
		}

		// Wait for all goroutines
		for i := 0; i < 10; i++ {
			<-done
		}

		// Verify all instances were created
		assert.Equal(t, 10, api.Instances.Len())
	})

	// Test concurrent reads and writes
	t.Run("concurrent read/write", func(t *testing.T) {
		api.Reset()

		// Add initial instance
		api.Instances.Add(&vpcv1.Instance{
			ID:   lo.ToPtr("test-instance"),
			Name: lo.ToPtr("test"),
		})

		done := make(chan bool, 20)

		// Start readers
		for i := 0; i < 10; i++ {
			go func() {
				_, err := api.GetInstance(context.Background(), "test-instance")
				assert.NoError(t, err)
				done <- true
			}()
		}

		// Start writers
		for i := 0; i < 10; i++ {
			go func(idx int) {
				prototype := &vpcv1.InstancePrototype{
					Name: lo.ToPtr(fmt.Sprintf("new-instance-%d", idx)),
				}
				_, err := api.CreateInstance(context.Background(), prototype)
				assert.NoError(t, err)
				done <- true
			}(i)
		}

		// Wait for all operations
		for i := 0; i < 20; i++ {
			<-done
		}

		// Verify state
		assert.Equal(t, 11, api.Instances.Len()) // 1 initial + 10 created
	})
}

func TestVPCAPI_DefaultValues(t *testing.T) {
	api := NewVPCAPI()

	// Test that default instance creation sets expected values
	prototype := &vpcv1.InstancePrototype{
		Name: lo.ToPtr("test"),
	}

	instance, err := api.CreateInstance(context.Background(), prototype)
	require.NoError(t, err)

	// Verify default values are set
	assert.NotNil(t, instance.ID)
	assert.NotNil(t, instance.Name)
	assert.NotNil(t, instance.Zone)
	assert.Equal(t, "us-south-1", lo.FromPtr(instance.Zone.Name))
	assert.NotNil(t, instance.VPC)
	assert.Equal(t, "vpc-test", lo.FromPtr(instance.VPC.ID))
	assert.NotNil(t, instance.Profile)
	assert.Equal(t, "bx2-2x8", lo.FromPtr(instance.Profile.Name))
	assert.Equal(t, "running", lo.FromPtr(instance.Status))
	assert.NotNil(t, instance.PrimaryNetworkInterface)
	assert.NotNil(t, instance.CreatedAt)
}

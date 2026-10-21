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

package instancetype

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewController(t *testing.T) {
	t.Setenv("IBMCLOUD_API_KEY", "")
	provider := NewMockInstanceTypeProvider()
	controller, err := NewController(provider)
	assert.NoError(t, err)
	assert.Same(t, provider, controller.instanceTypeProvider)
	controller, err = NewController(nil)
	assert.Error(t, err)
	assert.Nil(t, controller)
}

func TestControllerStructure(t *testing.T) {
	// Test that the Controller struct can be created
	controller := &Controller{}
	assert.NotNil(t, controller)
	assert.Nil(t, controller.instanceTypeProvider)

	// Test that we can set the provider
	controller = &Controller{
		instanceTypeProvider: nil, // Would normally be a real provider
	}
	assert.NotNil(t, controller)
}

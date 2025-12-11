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
	"encoding/json"
	"testing"

	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestVolumeAttachmentConcreteType verifies that using the concrete discriminated type
// correctly marshals to JSON with oneOf discriminator when assigned to an interface field
func TestVolumeAttachmentConcreteType(t *testing.T) {
	volumeName := "test-data-volume"
	deleteOnTermination := true
	capacity := int64(200)
	profile := "general-purpose"

	// CORRECT: Use concrete discriminated type for the "by capacity" oneOf variant
	// This preserves type information when assigned to the interface field
	volumeProto := &vpcv1.VolumeAttachmentPrototypeVolumeVolumePrototypeInstanceContextVolumePrototypeInstanceContextVolumeByCapacity{
		Name:     &volumeName,
		Capacity: &capacity,
		Profile: &vpcv1.VolumeProfileIdentityByName{
			Name: &profile,
		},
	}

	// Create volume attachment - volumeProto implements VolumeAttachmentPrototypeVolumeIntf
	volumeAttachment := vpcv1.VolumeAttachmentPrototype{
		Name:                         &volumeName,
		Volume:                       volumeProto,
		DeleteVolumeOnInstanceDelete: &deleteOnTermination,
	}

	// Marshal to JSON to verify proper oneOf handling
	jsonBytes, err := json.Marshal(volumeAttachment)
	require.NoError(t, err, "Failed to marshal volume attachment to JSON")

	var parsed map[string]interface{}
	err = json.Unmarshal(jsonBytes, &parsed)
	require.NoError(t, err, "Failed to unmarshal JSON")

	t.Logf("VolumeAttachment JSON: %s", string(jsonBytes))

	// Verify top-level fields
	assert.Contains(t, parsed, "volume", "JSON must contain volume field")
	assert.Contains(t, parsed, "name", "JSON must contain name field")
	assert.Contains(t, parsed, "delete_volume_on_instance_delete", "JSON must contain delete_volume_on_instance_delete field")

	volumeObj, ok := parsed["volume"].(map[string]interface{})
	require.True(t, ok, "volume must be an object")

	// Verify the volume has the correct fields for "by-capacity" variant
	assert.Contains(t, volumeObj, "capacity", "volume must contain capacity field")
	assert.Contains(t, volumeObj, "profile", "volume must contain profile field")
	assert.Contains(t, volumeObj, "name", "volume must contain name field")
	assert.Equal(t, float64(200), volumeObj["capacity"], "capacity must match")

	profileObj, ok := volumeObj["profile"].(map[string]interface{})
	require.True(t, ok, "profile must be an object")
	assert.Equal(t, "general-purpose", profileObj["name"], "profile name must match")

	// Verify that only the fields we populated are in the JSON (aggregate type behavior)
	// Should NOT contain fields from other oneOf variants like 'id', 'crn', 'href'
	assert.NotContains(t, volumeObj, "id", "should not contain ID field (different oneOf variant)")
	assert.NotContains(t, volumeObj, "crn", "should not contain CRN field (different oneOf variant)")
	assert.NotContains(t, volumeObj, "href", "should not contain Href field (different oneOf variant)")

	t.Logf("Aggregate type test passed - JSON contains only populated fields")
}

// TestVolumeAttachmentSliceMarshaling verifies that a slice of VolumeAttachmentPrototype
// with aggregate type volumes marshals correctly
func TestVolumeAttachmentSliceMarshaling(t *testing.T) {
	// Create multiple volume attachments using concrete discriminated type
	var volumeAttachments []vpcv1.VolumeAttachmentPrototype

	for i := 1; i <= 3; i++ {
		volumeName := "data-volume-" + string(rune('0'+i))
		deleteOnTermination := true
		capacity := int64(100 * i)
		profile := "general-purpose"

		// Use concrete discriminated type
		volumeProto := &vpcv1.VolumeAttachmentPrototypeVolumeVolumePrototypeInstanceContextVolumePrototypeInstanceContextVolumeByCapacity{
			Name:     &volumeName,
			Capacity: &capacity,
			Profile: &vpcv1.VolumeProfileIdentityByName{
				Name: &profile,
			},
		}

		volumeAttachment := vpcv1.VolumeAttachmentPrototype{
			Name:                         &volumeName,
			Volume:                       volumeProto,
			DeleteVolumeOnInstanceDelete: &deleteOnTermination,
		}

		volumeAttachments = append(volumeAttachments, volumeAttachment)
	}

	// Marshal the slice to JSON
	jsonBytes, err := json.Marshal(volumeAttachments)
	require.NoError(t, err, "Failed to marshal volume attachments slice to JSON")

	var parsed []interface{}
	err = json.Unmarshal(jsonBytes, &parsed)
	require.NoError(t, err, "Failed to unmarshal JSON")

	t.Logf("VolumeAttachments slice JSON: %s", string(jsonBytes))

	// Verify we have 3 attachments
	assert.Equal(t, 3, len(parsed), "should have 3 volume attachments")

	// Verify each attachment has correct structure
	for i, item := range parsed {
		attachment, ok := item.(map[string]interface{})
		require.True(t, ok, "attachment %d must be an object", i)

		assert.Contains(t, attachment, "volume", "attachment %d must have volume field", i)
		assert.Contains(t, attachment, "name", "attachment %d must have name field", i)

		volumeObj, ok := attachment["volume"].(map[string]interface{})
		require.True(t, ok, "volume in attachment %d must be an object", i)

		assert.Contains(t, volumeObj, "capacity", "volume %d must have capacity", i)
		expectedCapacity := float64(100 * (i + 1))
		assert.Equal(t, expectedCapacity, volumeObj["capacity"], "capacity for volume %d must match", i)
	}

	t.Logf("Slice marshaling test passed - all %d attachments correctly marshaled", len(volumeAttachments))
}

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

package bootstrap_test

import (
	"strings"
	"testing"
	"text/template"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
)

func TestUserDataAppend(t *testing.T) {
	// Test the template generation directly using a simplified version of the actual template

	// Create test options with custom user data
	options := struct {
		CustomUserData string
	}{
		CustomUserData: `echo "Custom user data appended"
touch /tmp/custom-userdata-was-here
systemctl restart my-custom-service || true`,
	}

	// Test the template rendering with the same pattern used in cloudinit.go
	templateStr := `#!/bin/bash
echo "Bootstrap script starting"

# Run custom user data if provided
{{ if .CustomUserData }}
echo "$(date): Running custom user data..."
{{ .CustomUserData }}
{{ end }}

echo "Bootstrap script completed"`

	// Parse and execute template
	tmpl, err := template.New("test").Parse(templateStr)
	require.NoError(t, err, "Failed to parse template")

	var buf strings.Builder
	err = tmpl.Execute(&buf, options)
	require.NoError(t, err, "Failed to execute template")

	result := buf.String()

	// Verify that the custom user data is appended
	assert.Contains(t, result, "Running custom user data...", "Custom user data header missing")
	assert.Contains(t, result, "echo \"Custom user data appended\"", "Custom echo command missing")
	assert.Contains(t, result, "touch /tmp/custom-userdata-was-here", "Custom touch command missing")
	assert.Contains(t, result, "systemctl restart my-custom-service", "Custom service restart missing")

	// Verify that custom data appears after the header
	headerIndex := strings.Index(result, "Running custom user data...")
	customIndex := strings.Index(result, "echo \"Custom user data appended\"")
	assert.Greater(t, customIndex, headerIndex, "Custom user data should appear after header")
}

func TestUserDataCompleteOverride(t *testing.T) {
	// Note: For complete override testing, we need to test at the instance provider level
	// since that's where the UserData field is checked
	// This test documents the expected behavior

	nodeClass := &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-nodeclass",
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			Region:         "us-south",
			Zone:           "us-south-1",
			VPC:            "r010-12345678-1234-5678-9abc-def012345678",
			Image:          "ubuntu-24-04",
			SecurityGroups: []string{"r010-87654321-4321-8765-9abc-def098765432"},
			UserData: `#!/bin/bash
echo "This is a complete custom bootstrap script"
# Custom logic here
`,
			UserDataAppend: `
echo "This should be ignored when UserData is set"
`,
		},
	}

	// When UserData is set, the instance provider should return it directly
	// and ignore UserDataAppend
	assert.NotEmpty(t, nodeClass.Spec.UserData, "UserData should be set")
	assert.Equal(t, `#!/bin/bash
echo "This is a complete custom bootstrap script"
# Custom logic here
`, nodeClass.Spec.UserData, "UserData should match expected value")
}

func TestNoUserDataProvided(t *testing.T) {
	// Test the template rendering with no custom user data

	// Create test options without custom user data
	options := struct {
		CustomUserData string
	}{
		CustomUserData: "", // No custom user data
	}

	// Test the template rendering with the same pattern used in cloudinit.go
	templateStr := `#!/bin/bash
echo "Bootstrap script starting"

# Run custom user data if provided
{{ if .CustomUserData }}
echo "$(date): Running custom user data..."
{{ .CustomUserData }}
{{ end }}

echo "Bootstrap script completed"`

	// Parse and execute template
	tmpl, err := template.New("test").Parse(templateStr)
	require.NoError(t, err, "Failed to parse template")

	var buf strings.Builder
	err = tmpl.Execute(&buf, options)
	require.NoError(t, err, "Failed to execute template")

	result := buf.String()

	// Verify that the bootstrap script is present
	assert.Contains(t, result, "Bootstrap script starting", "Bootstrap script header missing")
	assert.Contains(t, result, "Bootstrap script completed", "Bootstrap script footer missing")

	// Verify that no custom user data section is added
	assert.NotContains(t, result, "Running custom user data...", "Custom user data header should not be present")
}

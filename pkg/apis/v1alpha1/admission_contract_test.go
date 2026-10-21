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

package v1alpha1

import (
	"os"
	"regexp"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

func TestGeneratedStorageAndImageAdmission(t *testing.T) {
	data, err := os.ReadFile("../../../charts/crds/karpenter-ibm.sh_ibmnodeclasses.yaml")
	require.NoError(t, err)
	var crd map[string]interface{}
	require.NoError(t, yaml.Unmarshal(data, &crd))
	spec := crd["spec"].(map[string]interface{})
	version := spec["versions"].([]interface{})[0].(map[string]interface{})
	root := version["schema"].(map[string]interface{})["openAPIV3Schema"].(map[string]interface{})
	nodeSpec := root["properties"].(map[string]interface{})["spec"].(map[string]interface{})
	env, err := cel.NewEnv(cel.Variable("self", cel.DynType))
	require.NoError(t, err)
	rules := map[string]cel.Program{}
	for _, entry := range nodeSpec["x-kubernetes-validations"].([]interface{}) {
		rule := entry.(map[string]interface{})
		message := rule["message"].(string)
		if message != "at most one block device mapping can be a root volume" && message != "either image or imageSelector must be specified for VPC nodes" && message != "block device attachment names must be unique" {
			continue
		}
		ast, issues := env.Compile(rule["rule"].(string))
		require.NoError(t, issues.Err())
		program, err := env.Program(ast)
		require.NoError(t, err)
		rules[message] = program
	}
	require.Len(t, rules, 3)
	for _, test := range []struct {
		name, message string
		spec          map[string]interface{}
		accepted      bool
	}{
		{"default root", "at most one block device mapping can be a root volume", map[string]interface{}{}, true},
		{"data-only mappings", "at most one block device mapping can be a root volume", map[string]interface{}{"blockDeviceMappings": []interface{}{map[string]interface{}{"deviceName": "data"}}}, true},
		{"single root", "at most one block device mapping can be a root volume", map[string]interface{}{"blockDeviceMappings": []interface{}{map[string]interface{}{"rootVolume": true}, map[string]interface{}{"rootVolume": false}}}, true},
		{"duplicate roots", "at most one block device mapping can be a root volume", map[string]interface{}{"blockDeviceMappings": []interface{}{map[string]interface{}{"rootVolume": true}, map[string]interface{}{"rootVolume": true}}}, false},
		{"default attachment names", "block device attachment names must be unique", map[string]interface{}{"blockDeviceMappings": []interface{}{map[string]interface{}{}, map[string]interface{}{}}}, true},
		{"unique attachments", "block device attachment names must be unique", map[string]interface{}{"blockDeviceMappings": []interface{}{map[string]interface{}{"deviceName": "data-1"}, map[string]interface{}{"deviceName": "data-2"}}}, true},
		{"duplicate attachments", "block device attachment names must be unique", map[string]interface{}{"blockDeviceMappings": []interface{}{map[string]interface{}{"deviceName": "data"}, map[string]interface{}{"deviceName": "data"}}}, false},
		{"VPC missing image", "either image or imageSelector must be specified for VPC nodes", map[string]interface{}{}, false},
		{"explicit IKS", "either image or imageSelector must be specified for VPC nodes", map[string]interface{}{"bootstrapMode": "iks-api", "iksClusterID": "cluster"}, true},
		{"automatic class IKS", "either image or imageSelector must be specified for VPC nodes", map[string]interface{}{"bootstrapMode": "auto", "iksClusterID": "cluster"}, true},
		{"explicit VPC beats IKS ID", "either image or imageSelector must be specified for VPC nodes", map[string]interface{}{"bootstrapMode": "cloud-init", "iksClusterID": "cluster"}, false},
		{"VPC image", "either image or imageSelector must be specified for VPC nodes", map[string]interface{}{"image": "image"}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			value, _, err := rules[test.message].Eval(map[string]interface{}{"self": test.spec})
			require.NoError(t, err)
			require.Equal(t, test.accepted, value.Value())
		})
	}
	mapping := nodeSpec["properties"].(map[string]interface{})["blockDeviceMappings"].(map[string]interface{})["items"].(map[string]interface{})
	name := mapping["properties"].(map[string]interface{})["deviceName"].(map[string]interface{})
	pattern, err := regexp.Compile(name["pattern"].(string))
	require.NoError(t, err)
	require.True(t, pattern.MatchString("data-1"))
	require.False(t, pattern.MatchString("/dev/sdf"))
	require.False(t, pattern.MatchString("data-"))
	require.EqualValues(t, 63, name["maxLength"])
	price := nodeSpec["properties"].(map[string]interface{})["instanceRequirements"].(map[string]interface{})["properties"].(map[string]interface{})["maximumHourlyPrice"].(map[string]interface{})
	pricePattern, err := regexp.Compile(price["pattern"].(string))
	require.NoError(t, err)
	for _, test := range []struct {
		price    string
		accepted bool
	}{
		{"0", true},
		{"1", true},
		{"0.50", true},
		{"1.00", true},
		{"1234.567", true},
		{"", false},
		{"-1", false},
		{".50", false},
		{"1.", false},
		{"1e3", false},
		{"NaN", false},
		{"1.00 USD", false},
		{" 1.00", false},
	} {
		t.Run("maximum hourly price "+test.price, func(t *testing.T) {
			require.Equal(t, test.accepted, pricePattern.MatchString(test.price))
		})
	}
}

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

package bootstrap

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	commonTypes "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/types"
)

func safeBootstrapOptions() commonTypes.Options {
	return commonTypes.Options{
		ClusterEndpoint: "https://cluster.example.com:6443", BootstrapToken: "abcdef.0123456789abcdef",
		DNSClusterIP: "10.96.0.10", Region: "us-south", Zone: "us-south-1", NodeName: "claim-001",
		KubernetesVersion: "v1.34.0", ContainerRuntime: "containerd", CNIPlugin: "calico", CNIVersion: "v3.26.0", Architecture: "amd64",
	}
}

func bootstrapScript(t *testing.T, options commonTypes.Options) string {
	t.Helper()
	p := NewVPCBootstrapProvider(nil, nil, nil)
	script, err := p.generateCloudInitScript(context.Background(), options)
	require.NoError(t, err)
	return script
}

func bootstrapFragment(t *testing.T, script, start, end string) string {
	t.Helper()
	_, fragment, found := strings.Cut(script, start)
	require.True(t, found, "missing fragment start")
	fragment, _, found = strings.Cut(fragment, end)
	require.True(t, found, "missing fragment end")
	return fragment
}

func runBootstrapFragment(t *testing.T, fragment string, environment ...string) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "bash", "-c", "set -euo pipefail\n"+fragment)
	command.Env = append(os.Environ(), environment...)
	return command.CombinedOutput()
}

func TestBootstrapRejectsUntrustedLabelsAndStaticShellValues(t *testing.T) {
	cases := map[string]func(*commonTypes.Options){
		"label command":  func(o *commonTypes.Options) { o.Labels = map[string]string{"example.com/key": "$(touch marker)"} },
		"label heredoc":  func(o *commonTypes.Options) { o.Labels = map[string]string{"example.com/key": "value\nEOF\nfalse"} },
		"label argument": func(o *commonTypes.Options) { o.Labels = map[string]string{"example.com/key": "value,other=1"} },
		"label key":      func(o *commonTypes.Options) { o.Labels = map[string]string{"example.com/$(false)": "value"} },
		"taint value": func(o *commonTypes.Options) {
			o.Taints = []corev1.Taint{{Key: "example.com/key", Value: "$(false)", Effect: corev1.TaintEffectNoSchedule}}
		},
		"taint effect": func(o *commonTypes.Options) {
			o.Taints = []corev1.Taint{{Key: "example.com/key", Effect: "NoSchedule\nEOF"}}
		},
		"endpoint":      func(o *commonTypes.Options) { o.ClusterEndpoint = "https://cluster.example.com/$(false)" },
		"node name":     func(o *commonTypes.Options) { o.NodeName = "node'$(false)'" },
		"DNS address":   func(o *commonTypes.Options) { o.DNSClusterIP = "10.96.0.10\nEOF" },
		"version":       func(o *commonTypes.Options) { o.KubernetesVersion = "v1.34.0; false" },
		"CNI command":   func(o *commonTypes.Options) { o.CNIVersion = "$(false)" },
		"CNI separator": func(o *commonTypes.Options) { o.CNIVersion = "latest;false" },
		"CNI newline":   func(o *commonTypes.Options) { o.CNIVersion = "latest\nEOF" },
		"CNI length":    func(o *commonTypes.Options) { o.CNIVersion = strings.Repeat("a", 257) },
		"reserved quantity": func(o *commonTypes.Options) {
			o.KubeletConfig = &v1alpha1.KubeletConfiguration{KubeReserved: map[string]string{"memory": "1Gi\nEOF\nfalse"}}
		},
	}
	p := NewVPCBootstrapProvider(nil, nil, nil)
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			options := safeBootstrapOptions()
			mutate(&options)
			script, err := p.generateCloudInitScript(context.Background(), options)
			require.Error(t, err)
			require.Empty(t, script)
		})
	}
}

func TestBootstrapLabelsReachKubeletAndNativePodCapacityIsExplicit(t *testing.T) {
	options := safeBootstrapOptions()
	options.Labels = map[string]string{"example.com/z": "two", "example.com/a": "one"}
	script := bootstrapScript(t, options)
	assignment := ""
	for _, line := range strings.Split(script, "\n") {
		if strings.HasPrefix(line, "NODE_LABELS=") {
			assignment = line
		}
	}
	require.NotEmpty(t, assignment)
	fragment := bootstrapFragment(t, script, "# Configure kubelet service with bootstrap kubeconfig\n", "# Create kubelet service override\n")
	path := filepath.Join(t.TempDir(), "kubelet.conf")
	fragment = strings.ReplaceAll(fragment, "/etc/systemd/system/kubelet.service.d/10-karpenter.conf", `"$KUBELET_DROPIN"`)
	output, err := runBootstrapFragment(t, assignment+"\n"+fragment, "KUBELET_DROPIN="+path, "HOSTNAME=claim-001", "PRIVATE_IP=10.0.0.1", "PROVIDER_ID=ibm:///us-south/instance")
	require.NoError(t, err, "%s", output)
	configuration, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(configuration), "--node-labels=example.com/a=one,example.com/z=two ")

	config := bootstrapFragment(t, script, "cat > /var/lib/kubelet/config.yaml << 'EOF'\n", "\nEOF\n")
	decoded := map[string]interface{}{}
	require.NoError(t, yaml.Unmarshal([]byte(config), &decoded))
	require.Equal(t, float64(110), decoded["maxPods"])
	require.Equal(t, []interface{}{"10.96.0.10"}, decoded["clusterDNS"])
	maxPods := int32(10)
	options.KubeletConfig = &v1alpha1.KubeletConfiguration{MaxPods: &maxPods}
	config = bootstrapFragment(t, bootstrapScript(t, options), "cat > /var/lib/kubelet/config.yaml << 'EOF'\n", "\nEOF\n")
	require.NoError(t, yaml.Unmarshal([]byte(config), &decoded))
	require.Equal(t, float64(10), decoded["maxPods"])
}

func TestBootstrapEnvironmentAndCertificateBytesRemainLiteral(t *testing.T) {
	directory := t.TempDir()
	payload := "literal 'quoted'\nEOF\n$(touch \"$BOOTSTRAP_TEST_MARKER\")\n`false`"
	t.Setenv("BOOTSTRAP_TEST_LITERAL", payload)
	t.Setenv("ca_crt", payload)
	options := safeBootstrapOptions()
	options.CABundle = payload
	options.AdditionalCAs = []string{payload}
	options.KubeletClientCAs = []string{payload}
	script := bootstrapScript(t, options)
	exports, _, found := strings.Cut(script, "# Enhanced logging\n")
	require.True(t, found)
	certificates := bootstrapFragment(t, script, "# Write primary CA certificate\n", "# Allow override of CA trust via environment variable")
	certificates = strings.NewReplacer("/etc/kubernetes/pki/ca.crt", `"$BOOTSTRAP_TEST_ROOT/ca.crt"`, "/etc/kubernetes/pki/kubelet-client-ca.crt", `"$BOOTSTRAP_TEST_ROOT/client-ca.crt"`).Replace(certificates)
	fragment := exports + certificates + `
printf '%s' "$BOOTSTRAP_TEST_LITERAL" > "$BOOTSTRAP_TEST_ROOT/export"
printf '%s' "$KARPENTER_ADDITIONAL_CA" > "$BOOTSTRAP_TEST_ROOT/extra-ca"
`
	marker := filepath.Join(directory, "executed")
	output, err := runBootstrapFragment(t, fragment, "BOOTSTRAP_TEST_ROOT="+directory, "BOOTSTRAP_TEST_MARKER="+marker)
	require.NoError(t, err, "%s", output)
	for name, expected := range map[string]string{"ca.crt": payload + "\n", "client-ca.crt": strings.Repeat(payload+"\n", 3), "export": payload, "extra-ca": payload} {
		actual, readErr := os.ReadFile(filepath.Join(directory, name))
		require.NoError(t, readErr)
		require.Equal(t, expected, string(actual))
	}
	_, err = os.Stat(marker)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestMetadataRetriesTransientFailuresAndReportsPermanentFailure(t *testing.T) {
	script := bootstrapScript(t, safeBootstrapOptions())
	fragment := "metadata_value() {" + bootstrapFragment(t, script, "metadata_value() {", "unset INSTANCE_IDENTITY_TOKEN")
	for _, mode := range []string{"transient", "token-failure", "id-failure"} {
		t.Run(mode, func(t *testing.T) {
			directory := t.TempDir()
			stubs := `
curl() {
    local kind=id count=0
    if [[ "$*" == *instance_identity/v1/token* ]]; then kind=token; fi
    local counter="$BOOTSTRAP_TEST_ROOT/$kind-count"
    if [[ -f "$counter" ]]; then count=$(< "$counter"); fi
    count=$((count + 1))
    printf '%s' "$count" > "$counter"
    if [[ "$BOOTSTRAP_TEST_MODE" == token-failure && "$kind" == token ]]; then return 22; fi
    if [[ "$kind" == token ]]; then
        if [[ "$BOOTSTRAP_TEST_MODE" == transient && "$count" -lt 3 ]]; then return 22; fi
        printf '%s' '{"access_token":"valid.identity-token"}'
    else
        [[ "$*" == *'Authorization: Bearer valid.identity-token'* ]] || return 23
        if [[ "$BOOTSTRAP_TEST_MODE" == id-failure ]]; then printf '%s' '{"id":"invalid-instance"}'; return; fi
        if [[ "$BOOTSTRAP_TEST_MODE" == transient && "$count" -lt 2 ]]; then printf '%s' '{invalid JSON'; return; fi
        printf '%s' '{"id":"02u7_11111111-1111-1111-1111-111111111111"}'
    fi
}
sleep() { printf x >> "$BOOTSTRAP_TEST_ROOT/sleeps"; }
report_status() { printf '%s/%s' "$1" "$2" > "$BOOTSTRAP_TEST_ROOT/status"; }
`
			output, err := runBootstrapFragment(t, stubs+fragment+`printf '%s' "$INSTANCE_ID" > "$BOOTSTRAP_TEST_ROOT/instance"`, "BOOTSTRAP_TEST_ROOT="+directory, "BOOTSTRAP_TEST_MODE="+mode)
			if mode == "transient" {
				require.NoError(t, err, "%s", output)
				instance, readErr := os.ReadFile(filepath.Join(directory, "instance"))
				require.NoError(t, readErr)
				require.Equal(t, "02u7_11111111-1111-1111-1111-111111111111", string(instance))
				for name, expected := range map[string]string{"token-count": "3", "id-count": "2", "sleeps": "xxx"} {
					actual, readErr := os.ReadFile(filepath.Join(directory, name))
					require.NoError(t, readErr)
					require.Equal(t, expected, string(actual))
				}
				return
			}
			require.Error(t, err)
			phase, readErr := os.ReadFile(filepath.Join(directory, "status"))
			require.NoError(t, readErr)
			kind, expected := "token", "failed/instance-identity-token-failed"
			if mode == "id-failure" {
				kind, expected = "id", "failed/instance-id-metadata-failed"
			}
			require.Equal(t, expected, string(phase))
			attempts, readErr := os.ReadFile(filepath.Join(directory, kind+"-count"))
			require.NoError(t, readErr)
			require.Equal(t, "6", string(attempts))
			sleeps, readErr := os.ReadFile(filepath.Join(directory, "sleeps"))
			require.NoError(t, readErr)
			require.Equal(t, "xxxxx", string(sleeps))
			_, readErr = os.Stat(filepath.Join(directory, "instance"))
			require.ErrorIs(t, readErr, os.ErrNotExist)
		})
	}
}

func TestCorruptCNIDownloadIsNeverExtracted(t *testing.T) {
	script := bootstrapScript(t, safeBootstrapOptions())
	fragment := bootstrapFragment(t, script, "# Checksums pinned from the upstream v1.4.0 release assets.\n", "# Download plugin-specific CNI binaries")
	for _, failure := range []string{"corrupt", "transport"} {
		t.Run(failure, func(t *testing.T) {
			directory := t.TempDir()
			stubs := `
CNI_PLUGINS_VERSION=v1.4.0
curl() {
    local output="${@: -1}"
    printf corrupt > "$output"
    printf '%s' "$output" > "$BOOTSTRAP_TEST_ROOT/archive"
    [[ "$BOOTSTRAP_TEST_MODE" != transport ]]
}
tar() { touch "$BOOTSTRAP_TEST_ROOT/extracted"; }
report_status() { printf '%s/%s' "$1" "$2" > "$BOOTSTRAP_TEST_ROOT/status"; }
`
			_, err := runBootstrapFragment(t, stubs+fragment, "BOOTSTRAP_TEST_ROOT="+directory, "BOOTSTRAP_TEST_MODE="+failure, "TMPDIR="+directory)
			require.Error(t, err)
			phase, err := os.ReadFile(filepath.Join(directory, "status"))
			require.NoError(t, err)
			require.Equal(t, "failed/cni-integrity-verification", string(phase))
			_, err = os.Stat(filepath.Join(directory, "extracted"))
			require.ErrorIs(t, err, os.ErrNotExist)
			archive, err := os.ReadFile(filepath.Join(directory, "archive"))
			require.NoError(t, err)
			_, err = os.Stat(string(archive))
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestBootstrapFailureTrapPublishesValidStatusJSON(t *testing.T) {
	script := bootstrapScript(t, safeBootstrapOptions())
	fragment := "report_status() {" + bootstrapFragment(t, script, "report_status() {", "# Instance metadata\n")
	directory := t.TempDir()
	fragment = strings.ReplaceAll(fragment, "/var/log/", directory+"/")
	strict := ""
	for _, line := range strings.Split(script, "\n") {
		if strings.HasPrefix(line, "set -") {
			strict = line
			break
		}
	}
	require.NotEmpty(t, strict)
	stubs := "journalctl() { :; }\nip() { :; }\nnslookup() { :; }\n"
	_, err := runBootstrapFragment(t, strict+"\n"+stubs+fragment+"\nbootstrap_test_failure() { false; }\nbootstrap_test_failure\n", "INSTANCE_ID=02u7_11111111-1111-1111-1111-111111111111", "NODE_NAME=claim-001", "REGION=us-south", "ZONE=us-south-1", "BOOTSTRAP_PHASE=test-failure", "BOOTSTRAP_STATUS_CONFIGMAP=")
	require.Error(t, err)
	status, err := os.ReadFile(filepath.Join(directory, "karpenter-bootstrap-status.json"))
	require.NoError(t, err)
	decoded := map[string]string{}
	require.NoError(t, json.Unmarshal(status, &decoded))
	require.Equal(t, "failed", decoded["status"])
	require.Equal(t, "test-failure", decoded["phase"])
	require.Equal(t, "claim-001", decoded["nodeClaimName"])
}

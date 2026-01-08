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
	"context"
	"fmt"
	"os"
	"strings"
	"text/template"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/types"
)

// cloudInitTemplate defines the cloud-init script template for IBM Cloud VPC using direct kubelet configuration
const cloudInitTemplate = `#!/bin/bash
set -euo pipefail

# Enhanced logging
LOG_FILE="/var/log/karpenter-bootstrap.log"
exec > >(tee -a $LOG_FILE) 2>&1

echo "$(date): ===== Karpenter IBM Cloud Bootstrap (Direct Kubelet) ====="

# Configuration
CLUSTER_ENDPOINT="{{ .ClusterEndpoint }}"
BOOTSTRAP_TOKEN="{{ .BootstrapToken }}"
CLUSTER_DNS="{{ .DNSClusterIP }}"
REGION="{{ .Region }}"
ZONE="{{ .Zone }}"
NODE_NAME="{{ .NodeName }}"

# Status reporting function
report_status() {
    local status="$1"
    local phase="$2"
    local timestamp=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

    echo "$(date): 📊 Reporting status: $status, phase: $phase"

    # Use instance ID passed from template
    if [[ -n "$INSTANCE_ID" && "$INSTANCE_ID" != "unknown" ]]; then
        echo "$(date): Instance ID: $INSTANCE_ID"
        echo "$(date): NodeClaim: $NODE_NAME"
        echo "$(date): Status: $status, Phase: $phase, Time: $timestamp" >> /var/log/karpenter-status.log

        # Create structured status for operator polling
        cat > /var/log/karpenter-bootstrap-status.json << EOF
{
    "instanceId": "$INSTANCE_ID",
    "nodeClaimName": "$NODE_NAME",
    "status": "$status",
    "phase": "$phase",
    "timestamp": "$timestamp",
    "region": "$REGION",
    "zone": "$ZONE"
}
EOF
    else
        echo "$(date): ⚠️ Instance ID not available for status reporting"
    fi

    # Always log status locally for debugging
    echo "$timestamp|$INSTANCE_ID|$NODE_NAME|$status|$phase" >> /var/log/karpenter-bootstrap-status.log

    # Enhanced error capture with detailed diagnostics
    if [[ "$status" == "failed" ]]; then
        echo "$(date): 🔍 BOOTSTRAP FAILURE DIAGNOSTICS" >> /var/log/karpenter-bootstrap-failure.log
        echo "$(date): Phase: $phase" >> /var/log/karpenter-bootstrap-failure.log
        echo "$(date): Instance ID: $INSTANCE_ID" >> /var/log/karpenter-bootstrap-failure.log
        echo "$(date): NodeClaim: $NODE_NAME" >> /var/log/karpenter-bootstrap-failure.log
        echo "$(date): Error details below:" >> /var/log/karpenter-bootstrap-failure.log
        echo "----------------------------------------" >> /var/log/karpenter-bootstrap-failure.log

        # Capture recent system logs for debugging
        echo "Recent system logs:" >> /var/log/karpenter-bootstrap-failure.log
        journalctl --no-pager -n 20 >> /var/log/karpenter-bootstrap-failure.log 2>/dev/null || echo "Could not capture journalctl logs" >> /var/log/karpenter-bootstrap-failure.log

        echo "----------------------------------------" >> /var/log/karpenter-bootstrap-failure.log
        echo "Network interface status:" >> /var/log/karpenter-bootstrap-failure.log
        ip addr show >> /var/log/karpenter-bootstrap-failure.log 2>/dev/null || echo "Could not capture network interfaces" >> /var/log/karpenter-bootstrap-failure.log

        echo "----------------------------------------" >> /var/log/karpenter-bootstrap-failure.log
        echo "DNS resolution test:" >> /var/log/karpenter-bootstrap-failure.log
        nslookup kubernetes.default.svc.cluster.local >> /var/log/karpenter-bootstrap-failure.log 2>&1 || echo "DNS resolution failed" >> /var/log/karpenter-bootstrap-failure.log
    fi
}

# Instance metadata
PRIVATE_IP=$(hostname -I | awk '{print $1}')

# Get instance ID from IBM Cloud metadata service
# The instance ID must include the zone prefix (e.g., 02u7_uuid) for proper provider ID
echo "$(date): Attempting to retrieve instance ID from metadata service..."

# Ensure metadata service is routable
echo "$(date): Checking metadata service routing..."
if ! ip route get 169.254.169.254 >/dev/null 2>&1; then
    echo "$(date): No route to metadata service, adding route via default gateway..."
    # Get default gateway
    DEFAULT_GW=$(ip route show default | awk '/default/ { print $3 }' | head -1)
    if [[ -n "$DEFAULT_GW" ]]; then
        echo "$(date): Adding route to metadata service via gateway: $DEFAULT_GW"
        ip route add 169.254.169.254 via "$DEFAULT_GW" || echo "$(date): ⚠️ Failed to add metadata route, continuing anyway..."
    else
        echo "$(date): ⚠️ Could not determine default gateway for metadata route"
    fi
else
    echo "$(date): ✅ Metadata service is already routable"
fi

# Test basic connectivity to metadata service
echo "$(date): Testing metadata service connectivity..."
if ! curl -s --connect-timeout 5 -m 10 http://169.254.169.254/ >/dev/null 2>&1; then
    echo "$(date): ⚠️ Metadata service not responding, but continuing bootstrap..."
else
    echo "$(date): ✅ Metadata service is accessible"
fi

# Get instance identity token for metadata service authentication
echo "$(date): Getting instance identity token..."
# Use IP address instead of DNS hostname to avoid DNS resolution issues
INSTANCE_IDENTITY_TOKEN=$(curl -s -f --max-time 10 -X PUT "http://169.254.169.254/instance_identity/v1/token?version=2022-03-29" -H "Metadata-Flavor: ibm" | grep -o "\"access_token\":\"[^\"]*" | cut -d"\"" -f4)

if [[ -z "$INSTANCE_IDENTITY_TOKEN" ]]; then
    echo "$(date): ❌ ERROR: Could not get instance identity token" | tee -a "$LOG_FILE"
    echo "$(date): Testing metadata service connectivity..." | tee -a "$LOG_FILE"
    curl -v -m 5 "http://169.254.169.254" 2>&1 | tee -a "$LOG_FILE" || true
    report_status "failed" "instance-identity-token-failed"
    exit 1
fi

echo "$(date): Successfully obtained instance identity token"

# Get instance ID using IBM Cloud metadata service
echo "$(date): Retrieving instance ID from IBM Cloud metadata service..."
# Use IP address instead of DNS hostname to avoid DNS resolution issues
INSTANCE_ID=$(curl -s -f --max-time 10 -H "Authorization: Bearer $INSTANCE_IDENTITY_TOKEN" "http://169.254.169.254/metadata/v1/instance?version=2022-03-29" | grep -o "\"id\":\"[0-9a-z]\{4\}_[^\"]*" | head -1 | cut -d"\"" -f4)

# Validate instance ID
if [[ -z "$INSTANCE_ID" || "$INSTANCE_ID" == "unknown" ]]; then
    echo "$(date): ❌ ERROR: Could not retrieve instance ID from metadata service" | tee -a "$LOG_FILE"
    echo "$(date): This is required for proper provider ID configuration" | tee -a "$LOG_FILE"
    echo "$(date): Token available: $([[ -n \"$INSTANCE_IDENTITY_TOKEN\" ]] && echo \"yes\" || echo \"no\")" | tee -a "$LOG_FILE"
    echo "$(date): Testing metadata endpoint with token..." | tee -a "$LOG_FILE"
    curl -v -H "Authorization: Bearer $INSTANCE_IDENTITY_TOKEN" "http://169.254.169.254/metadata/v1/instance?version=2022-03-29" 2>&1 | tee -a "$LOG_FILE" || true
    report_status "failed" "instance-id-metadata-failed"
    exit 1
fi

echo "$(date): Successfully retrieved instance ID: $INSTANCE_ID"

# Use NodeClaim name as hostname for proper Karpenter registration
HOSTNAME="$NODE_NAME"

# Report bootstrap start
report_status "starting" "hostname-setup"

# Set hostname
hostnamectl set-hostname "$HOSTNAME"
echo "127.0.0.1 $HOSTNAME" >> /etc/hosts
echo "$(date): ✅ Hostname set to: $HOSTNAME (NodeClaim name)"

# System configuration
echo 'net.ipv4.ip_forward = 1' >> /etc/sysctl.conf && sysctl -p
swapoff -a && sed -i '/swap/d' /etc/fstab

# Filesystem preparation - fix disk capacity detection
echo "$(date): Preparing filesystem..."
# Resize the root filesystem to ensure full disk is available
ROOT_DEV=$(df / | awk 'NR==2 {print $1}')
if [[ "$ROOT_DEV" =~ ^/dev/ ]]; then
    resize2fs "$ROOT_DEV" || true
    echo "$(date): ✅ Root filesystem resized"
else
    echo "$(date): ⚠️ Could not identify root device: $ROOT_DEV"
fi

# Ensure proper directory structure for container storage
mkdir -p /var/lib/containerd /var/lib/kubelet /var/log/pods /var/lib/cni
chown -R root:root /var/lib/containerd /var/lib/kubelet

# Create required directories for CNI
mkdir -p /opt/cni/bin /etc/cni/net.d /var/lib/calico /var/run/calico /var/log/calico/cni
chown -R root:root /var/lib/calico /var/run/calico /var/log/calico

echo "$(date): ✅ System configured"

# Report system configuration complete
report_status "configuring" "system-setup"

# Install prerequisites
echo "$(date): Installing prerequisites..."
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y curl apt-transport-https ca-certificates gnupg lsb-release dmidecode jq
echo "$(date): ✅ Prerequisites installed"

# Report prerequisites installed
report_status "configuring" "packages-installed"

# Install container runtime based on configuration
CONTAINER_RUNTIME="{{ .ContainerRuntime }}"
echo "$(date): Installing container runtime: $CONTAINER_RUNTIME"

install_containerd() {
    echo "$(date): Installing containerd..."
    curl -fsSL https://download.docker.com/linux/ubuntu/gpg | apt-key add -
    add-apt-repository "deb [arch=amd64] https://download.docker.com/linux/ubuntu $(lsb_release -cs) stable"
    apt-get update
    apt-get install -y containerd.io
    echo "$(date): ✅ Containerd installed"

    # Configure containerd
    echo "$(date): Configuring containerd..."
    mkdir -p /etc/containerd

    # Create containerd config with proper systemd cgroup configuration
    cat > /etc/containerd/config.toml << 'EOF'
disabled_plugins = []
imports = []
oom_score = 0
plugin_dir = ""
required_plugins = []
root = "/var/lib/containerd"
state = "/run/containerd"
temp = ""
version = 2

[grpc]
  address = "/run/containerd/containerd.sock"
  tcp_address = ""
  tcp_tls_cert = ""
  tcp_tls_key = ""
  uid = 0
  gid = 0
  max_recv_message_size = 16777216
  max_send_message_size = 16777216

[debug]
  address = ""
  format = ""
  level = ""
  uid = 0
  gid = 0

[metrics]
  address = ""
  grpc_histogram = false

[cgroup]
  path = ""

[timeouts]
  "io.containerd.timeout.shim.cleanup" = "5s"
  "io.containerd.timeout.shim.load" = "5s"
  "io.containerd.timeout.shim.shutdown" = "3s"
  "io.containerd.timeout.task.state" = "2s"

[plugins]
  [plugins."io.containerd.gc.v1.scheduler"]
    pause_threshold = 0.02
    deletion_threshold = 0
    mutation_threshold = 100
    schedule_delay = "0s"
    startup_delay = "100ms"
  [plugins."io.containerd.grpc.v1.cri"]
    disable_tcp_service = true
    stream_server_address = "127.0.0.1"
    stream_server_port = "0"
    stream_idle_timeout = "4h0m0s"
    enable_selinux = false
    selinux_category_range = 1024
    sandbox_image = "registry.k8s.io/pause:3.6"
    stats_collect_period = 10
    enable_tls_streaming = false
    max_container_log_line_size = 16384
    disable_cgroup = false
    disable_apparmor = false
    restrict_oom_score_adj = false
    max_concurrent_downloads = 3
    disable_proc_mount = false
    unset_seccomp_profile = ""
    tolerate_missing_hugetlb_controller = true
    disable_hugetlb_controller = true
    ignore_image_defined_volumes = false
    [plugins."io.containerd.grpc.v1.cri".containerd]
      snapshotter = "overlayfs"
      default_runtime_name = "runc"
      no_pivot = false
      disable_snapshot_annotations = true
      discard_unpacked_layers = false
      [plugins."io.containerd.grpc.v1.cri".containerd.runtimes]
        [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runc]
          runtime_type = "io.containerd.runc.v2"
          runtime_engine = ""
          runtime_root = ""
          privileged_without_host_devices = false
          base_runtime_spec = ""
          [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runc.options]
            SystemdCgroup = true
EOF

    systemctl restart containerd
    systemctl enable containerd
    echo "$(date): ✅ Containerd configured and started"
}

install_crio() {
    echo "$(date): Installing CRI-O..."

    # Add CRI-O repository
    curl -fsSL https://download.opensuse.org/repositories/devel:/kubic:/libcontainers:/stable/xUbuntu_20.04/Release.key | apt-key add -
    curl -fsSL https://download.opensuse.org/repositories/devel:/kubic:/libcontainers:/stable:/cri-o:/1.24/xUbuntu_20.04/Release.key | apt-key add -

    echo "deb https://download.opensuse.org/repositories/devel:/kubic:/libcontainers:/stable/xUbuntu_20.04/ /" > /etc/apt/sources.list.d/devel:kubic:libcontainers:stable.list
    echo "deb https://download.opensuse.org/repositories/devel:/kubic:/libcontainers:/stable:/cri-o:/1.24/xUbuntu_20.04/ /" > /etc/apt/sources.list.d/devel:kubic:libcontainers:stable:cri-o:1.24.list

    apt-get update
    apt-get install -y cri-o cri-o-runc

    # Start CRI-O
    systemctl enable crio
    systemctl start crio
    echo "$(date): ✅ CRI-O configured and started"
}

# Install the configured container runtime
case "$CONTAINER_RUNTIME" in
    "containerd"|"")
        install_containerd
        ;;
    "cri-o")
        install_crio
        ;;
    *)
        echo "$(date): ❌ Unsupported container runtime: $CONTAINER_RUNTIME"
        echo "$(date): Supported runtimes: containerd, cri-o"
        exit 1
        ;;
esac

# Report container runtime installed
report_status "configuring" "container-runtime-ready"

# Install Kubernetes components
echo "$(date): Installing Kubernetes components..."
K8S_VERSION="{{ .KubernetesVersion }}"
K8S_MAJOR_MINOR=$(echo $K8S_VERSION | sed 's/v\([0-9]*\.[0-9]*\).*/v\1/')
echo "$(date): Installing Kubernetes $K8S_VERSION (using repo $K8S_MAJOR_MINOR)"

curl -fsSL https://pkgs.k8s.io/core:/stable:/$K8S_MAJOR_MINOR/deb/Release.key | gpg --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg
echo "deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/$K8S_MAJOR_MINOR/deb/ /" > /etc/apt/sources.list.d/kubernetes.list
apt-get update
apt-get install -y kubelet kubectl
apt-mark hold kubelet kubectl
echo "$(date): ✅ Kubernetes components installed"

# Report kubelet installed
report_status "configuring" "kubelet-installed"

# Create directories
mkdir -p /etc/kubernetes/pki /var/lib/kubelet /etc/systemd/system/kubelet.service.d

# Write primary CA certificate
cat > /etc/kubernetes/pki/ca.crt << 'EOF'
{{ .CABundle }}
EOF
echo "$(date): ✅ Primary CA certificate created"

# Create combined CA bundle for kubelet client authentication
cat > /etc/kubernetes/pki/kubelet-client-ca.crt << 'EOF'
{{ .CABundle }}
EOF

{{ if .AdditionalCAs }}{{ range .AdditionalCAs }}
# Add additional CA
cat >> /etc/kubernetes/pki/kubelet-client-ca.crt << 'EOF'
{{ . }}
EOF
{{ end }}{{ end }}

{{ if .KubeletClientCAs }}{{ range .KubeletClientCAs }}
# Add kubelet-specific CA
cat >> /etc/kubernetes/pki/kubelet-client-ca.crt << 'EOF'
{{ . }}
EOF
{{ end }}{{ end }}

echo "$(date): ✅ Combined CA certificate created for kubelet client authentication"

# Allow override of CA trust via environment variable (optional)
if [[ -n "${KARPENTER_ADDITIONAL_CA:-}" ]]; then
    echo "$(date): Adding additional CA from environment variable"
    echo "${KARPENTER_ADDITIONAL_CA}" >> /etc/kubernetes/pki/kubelet-client-ca.crt
    echo "$(date): ✅ Additional CA added to kubelet client CA bundle"
fi

# Allow override from file (for testing, optional)
if [[ -f "/etc/kubernetes/additional-ca.crt" ]]; then
    echo "$(date): Adding additional CA from file"
    cat /etc/kubernetes/additional-ca.crt >> /etc/kubernetes/pki/kubelet-client-ca.crt
fi

# Create bootstrap kubeconfig
cat > /var/lib/kubelet/bootstrap-kubeconfig << EOF
apiVersion: v1
kind: Config
clusters:
- cluster:
    certificate-authority: /etc/kubernetes/pki/ca.crt
    server: ${CLUSTER_ENDPOINT}
  name: default
contexts:
- context:
    cluster: default
    user: kubelet-bootstrap
  name: default
current-context: default
users:
- name: kubelet-bootstrap
  user:
    token: ${BOOTSTRAP_TOKEN}
EOF
echo "$(date): ✅ Bootstrap kubeconfig created"

# Create kubelet configuration
cat > /var/lib/kubelet/config.yaml << EOF
apiVersion: kubelet.config.k8s.io/v1beta1
kind: KubeletConfiguration
authentication:
  anonymous:
    enabled: false
  webhook:
    enabled: true
  x509:
    clientCAFile: /etc/kubernetes/pki/kubelet-client-ca.crt
authorization:
  mode: Webhook
clusterDomain: cluster.local
clusterDNS:
{{- if .KubeletConfig }}
  {{- if .KubeletConfig.ClusterDNS }}
    {{- range .KubeletConfig.ClusterDNS }}
  - {{ . }}
    {{- end }}
  {{- else }}
  - ${CLUSTER_DNS}
  {{- end }}
{{- else }}
  - ${CLUSTER_DNS}
{{- end }}
rotateCertificates: true
serverTLSBootstrap: true
registerNode: true
cgroupDriver: systemd
{{- if .KubeletConfig }}
  {{- if .KubeletConfig.MaxPods }}
maxPods: {{ .KubeletConfig.MaxPods }}
  {{- end }}
  {{- if .KubeletConfig.PodsPerCore }}
podsPerCore: {{ .KubeletConfig.PodsPerCore }}
  {{- end }}
  {{- if .KubeletConfig.KubeReserved }}
kubeReserved:
    {{- range $k, $v := .KubeletConfig.KubeReserved }}
  "{{ $k }}": "{{ $v }}"
    {{- end }}
  {{- end }}
  {{- if .KubeletConfig.SystemReserved }}
systemReserved:
    {{- range $k, $v := .KubeletConfig.SystemReserved }}
  "{{ $k }}": "{{ $v }}"
    {{- end }}
  {{- end }}
  {{- if .KubeletConfig.EvictionHard }}
evictionHard:
    {{- range $k, $v := .KubeletConfig.EvictionHard }}
  "{{ $k }}": "{{ $v }}"
    {{- end }}
  {{- end }}
  {{- if .KubeletConfig.EvictionSoft }}
evictionSoft:
    {{- range $k, $v := .KubeletConfig.EvictionSoft }}
  "{{ $k }}": "{{ $v }}"
    {{- end }}
  {{- end }}
  {{- if .KubeletConfig.EvictionSoftGracePeriod }}
evictionSoftGracePeriod:
    {{- range $k, $v := .KubeletConfig.EvictionSoftGracePeriod }}
  "{{ $k }}": "{{ $v.Duration }}"
    {{- end }}
  {{- end }}
  {{- if .KubeletConfig.EvictionMaxPodGracePeriod }}
evictionMaxPodGracePeriod: {{ .KubeletConfig.EvictionMaxPodGracePeriod }}
  {{- end }}
  {{- if .KubeletConfig.ImageGCHighThresholdPercent }}
imageGCHighThresholdPercent: {{ .KubeletConfig.ImageGCHighThresholdPercent }}
  {{- end }}
  {{- if .KubeletConfig.ImageGCLowThresholdPercent }}
imageGCLowThresholdPercent: {{ .KubeletConfig.ImageGCLowThresholdPercent }}
  {{- end }}
  {{- if .KubeletConfig.CPUCFSQuota }}
cpuCFSQuota: {{ .KubeletConfig.CPUCFSQuota }}
  {{- end }}
{{- end }}
registerWithTaints:
{{ range .Taints }}
- key: {{ .Key }}
  value: "{{ .Value }}"
  effect: {{ .Effect }}
{{ end }}
{{ if eq .CNIPlugin "cilium" }}
- key: node.cilium.io/agent-not-ready
  value: "true"
  effect: PreferNoSchedule
{{ end }}
nodeLabels:
{{ range $key, $value := .Labels }}
  {{ $key }}: "{{ $value }}"
{{ end }}
EOF
echo "$(date): ✅ Kubelet configuration created"

# Provider ID configuration
PROVIDER_ID="ibm:///${REGION}/${INSTANCE_ID}"
echo "$(date): Instance ID: $INSTANCE_ID, Provider ID: $PROVIDER_ID"

# Create bootstrap kubeconfig with correct API server endpoint
mkdir -p /etc/kubernetes
cat > /etc/kubernetes/bootstrap-kubeconfig << EOF
apiVersion: v1
kind: Config
clusters:
- cluster:
    certificate-authority: /etc/kubernetes/pki/ca.crt
    server: {{ .ClusterEndpoint }}
  name: bootstrap-cluster
contexts:
- context:
    cluster: bootstrap-cluster
    user: bootstrap-user
  name: bootstrap
current-context: bootstrap
users:
- name: bootstrap-user
  user:
    token: {{ .BootstrapToken }}
EOF

# Configure kubelet service with bootstrap kubeconfig
cat > /etc/systemd/system/kubelet.service.d/10-karpenter.conf << EOF
[Service]
Environment="KUBELET_EXTRA_ARGS=--hostname-override=${HOSTNAME} --node-ip=${PRIVATE_IP} --provider-id=${PROVIDER_ID} --bootstrap-kubeconfig=/etc/kubernetes/bootstrap-kubeconfig --sync-frequency=30s"
EOF

# Create kubelet service override
cat > /etc/systemd/system/kubelet.service << 'EOF'
[Unit]
Description=kubelet: The Kubernetes Node Agent
Documentation=https://kubernetes.io/docs/
Wants=network-online.target
After=network-online.target

[Service]
ExecStart=/usr/bin/kubelet \
  --bootstrap-kubeconfig=/var/lib/kubelet/bootstrap-kubeconfig \
  --kubeconfig=/var/lib/kubelet/kubeconfig \
  --config=/var/lib/kubelet/config.yaml \
  $KUBELET_EXTRA_ARGS
Restart=always
StartLimitInterval=0
RestartSec=10

[Install]
WantedBy=multi-user.target
EOF

echo "$(date): ✅ Kubelet service configured"

# Verify filesystem before starting kubelet
echo "$(date): Verifying filesystem readiness..."
df -h / && echo "$(date): ✅ Filesystem verified" || echo "$(date): ⚠️ Filesystem verification failed"

# Wait for container runtime to be ready
echo "$(date): Waiting for container runtime to be ready..."
for i in {1..30}; do
  if systemctl is-active containerd >/dev/null 2>&1; then
    echo "$(date): ✅ Container runtime is ready"
    break
  fi
  sleep 2
done

# Wait for API server to be available before starting kubelet
echo "$(date): Testing API server connectivity before starting kubelet..."
for i in {1..60}; do
  if curl -k --connect-timeout 5 -m 5 {{ .ClusterEndpoint }}/healthz >/dev/null 2>&1; then
    echo "$(date): ✅ API server is ready"
    break
  fi
  echo "$(date): Waiting for API server... (attempt $i/60)"
  sleep 5
done

# Install CNI binaries and configuration
CNI_PLUGIN="{{ .CNIPlugin }}"
CNI_VERSION="{{ .CNIVersion }}"
echo "$(date): Installing $CNI_PLUGIN CNI binaries and configuration..."
mkdir -p /etc/cni/net.d

# Download and install CNI binaries
echo "$(date): Downloading CNI binaries..."
CNI_PLUGINS_VERSION="v1.4.0"

# Download standard CNI plugins
curl -L "https://github.com/containernetworking/plugins/releases/download/${CNI_PLUGINS_VERSION}/cni-plugins-linux-{{ .Architecture }}-${CNI_PLUGINS_VERSION}.tgz" | tar -C /opt/cni/bin -xz

# Download plugin-specific CNI binaries using detected version
case "$CNI_PLUGIN" in
  "calico")
    echo "$(date): Downloading Calico CNI binaries version $CNI_VERSION..."
    curl -L -o /opt/cni/bin/calico "https://github.com/projectcalico/cni-plugin/releases/download/${CNI_VERSION}/calico-{{ .Architecture }}"
    curl -L -o /opt/cni/bin/calico-ipam "https://github.com/projectcalico/cni-plugin/releases/download/${CNI_VERSION}/calico-ipam-{{ .Architecture }}"
    chmod +x /opt/cni/bin/calico /opt/cni/bin/calico-ipam
    mkdir -p /var/log/calico/cni
    ;;
  "cilium")
    echo "$(date): Setting up Cilium CNI configuration..."
    # Cilium CNI plugin is installed via DaemonSet, not as standalone binary
    # Create a minimal CNI configuration that Cilium will replace when it starts
    mkdir -p /var/log/cilium
    echo "$(date): ✅ Cilium CNI setup prepared (plugin will be installed by DaemonSet)"
    ;;
  "flannel")
    echo "$(date): Downloading Flannel CNI binaries version $CNI_VERSION..."
    curl -L -o /opt/cni/bin/flannel "https://github.com/flannel-io/cni-plugin/releases/download/${CNI_VERSION}/flannel-amd64"
    chmod +x /opt/cni/bin/flannel
    mkdir -p /var/log/flannel
    ;;
esac

# Set proper permissions
chmod +x /opt/cni/bin/*
echo "$(date): ✅ CNI binaries installed"

# Install CNI configuration based on detected plugin
case "$CNI_PLUGIN" in
  "calico")
    echo "$(date): Installing Calico CNI configuration..."

    # Create nodename file - this is critical for Calico CNI to work
    # This prevents the race condition where CNI is invoked before the DaemonSet creates this file
    echo "$HOSTNAME" > /var/lib/calico/nodename
    echo "$(date): ✅ Created Calico nodename file: $HOSTNAME"

    cat > /etc/cni/net.d/10-calico.conflist << 'EOF'
{
  "name": "k8s-pod-network",
  "cniVersion": "0.3.1",
  "plugins": [
    {
      "type": "calico",
      "log_level": "info",
      "log_file_path": "/var/log/calico/cni/cni.log",
      "datastore_type": "kubernetes",
      "nodename": "__KUBERNETES_NODE_NAME__",
      "mtu": 1440,
      "ipam": {
          "type": "calico-ipam"
      },
      "policy": {
          "type": "k8s"
      },
      "kubernetes": {
          "kubeconfig": "__KUBECONFIG_FILEPATH__"
      }
    },
    {
      "type": "portmap",
      "snat": true,
      "capabilities": {"portMappings": true}
    },
    {
      "type": "bandwidth",
      "capabilities": {"bandwidth": true}
    }
  ]
}
EOF
    # Replace placeholders in CNI configuration
    sed -i "s/__KUBERNETES_NODE_NAME__/$(hostname)/g" /etc/cni/net.d/10-calico.conflist
    sed -i "s/__KUBECONFIG_FILEPATH__/\/var\/lib\/kubelet\/bootstrap-kubeconfig/g" /etc/cni/net.d/10-calico.conflist
    ;;
  "cilium")
    echo "$(date): Creating temporary CNI configuration for Cilium bootstrap..."
    # Create a temporary bridge CNI configuration that Cilium will replace
    # This allows kubelet to start and provide service info to Cilium DaemonSet
    # Using 192.168.200.0/24 to avoid conflicts with existing 10.x networks
    # IMPORTANT: isDefaultGateway is set to false to avoid route conflicts
    cat > /etc/cni/net.d/00-cilium-bootstrap.conflist << 'EOF'
{
  "cniVersion": "0.3.1",
  "name": "cilium-bootstrap",
  "plugins": [
    {
      "type": "bridge",
      "bridge": "cni-bootstrap",
      "isDefaultGateway": false,
      "ipMasq": true,
      "hairpinMode": true,
      "ipam": {
        "type": "host-local",
        "subnet": "192.168.200.0/24"
      }
    },
    {
      "type": "portmap",
      "capabilities": {
        "portMappings": true
      }
    }
  ]
}
EOF
    echo "$(date): ✅ Temporary CNI configuration created - Cilium DaemonSet will replace it"
    ;;
  "flannel")
    echo "$(date): Installing Flannel CNI configuration..."
    cat > /etc/cni/net.d/10-flannel.conflist << 'EOF'
{
  "name": "cbr0",
  "cniVersion": "0.3.1",
  "plugins": [
    {
      "type": "flannel",
      "delegate": {
        "hairpinMode": true,
        "isDefaultGateway": true
      }
    },
    {
      "type": "portmap",
      "capabilities": {
        "portMappings": true
      }
    }
  ]
}
EOF
    ;;
  *)
    echo "$(date): ⚠️  Unknown CNI plugin: $CNI_PLUGIN, skipping CNI configuration"
    echo "$(date): Node will rely on CNI DaemonSet deployment"
    ;;
esac

echo "$(date): ✅ CNI configuration installed for $CNI_PLUGIN"

# Start kubelet
systemctl daemon-reload
systemctl enable kubelet

# Report kubelet starting
report_status "starting" "kubelet-startup"

systemctl start kubelet

echo "$(date): Waiting for node to be ready..."
for i in {1..60}; do
  if systemctl is-active kubelet >/dev/null 2>&1; then
    echo "$(date): ✅ Kubelet is running"
    # Report kubelet successfully started
    report_status "running" "kubelet-active"
    break
  fi
  sleep 5
done

echo "$(date): Checking kubelet status..."
systemctl status kubelet --no-pager || true
journalctl -u kubelet --no-pager -n 20 || true

# Wait for CNI to be fully operational
echo "$(date): Waiting for $CNI_PLUGIN CNI to initialize..."
# Set timeout based on CNI plugin type
if [[ "$CNI_PLUGIN" == "cilium" ]]; then
    CNI_WAIT_TIMEOUT=30  # 30 seconds for Cilium (basic check only)
    echo "$(date): Cilium CNI uses DaemonSet installation - minimal wait for infrastructure"
else
    CNI_WAIT_TIMEOUT=300  # 5 minutes for other CNI plugins
fi
CNI_WAIT_INTERVAL=5
elapsed=0

check_cni_ready() {
    case "$CNI_PLUGIN" in
      "calico")
        # Check 1: CNI binaries exist and are executable
        [ -x /opt/cni/bin/calico ] || return 1
        [ -x /opt/cni/bin/calico-ipam ] || return 1

        # Check 2: CNI configuration exists and is valid JSON
        [ -f /etc/cni/net.d/10-calico.conflist ] || return 1

        # Check 3: Calico nodename file exists (created during bootstrap)
        [ -f /var/lib/calico/nodename ] || return 1

        # Check 4: CNI can be invoked successfully
        if [ -x /opt/cni/bin/calico ] && [ -f /etc/cni/net.d/10-calico.conflist ]; then
            # Try a simple CNI version check
            CNI_PATH=/opt/cni/bin /opt/cni/bin/calico version >/dev/null 2>&1 || return 1
        fi
        ;;
      "cilium")
        # For Cilium, CNI plugin is installed by DaemonSet after node joins cluster
        # Just verify basic CNI infrastructure is ready

        # Check 1: Standard CNI plugins are available
        [ -x /opt/cni/bin/bridge ] || return 1
        [ -x /opt/cni/bin/loopback ] || return 1

        # Check 2: CNI directories exist
        [ -d /opt/cni/bin ] || return 1
        [ -d /etc/cni/net.d ] || return 1
        [ -d /var/log/cilium ] || return 1

        # Don't wait for Cilium CNI plugin - it's installed post-join by DaemonSet
        echo "$(date): Cilium CNI readiness: Basic infrastructure ready, DaemonSet will install plugin"
        ;;
      "flannel")
        # Check 1: CNI binaries exist and are executable
        [ -x /opt/cni/bin/flannel ] || return 1

        # Check 2: CNI configuration exists
        [ -f /etc/cni/net.d/10-flannel.conflist ] || return 1

        # Check 3: Flannel is running (if crictl available)
        if command -v crictl >/dev/null 2>&1; then
            crictl ps 2>/dev/null | grep -q flannel || return 1
        fi
        ;;
      *)
        # For unknown CNI plugins, just check basic CNI setup
        # Look for any CNI configuration file
        ls /etc/cni/net.d/*.conflist >/dev/null 2>&1 || return 1
        # Check for basic CNI binaries
        [ -x /opt/cni/bin/bridge ] || return 1
        [ -x /opt/cni/bin/loopback ] || return 1
        ;;
    esac

    return 0
}

while [ $elapsed -lt $CNI_WAIT_TIMEOUT ]; do
  if check_cni_ready; then
    echo "$(date): ✅ $CNI_PLUGIN CNI is fully operational"
    break
  fi

  echo "$(date): Waiting for $CNI_PLUGIN CNI readiness... (elapsed: ${elapsed}s)"

  # Detailed status for debugging every 30 seconds
  if [ $((elapsed % 30)) -eq 0 ] && [ $elapsed -gt 0 ]; then
    echo "$(date): CNI Status Check:"
    case "$CNI_PLUGIN" in
      "calico")
        echo "  - CNI binaries: $([ -x /opt/cni/bin/calico ] && echo "✓" || echo "✗")"
        echo "  - CNI config: $([ -f /etc/cni/net.d/10-calico.conflist ] && echo "✓" || echo "✗")"
        echo "  - Calico nodename: $([ -f /var/lib/calico/nodename ] && echo "✓" || echo "✗")"
        if command -v crictl >/dev/null 2>&1; then
          echo "  - Calico container: $(crictl ps 2>/dev/null | grep -q calico-node && echo "✓" || echo "✗")"
        fi
        ;;
      "cilium")
        echo "  - CNI binaries: $([ -x /opt/cni/bin/cilium-cni ] && echo "✓" || echo "✗")"
        echo "  - CNI config: $([ -f /etc/cni/net.d/05-cilium.conflist ] && echo "✓" || echo "✗")"
        if command -v crictl >/dev/null 2>&1; then
          echo "  - Cilium container: $(crictl ps 2>/dev/null | grep -q cilium && echo "✓" || echo "✗")"
        fi
        ;;
      "flannel")
        echo "  - CNI binaries: $([ -x /opt/cni/bin/flannel ] && echo "✓" || echo "✗")"
        echo "  - CNI config: $([ -f /etc/cni/net.d/10-flannel.conflist ] && echo "✓" || echo "✗")"
        if command -v crictl >/dev/null 2>&1; then
          echo "  - Flannel container: $(crictl ps 2>/dev/null | grep -q flannel && echo "✓" || echo "✗")"
        fi
        ;;
      *)
        echo "  - CNI configs: $(ls /etc/cni/net.d/*.conflist 2>/dev/null | wc -l) found"
        echo "  - Basic CNI binaries: $([ -x /opt/cni/bin/bridge ] && echo "✓" || echo "✗")"
        ;;
    esac
  fi

  sleep $CNI_WAIT_INTERVAL
  elapsed=$((elapsed + CNI_WAIT_INTERVAL))
done

# Check if we timed out
if [ $elapsed -ge $CNI_WAIT_TIMEOUT ]; then
  echo "$(date): ⚠️ $CNI_PLUGIN CNI failed to become ready after ${CNI_WAIT_TIMEOUT}s"
  echo "$(date): Comprehensive diagnostic information:"

  # CNI binaries
  echo "  CNI binaries in /opt/cni/bin/:"
  ls -la /opt/cni/bin/ 2>/dev/null || echo "    Directory does not exist"

  # CNI configs
  echo "  CNI configs in /etc/cni/net.d/:"
  ls -la /etc/cni/net.d/ 2>/dev/null || echo "    Directory does not exist"

  # Plugin-specific diagnostics
  case "$CNI_PLUGIN" in
    "calico")
      if [ -f /etc/cni/net.d/10-calico.conflist ]; then
        echo "  Calico CNI config content:"
        head -20 /etc/cni/net.d/10-calico.conflist 2>/dev/null | sed 's/^/    /'
      fi
      echo "  Calico directory /var/lib/calico/:"
      ls -la /var/lib/calico/ 2>/dev/null || echo "    Directory does not exist"
      echo "  Calico runtime directory /var/run/calico/:"
      ls -la /var/run/calico/ 2>/dev/null || echo "    Directory does not exist"
      ;;
    "cilium")
      if [ -f /etc/cni/net.d/05-cilium.conflist ]; then
        echo "  Cilium CNI config content:"
        head -20 /etc/cni/net.d/05-cilium.conflist 2>/dev/null | sed 's/^/    /'
      fi
      echo "  Cilium directory /var/run/cilium/:"
      ls -la /var/run/cilium/ 2>/dev/null || echo "    Directory does not exist"
      ;;
    "flannel")
      if [ -f /etc/cni/net.d/10-flannel.conflist ]; then
        echo "  Flannel CNI config content:"
        head -20 /etc/cni/net.d/10-flannel.conflist 2>/dev/null | sed 's/^/    /'
      fi
      echo "  Flannel directory /var/run/flannel/:"
      ls -la /var/run/flannel/ 2>/dev/null || echo "    Directory does not exist"
      ;;
  esac

  # Container runtime status
  if command -v crictl >/dev/null 2>&1; then
    echo "  Running containers:"
    crictl ps 2>/dev/null | head -10 | sed 's/^/    /' || echo "    Could not list containers"
  fi

  # Kubelet logs for CNI errors
  echo "  Recent kubelet CNI logs:"
  journalctl -u kubelet --no-pager -n 10 2>/dev/null | grep -i cni | tail -5 | sed 's/^/    /' || echo "    No CNI-related logs found"

  # Try to get CNI pod status
  if command -v kubectl >/dev/null 2>&1; then
    echo "  $CNI_PLUGIN pod status:"
    kubectl --kubeconfig=/var/lib/kubelet/kubeconfig get pods -n kube-system -l k8s-app=$CNI_PLUGIN --field-selector spec.nodeName=$(hostname) -o wide 2>/dev/null | sed 's/^/    /' || echo "    Could not get pod status"
  fi

  # Continue anyway - let Kubernetes handle the CNI readiness
  echo "$(date): Continuing bootstrap despite CNI issues..."
else
  echo "$(date): ✅ $CNI_PLUGIN CNI initialization completed successfully"
fi

# Run custom user data if provided
{{ if .CustomUserData }}
echo "$(date): Running custom user data..."
{{ .CustomUserData }}
{{ end }}

# Report bootstrap completion
report_status "completed" "bootstrap-finished"

echo "$(date): ===== Bootstrap completed ====="
`

// InjectBootstrapEnvVars injects BOOTSTRAP_* environment variables into a script
// This function works with any bash script by inserting exports after the shebang
func InjectBootstrapEnvVars(ctx context.Context, script string) string {
	logger := log.FromContext(ctx)
	var bootstrapVars strings.Builder
	for _, env := range os.Environ() {
		if strings.HasPrefix(env, "BOOTSTRAP_") {
			parts := strings.SplitN(env, "=", 2)
			if len(parts) == 2 {
				varName := parts[0]
				varValue := parts[1]
				bootstrapVars.WriteString(fmt.Sprintf("export %s=%q\n", varName, varValue))
			}
		}
	}

	if bootstrapVars.Len() > 0 {
		logger.V(1).Info("Injecting bootstrap environment variables", "count", strings.Count(bootstrapVars.String(), "\n"))

		// Handle different shebang styles
		if strings.HasPrefix(script, "#!/bin/bash\nset -euo pipefail\n") {
			script = strings.Replace(script, "#!/bin/bash\nset -euo pipefail\n",
				"#!/bin/bash\nset -euo pipefail\n\n"+bootstrapVars.String(), 1)
		} else if strings.HasPrefix(script, "#!/bin/bash\n") {
			script = strings.Replace(script, "#!/bin/bash\n",
				"#!/bin/bash\n\n"+bootstrapVars.String(), 1)
		} else {
			// If no shebang found, prepend to the script
			script = bootstrapVars.String() + "\n" + script
		}
	}

	return script
}

// generateCloudInitScript generates a cloud-init script for node bootstrapping
func (p *VPCBootstrapProvider) generateCloudInitScript(ctx context.Context, options types.Options) (string, error) {
	// Create template
	tmpl, err := template.New("cloudinit").Parse(cloudInitTemplate)
	if err != nil {
		return "", fmt.Errorf("parsing cloud-init template: %w", err)
	}

	// Build template data
	data := struct {
		types.Options
	}{
		Options: options,
	}

	// Execute template
	var buf strings.Builder
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("executing cloud-init template: %w", err)
	}

	// Get the base script
	script := buf.String()

	// Add additional CA environment variable if available from secret
	logger := log.FromContext(ctx)
	if additionalCA := os.Getenv("ca_crt"); additionalCA != "" {
		logger.V(1).Info("Found ca_crt environment variable", "length", len(additionalCA))
		// Inject KARPENTER_ADDITIONAL_CA environment variable at the beginning of the script
		envVar := fmt.Sprintf("export KARPENTER_ADDITIONAL_CA=\"%s\"\n", additionalCA)
		script = strings.Replace(script, "#!/bin/bash\nset -euo pipefail\n", "#!/bin/bash\nset -euo pipefail\n\n"+envVar, 1)
		logger.V(1).Info("Injected KARPENTER_ADDITIONAL_CA into cloud-init script")
	}

	// Inject BOOTSTRAP_* environment variables
	script = InjectBootstrapEnvVars(ctx, script)

	return script, nil
}

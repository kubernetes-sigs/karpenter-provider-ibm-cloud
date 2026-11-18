# Bootstrap Methods

The Karpenter IBM Cloud Provider provides automatic node bootstrap capabilities to seamlessly join IBM Cloud VPC instances to your Kubernetes cluster. This document explains the available bootstrap methods and their configurations.

## Overview

The provider supports three bootstrap approaches:

1. **Auto Bootstrap** (Default) - Intelligent automatic method selection (Experimental)
2. **VPC Bootstrap** - Direct cloud-init integration for self-managed clusters
3. **IKS Bootstrap** - Native IBM Kubernetes Service integration (Experimental)

The provider aims to automatically detect your cluster configuration and generates appropriate bootstrap scripts with no manual userData needed.

## Auto Bootstrap (Experimental)

### When to Use
- **Simplified configuration** without manual bootstrap decisions

### How It Works
An explicit class `bootstrapMode` overrides the controller default. An omitted/`auto` class with `iksClusterID` selects IKS; otherwise `BOOTSTRAP_MODE` supplies `cloud-init`, `iks-api`, or `auto`. An invalid global mode prevents startup. With `auto`, `IKS_CLUSTER_ID` selects IKS; otherwise the provider uses VPC cloud-init. IKS failures do not switch to VPC. Explicit class `cloud-init` takes precedence over an IKS cluster ID.

### Configuration
```yaml
apiVersion: karpenter-ibm.sh/v1alpha1
kind: IBMNodeClass
metadata:
  name: auto-bootstrap-nodeclass
spec:
  resourceGroup: replace-with-resource-group-id
  apiServerEndpoint: "https://<INTERNAL-API-SERVER-IP>:6443"
  subnet: 0717-replace-with-subnet-id
  iksDynamicPools:
    enabled: true
  region: us-south
  zone: us-south-1
  vpc: vpc-12345678
  image: r006-ubuntu-20-04

  # Auto bootstrap (default - no bootstrapMode needed)
  # Optionally provide IKS cluster ID for IKS preference
  iksClusterID: "cluster-12345678"  # Optional

  # No userData required - fully automatic!
```

### Automatic Features
- **Cluster Discovery**: Automatically detects cluster API endpoint and CA certificate
- **Token Management**: Issues a claim-bound token with a one-hour lifetime and revokes it after registration
- **Network Detection**: Discovers cluster CIDR and DNS configuration
- **System Configuration**: Enables IP forwarding, disables swap, configures hostname
- **Runtime Selection**: Auto-detects and configures container runtime (containerd/crio)

### Bootstrap Token RBAC Design

Bootstrap requests use the group below. Each NodeClaim receives its own token and a named ConfigMap status Role. Client and serving CSR approval verifies the claim, launch, cloud ownership, and requested node identity; serving approval is not granted to `system:nodes`. Generic `nodeclient` autoapproval for provider tokens must be disabled before provisioning.

#### Bootstrap request group
```yaml
# All bootstrap tokens use the same generic group
group: "system:bootstrappers:karpenter:ibm-cloud"

# Single ClusterRoleBinding covers all NodePools
subjects:
- kind: Group
  name: system:bootstrappers:karpenter:ibm-cloud
  apiGroup: rbac.authorization.k8s.io
```

## VPC Bootstrap (Cloud-Init)

### When to Use
- **Self-managed Kubernetes clusters** running on IBM Cloud VPC
- **Custom cluster configurations** requiring specific setup

### Configuration
```yaml
apiVersion: karpenter-ibm.sh/v1alpha1
kind: IBMNodeClass
metadata:
  name: vpc-bootstrap-nodeclass
spec:
  resourceGroup: replace-with-resource-group-id
  apiServerEndpoint: "https://<INTERNAL-API-SERVER-IP>:6443"
  region: us-south
  zone: us-south-1
  vpc: vpc-12345678
  image: r006-ubuntu-20-04

  # Explicit VPC bootstrap mode (optional - auto-detected)
  bootstrapMode: cloud-init

  # Optional custom pre-bootstrap setup
  userDataAppend: |
    #!/bin/bash
    echo "Custom bootstrap extension"
```

### Automatic Features

#### **Intelligent Cluster Discovery**
- **API Endpoint Detection**: Automatically finds internal cluster API server endpoint
- **Certificate Authority**: Extracts cluster CA certificate from existing nodes
- **DNS Configuration**: Discovers cluster DNS service IP and domain
- **Network Setup**: Detects cluster pod and service CIDR ranges

#### **Container Runtime Management**
- **Containerd** (Default): Installs and configures containerd runtime
- **CRI-O Support**: Alternative container runtime option
- **Auto-Detection**: Analyzes existing cluster nodes to match runtime

#### **CNI Plugin Integration**
-  **Calico**: Full support with automatic configuration
-  **Cilium**: Advanced networking with eBPF support
-  **Flannel**: Lightweight overlay networking
-  **Auto-Detection**: Matches CNI plugin used by existing cluster nodes

#### **Complete Kubernetes Setup**
- **System Preparation**: Configures system requirements (swap, IP forwarding, hostname)
- **Package Installation**: Installs kubelet and kubectl with the cluster's Kubernetes version
- **Service Configuration**: Sets up systemd services and startup scripts
- **Node Labeling**: Applies proper Karpenter and workload labels
- **Bootstrap Process**: Starts kubelet with a claim-bound bootstrap kubeconfig

### Customization Options

### Kubelet Configuration Overrides

For VPC bootstrap, you can override a subset of kubelet configuration directly from the `IBMNodeClass`. These settings are rendered into the kubelet `config.yaml` on the node and validated by the CRD.

```yaml
apiVersion: karpenter-ibm.sh/v1alpha1
kind: IBMNodeClass
metadata:
  name: vpc-bootstrap-kubelet
spec:
  resourceGroup: replace-with-resource-group-id
  region: us-south
  zone: us-south-1
  vpc: "r006-a8efb117-fd5e-4f63-ae16-4fb9faafa4ff"
  image: ubuntu-24-04-amd64
  apiServerEndpoint: "https://10.240.0.1:6443"
  bootstrapMode: cloud-init
  securityGroups:
    - "r006-12345678-1234-1234-1234-123456789012"

  kubelet:
    clusterDNS:
      - 10.96.0.10
      - 10.96.0.11

    maxPods: 150
    podsPerCore: 10

    kubeReserved:
      cpu: "200m"
      memory: "512Mi"

    systemReserved:
      cpu: "100m"
      memory: "256Mi"

    evictionHard:
      memory.available: "500Mi"

    evictionSoft:
      memory.available: "1Gi"

    evictionSoftGracePeriod:
      memory.available: "1m0s"

    evictionMaxPodGracePeriod: 120

    imageGCHighThresholdPercent: 85
    imageGCLowThresholdPercent: 70

    cpuCFSQuota: true

```
* **Reserved resources**
    * `systemReserved` and `kubeReserved` keys must be one of:
      `cpu`, `memory`, `ephemeral-storage`, `pid`.
    * Values must not be negative (strings starting with `-` are rejected).

* **Eviction settings**
    * `evictionHard`, `evictionSoft`, and `evictionSoftGracePeriod` may only use:
      `memory.available`, `nodefs.available`, `nodefs.inodesFree`,
      `imagefs.available`, `imagefs.inodesFree`, `pid.available`.
    * Every key in `evictionSoft` must exist in `evictionSoftGracePeriod`.
    * Every key in `evictionSoftGracePeriod` must exist in `evictionSoft`.

* **Image garbage collection**
    * `imageGCHighThresholdPercent` and `imageGCLowThresholdPercent` must be in
      the range `[0, 100]`.
    * If both are set, `imageGCLowThresholdPercent` must be **less than**
      `imageGCHighThresholdPercent`.

For a complete reference, see `examples/kubelet-configuration.yaml`.

#### **Custom User Data**
```yaml
spec:
  userDataAppend: |
    #!/bin/bash
    # Your custom bootstrap extension
    echo "Installing custom packages..."
    apt-get update && apt-get install -y htop vim

    # Custom environment variables
    echo "CUSTOM_VAR=value" >> /etc/environment

    # Custom service configuration
    systemctl enable my-custom-service
```

Runtime and CNI are discovered from the cluster.

## IKS Bootstrap (Experimental)

### When to Use
- **IBM Kubernetes Service (IKS) clusters** with existing worker pools
- **Consistent worker pool management** across teams

### Configuration
```yaml
apiVersion: karpenter-ibm.sh/v1alpha1
kind: IBMNodeClass
metadata:
  name: iks-bootstrap-nodeclass
spec:
  resourceGroup: replace-with-resource-group-id
  apiServerEndpoint: "https://<INTERNAL-API-SERVER-IP>:6443"
  region: us-south
  zone: us-south-1
  vpc: vpc-iks-12345
  image: r006-ubuntu-20-04

  # IKS-specific configuration
  iksClusterID: "cluster-12345678"        # Required: Your IKS cluster ID
  iksWorkerPoolID: "pool-default"         # Optional: flavor template
  subnet: 0717-replace-with-subnet-id
  iksDynamicPools:
    enabled: true
```

### Features

#### **Native IKS Integration**
- **Worker Pool API**: Creates a dedicated worker pool for each NodeClaim; shared pools are not resized
- **Automatic Registration**: Nodes automatically join IKS cluster through worker pools

### Important Constraints

#### **Instance Type Limitations**
- **Constraint**: Cannot dynamically select instance types in IKS mode
- **Reason**: IKS Worker Pool API uses pre-configured instance types
- **Impact**: `instanceProfile` and `instanceRequirements` are ignored
- **Solution**: Pre-create worker pools for different instance types

```yaml
# Example: Multiple NodeClasses for different instance types
---
apiVersion: karpenter-ibm.sh/v1alpha1
kind: IBMNodeClass
metadata:
  name: iks-small-instances
spec:
  region: us-south
  vpc: replace-with-vpc-id
  resourceGroup: replace-with-resource-group-id
  apiServerEndpoint: "https://<INTERNAL-API-SERVER-IP>:6443"
  zone: us-south-1
  subnet: 0717-replace-with-subnet-id
  iksDynamicPools:
    enabled: true
  iksClusterID: "cluster-12345678"
  iksWorkerPoolID: "pool-small"     # Pre-configured with bx2-2x8
---
apiVersion: karpenter-ibm.sh/v1alpha1
kind: IBMNodeClass
metadata:
  name: iks-large-instances
spec:
  region: us-south
  vpc: replace-with-vpc-id
  resourceGroup: replace-with-resource-group-id
  apiServerEndpoint: "https://<INTERNAL-API-SERVER-IP>:6443"
  zone: us-south-1
  subnet: 0717-replace-with-subnet-id
  iksDynamicPools:
    enabled: true
  iksClusterID: "cluster-12345678"
  iksWorkerPoolID: "pool-large"     # Pre-configured with bx2-8x32
```

### Requirements

#### **IKS Cluster Access**
- Valid IKS cluster ID in same region as nodes
- API key with IKS cluster access permissions
- Worker pools pre-configured with desired instance types

## Advanced Configuration

### Environment Variables
`BOOTSTRAP_MODE` selects the controller default; a class can override it. Kubelet settings belong in `spec.kubelet`.

Kubelet pod capacity defaults to 110. `maxPods` and `podsPerCore` constrain both advertised capacity and the guest configuration. Treat `userData` and `userDataAppend` as privileged root scripts; restrict NodeClass writes to trusted administrators.

Bootstrap failures appear as `BootstrapFailed` claim Events and in `kube-system/karpenter-bootstrap-<claim UID>`. The guest can get/patch only its named status ConfigMap.

## Troubleshooting Bootstrap Issues

### Common Problems and Solutions

#### **Bootstrap Script Debugging**
```bash
# Check cloud-init logs on the instance
ssh ubuntu@<instance-ip> "sudo journalctl -u cloud-final"
ssh ubuntu@<instance-ip> "sudo tail -f /var/log/cloud-init-output.log"

# View the generated bootstrap script
ssh ubuntu@<instance-ip> "sudo cat /var/lib/cloud/instance/scripts/*"

# Check bootstrap script execution status
ssh ubuntu@<instance-ip> "sudo systemctl status cloud-final"
```

#### **Cluster Join Failures**

**VPC Clusters - Wrong API Endpoint (Most Common)**:
```bash
# Symptoms: Timeout errors, nodes never register
# Check if using correct INTERNAL endpoint, not external

# 1. Find correct internal API endpoint
kubectl get endpointslice -n default -l kubernetes.io/service-name=kubernetes

# 2. Update NodeClass with internal endpoint
kubectl patch ibmnodeclass your-nodeclass --type='merge' \
  -p='{"spec":{"apiServerEndpoint":"https://<INTERNAL-IP>:6443"}}'

# 3. Verify connectivity from worker instance
ssh ubuntu@<instance-ip> "telnet <INTERNAL-IP> 6443"
```

**Bootstrap Token Issues**:
```bash
# Check if bootstrap tokens are being created
kubectl get secrets -n kube-system | grep bootstrap-token

# Verify RBAC permissions exist
kubectl get clusterrolebindings | grep karpenter-ibm-bootstrap-nodes

# Check token authentication on instance
ssh ubuntu@<instance-ip> "sudo cat /var/lib/kubelet/bootstrap-kubeconfig"
```

**General Debugging**:
```bash
# Check kubelet status and logs
ssh ubuntu@<instance-ip> "sudo systemctl status kubelet"
ssh ubuntu@<instance-ip> "sudo journalctl -u kubelet --no-pager -n 50"

# Verify cluster connectivity (use INTERNAL endpoint)
ssh ubuntu@<instance-ip> "curl --cacert /etc/kubernetes/pki/ca.crt https://<INTERNAL-IP>:6443/healthz"

# For direct kubelet bootstrap (not kubeadm)
ssh ubuntu@<instance-ip> "sudo journalctl -u kubelet | grep -E '(bootstrap|token|certificate)'"
```

#### **Network Connectivity Issues**
```bash
# Test DNS resolution
ssh ubuntu@<instance-ip> "nslookup kubernetes.default.svc.cluster.local"

# Check if required ports are accessible
ssh ubuntu@<instance-ip> "nc -zv CLUSTER_ENDPOINT 6443"

# Verify security group rules allow cluster communication
ibmcloud is security-group <security-group-id> --output json
```

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
Auto Bootstrap should select the right bootstrap method based on your configuration:

1. **IKS Detection**: If `iksClusterID` is provided and accessible → Uses IKS Bootstrap
2. **VPC Fallback**: Otherwise → Uses VPC Bootstrap with automatic cluster discovery

### Configuration
```yaml
apiVersion: karpenter-ibm.sh/v1alpha1
kind: IBMNodeClass
metadata:
  name: auto-bootstrap-nodeclass
spec:
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
- **Token Management**: Generates and refreshes bootstrap tokens automatically with generic RBAC
- **Network Detection**: Discovers cluster CIDR and DNS configuration
- **System Configuration**: Enables IP forwarding, disables swap, configures hostname
- **Runtime Selection**: Auto-detects and configures container runtime (containerd/crio)

### Bootstrap Token RBAC Design

The provider uses a **generic RBAC approach** for bootstrap tokens:

#### **Single Role for All NodePools**
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
  region: us-south
  zone: us-south-1
  vpc: vpc-12345678
  image: r006-ubuntu-20-04

  # Explicit VPC bootstrap mode (optional - auto-detected)
  bootstrapMode: vpc

  # Optional custom pre-bootstrap setup
  userData: |
    #!/bin/bash
    echo "Custom pre-bootstrap configuration"
    # Bootstrap script automatically appended
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
- **Package Installation**: Installs kubelet, kubeadm, kubectl with correct versions
- **Service Configuration**: Sets up systemd services and startup scripts
- **Node Labeling**: Applies proper Karpenter and workload labels
- **Bootstrap Process**: Executes kubeadm join with proper configuration

### Customization Options

#### **Custom User Data**
```yaml
spec:
  userData: |
    #!/bin/bash
    # Your custom pre-bootstrap configuration
    echo "Installing custom packages..."
    apt-get update && apt-get install -y htop vim

    # Custom environment variables
    echo "CUSTOM_VAR=value" >> /etc/environment

    # Custom service configuration
    systemctl enable my-custom-service
```

#### **Environment Variables**
```bash
# Override container runtime
export CONTAINER_RUNTIME=crio

# Custom CNI configuration
export CNI_PLUGIN=cilium

# Debug mode
export DEBUG=true
```

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
  region: us-south
  zone: us-south-1
  vpc: vpc-iks-12345
  image: r006-ubuntu-20-04

  # IKS-specific configuration
  iksClusterID: "cluster-12345678"        # Required: Your IKS cluster ID
  iksWorkerPoolID: "pool-default"         # Optional: specific worker pool

  # Optional: Custom post-registration setup
  userData: |
    #!/bin/bash
    echo "Post-IKS registration customization"
    # Custom configuration after node joins IKS cluster
```

### Features

#### **Native IKS Integration**
- **Worker Pool API**: Uses IBM Kubernetes Service worker pool resize APIs
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
  iksClusterID: "cluster-12345678"
  iksWorkerPoolID: "pool-small"     # Pre-configured with bx2-2x8
---
apiVersion: karpenter-ibm.sh/v1alpha1
kind: IBMNodeClass
metadata:
  name: iks-large-instances
spec:
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
All bootstrap modes support environment variable customization:

```bash
# Bootstrap behavior
export LOG_LEVEL=debug                   # Enhanced logging
export BOOTSTRAP_TIMEOUT=600             # Bootstrap timeout in seconds

# Container runtime preferences
export CONTAINER_RUNTIME=containerd      # or crio
export CONTAINERD_VERSION=1.7.22         # Specific version

# CNI plugin preferences
export CNI_PLUGIN=calico                 # or cilium, flannel
export CNI_VERSION=v3.29.0               # Specific CNI version

# System configuration
export ENABLE_IP_FORWARDING=true         # Enable IP forwarding
export DISABLE_SWAP=true                 # Disable swap
export HOSTNAME_STRATEGY=ibm-cloud       # Hostname configuration strategy
```

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
ssh ubuntu@<instance-ip> "curl -k https://<INTERNAL-IP>:6443/healthz"

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

# VPC Integration Guide

This guide focuses on using Karpenter IBM Cloud Provider with self-managed Kubernetes clusters running on IBM Cloud VPC infrastructure.

## Overview

VPC integration provides flexible node provisioning for self-managed Kubernetes clusters with full control over cluster configuration and automatic bootstrap capabilities.

### Key Benefits
- **Automatic Bootstrap**: Zero-configuration node joining with intelligent cluster discovery
- **Dynamic Instance Selection**: Full flexibility in instance type selection based on workload requirements
- **Custom Configurations**: Support for specialized setups (GPU, HPC, security hardening)

## Prerequisites

### Infrastructure Requirements
- **Self-Managed Kubernetes**: Running on IBM Cloud VPC instances
- **VPC Infrastructure**: VPC with subnets, security groups, and network configuration
- **API Access**: Service ID with VPC Infrastructure Services permissions
- **Network Connectivity**: Proper security groups allowing cluster communication

### Required Information
Gather the following before starting:
```bash
# List your VPCs
ibmcloud is vpcs --output json

# List subnets in your VPC
ibmcloud is subnets --vpc <vpc-id> --output json

# List security groups
ibmcloud is security-groups --vpc <vpc-id> --output json

# List available images
ibmcloud is images --visibility public --status available | grep ubuntu
```

## Quick Setup

### Step 1: Install Karpenter
```bash
# Create namespace and secrets
kubectl create namespace karpenter

kubectl create secret generic karpenter-ibm-credentials \
  --from-literal=ibmApiKey="your-general-api-key" \
  --from-literal=vpcApiKey="your-vpc-api-key" \
  --namespace karpenter

# Install via Helm
helm repo add karpenter-ibm https://karpenter-ibm.sh
helm repo update
helm install karpenter karpenter-ibm/karpenter-ibm \
  --namespace karpenter \
  --create-namespace \
  --set controller.env.IBM_REGION="us-south"
```

### Step 2: Create VPC NodeClass

⚠️ **CRITICAL CONFIGURATION REQUIREMENTS:**
- **API Server Endpoint is REQUIRED** - Without it, nodes cannot join the cluster
- **Use Resource IDs, NOT Names** - Security groups, VPC, and subnet must be IDs
- **Bootstrap Mode** - Explicitly set

```yaml
apiVersion: karpenter.ibm.sh/v1alpha1
kind: IBMNodeClass
metadata:
  name: vpc-nodeclass
  annotations:
    karpenter.ibm.sh/description: "VPC self-managed cluster NodeClass"
spec:
  # REQUIRED: Replace with your actual resource IDs (NOT names!)
  region: us-south                                        # Your IBM Cloud region
  zone: us-south-1                                        # Target availability zone
  vpc: "r006-4225852b-4846-4a4a-88c4-9966471337c6"       # VPC ID format: r###-########-####-####-####-############
  image: "r006-dd3c20fa-71d3-4dc0-913f-2f097bf3e500"     # Image ID (recommended) or name

  # CRITICAL: API Server Endpoint - nodes CANNOT join without this!
  apiServerEndpoint: "https://10.240.0.1:6443"           # Your cluster's INTERNAL API endpoint

  # REQUIRED: Bootstrap mode for VPC clusters
  bootstrapMode: cloud-init                               # Valid: cloud-init, iks, user-data

  # REQUIRED: Security groups (must be IDs, not names!)
  securityGroups:
    - "r006-36f045e2-86a1-4af8-917e-b17a41f8abe3"       # ID format: r###-########-####-####-####-############
    # ❌ NOT "sg-k8s-workers" - names will cause validation errors!

  # Optional: Specific subnet (must be ID if specified)
  subnet: "02c7-718345b5-2de1-4a9a-b1de-fa7e307ee8c5"   # Format: ####-########-####-####-####-############

  # Optional: Instance requirements for filtering available instance types
  # If neither instanceRequirements nor instanceProfile are specified,
  # instance types will be selected based on NodePool requirements
  instanceRequirements:
    architecture: amd64                 # CPU architecture: amd64, arm64, s390x
    minimumCPU: 2                       # Minimum vCPUs required
    minimumMemory: 4                    # Minimum memory in GiB
    maximumHourlyPrice: "1.00"          # Maximum hourly price in USD

  # Optional: Specific instance profile (mutually exclusive with instanceRequirements)
  # instanceProfile: bx2-4x16           # Uncomment to use a specific instance type

  # Optional: Placement strategy for zone/subnet selection
  placementStrategy:
    zoneBalance: Balanced               # Balanced, AvailabilityFirst, or CostOptimized

  # Optional: SSH access for troubleshooting
  # To find SSH key IDs: ibmcloud is keys --output json | jq '.[] | {name, id}'
  sshKeys:
  - r010-12345678-1234-1234-1234-123456789012  # SSH key ID

  # Resource group (supports both name and ID)
  resourceGroup: my-resource-group       # Resource group name (automatically resolved to ID)
  # OR:
  # resourceGroup: rg-12345678          # Resource group ID (used directly)
  # If omitted, instances are created in the account's default resource group

  # Optional: Placement target (dedicated host or placement group)
  placementTarget: ph-12345678

  # Optional: Tags to apply to instances
  tags:
    environment: production
    team: devops

  # Optional: Bootstrap mode (cloud-init, iks-api, or auto)
  bootstrapMode: cloud-init

  # REQUIRED: Internal API server endpoint (find with: kubectl get endpointslice -n default -l kubernetes.io/service-name=kubernetes)
  apiServerEndpoint: "https://<INTERNAL-API-SERVER-IP>:6443"

  # Optional: IKS cluster ID (required when bootstrapMode is "iks-api")
  iksClusterID: bng6n48d0t6vj7b33kag

  # Optional: IKS worker pool ID (for IKS API bootstrapping)
  iksWorkerPoolID: bng6n48d0t6vj7b33kag-pool1

  # Optional: Load balancer integration
  loadBalancerIntegration:
    enabled: true
    targetGroups:
    - loadBalancerID: r010-12345678-1234-5678-9abc-def012345678
      poolName: web-servers
      port: 80
      weight: 50
    autoDeregister: true
    registrationTimeout: 300

  # VPC mode uses automatic bootstrap - no userData required!
```

### Step 3: Create NodePool
```yaml
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: vpc-nodepool
spec:
  template:
    metadata:
      labels:
        provisioner: karpenter-vpc
        cluster-type: self-managed
    spec:
      nodeClassRef:
        apiVersion: karpenter.ibm.sh/v1alpha1
        kind: IBMNodeClass
        name: vpc-nodeclass

      # Full flexibility in instance requirements
      requirements:
      - key: node.kubernetes.io/instance-type
        operator: In
        values: ["bx2-2x8", "bx2-4x16", "cx2-2x4", "cx2-4x8", "mx2-2x16"]
      - key: kubernetes.io/arch
        operator: In
        values: ["amd64"]
      - key: karpenter.sh/capacity-type
        operator: In
        values: ["on-demand"]

  limits:
    cpu: 1000
    memory: 1000Gi

  disruption:
    consolidationPolicy: WhenEmpty
    consolidateAfter: 30s
```

## VPC Bootstrap Features

### API Endpoint Discovery (Critical for VPC Clusters)

**Important**: VPC clusters must use the **internal API endpoint**, not the external kubectl endpoint.

#### Finding the Correct Internal API Endpoint

```bash
# Method 1: Get actual API server endpoint (RECOMMENDED)
kubectl get endpointslice -n default -l kubernetes.io/service-name=kubernetes

# Example output:
# NAME         ADDRESSTYPE   PORTS   ENDPOINTS       AGE
# kubernetes   IPv4          6443    <INTERNAL-IP>   15d

# Use: https://<INTERNAL-IP>:6443
```

```bash
# Method 2: Check kubernetes service (alternative)
kubectl get svc kubernetes -o jsonpath='{.spec.clusterIP}'
# Returns cluster IP (e.g., <CLUSTER-IP>) - use https://<CLUSTER-IP>:443
```

#### Configuring IBMNodeClass with Correct Endpoint

```yaml
apiVersion: karpenter.ibm.sh/v1alpha1
kind: IBMNodeClass
metadata:
  name: vpc-nodeclass
spec:
  # CRITICAL: Use INTERNAL endpoint from discovery above
  apiServerEndpoint: "https://<INTERNAL-IP>:6443"

  region: us-south
  vpc: vpc-12345678
  # ... rest of config
```

### Automatic Cluster Discovery
The VPC integration automatically discovers your cluster configuration:

- **API Endpoint**: Uses the internal cluster API server endpoint you configure
- **CA Certificate**: Extracts cluster CA certificate from existing nodes
- **DNS Configuration**: Discovers cluster DNS service IP and search domains
- **Network Settings**: Detects cluster pod and service CIDR ranges
- **Runtime Detection**: Matches container runtime used by existing nodes

### Zero Configuration Bootstrap
```yaml
# Minimal configuration - everything else is automatic
apiVersion: karpenter.ibm.sh/v1alpha1
kind: IBMNodeClass
metadata:
  name: minimal-vpc
spec:
  region: us-south
  zone: us-south-1
  vpc: vpc-12345678
  image: r006-ubuntu-20-04
  securityGroups:
  - sg-default
  # No userData needed - bootstrap is fully automatic!
```

## Advanced VPC Configurations

### Multi-Zone VPC Setup
```yaml
# Zone 1
---
apiVersion: karpenter.ibm.sh/v1alpha1
kind: IBMNodeClass
metadata:
  name: vpc-us-south-1
spec:
  region: us-south
  zone: us-south-1
  vpc: vpc-12345678
  subnet: subnet-zone1-12345
  image: r006-ubuntu-20-04
  securityGroups:
  - sg-k8s-workers
---
# Zone 2
apiVersion: karpenter.ibm.sh/v1alpha1
kind: IBMNodeClass
metadata:
  name: vpc-us-south-2
spec:
  region: us-south
  zone: us-south-2
  vpc: vpc-12345678
  subnet: subnet-zone2-12345
  image: r006-ubuntu-20-04
  securityGroups:
  - sg-k8s-workers
```

### GPU Workloads
```yaml
apiVersion: karpenter.ibm.sh/v1alpha1
kind: IBMNodeClass
metadata:
  name: vpc-gpu
spec:
  region: us-south
  zone: us-south-1
  vpc: vpc-gpu-12345
  image: r006-ubuntu-20-04
  instanceProfile: gx2-8x64x1v100      # GPU instance type
  securityGroups:
  - sg-gpu-workloads
  userData: |
    #!/bin/bash
    # GPU drivers and configuration
    apt-get update
    apt-get install -y nvidia-driver-470 nvidia-container-toolkit

    # Configure containerd for GPU support
    mkdir -p /etc/containerd
    cat > /etc/containerd/config.toml <<EOF
    [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.nvidia]
      runtime_type = "io.containerd.runc.v2"
      [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.nvidia.options]
        BinaryName = "/usr/bin/nvidia-container-runtime"
    EOF

    # Bootstrap script automatically appended
```

### High-Performance Computing (HPC)
```yaml
apiVersion: karpenter.ibm.sh/v1alpha1
kind: IBMNodeClass
metadata:
  name: vpc-hpc
spec:
  region: us-south
  zone: us-south-1
  vpc: vpc-hpc-12345
  image: r006-ubuntu-20-04
  instanceProfile: cx2-32x64           # High-performance instance
  securityGroups:
  - sg-hpc-cluster
  userData: |
    #!/bin/bash
    # HPC optimizations
    echo performance > /sys/devices/system/cpu/cpu*/cpufreq/scaling_governor

    # Memory optimizations
    echo 'vm.swappiness = 1' >> /etc/sysctl.conf
    echo 'vm.dirty_ratio = 15' >> /etc/sysctl.conf

    # Install HPC libraries
    apt-get update && apt-get install -y \
      openmpi-bin openmpi-common libopenmpi-dev \
      libblas3 liblapack3

    # Network optimizations for high-throughput
    echo 'net.core.rmem_max = 134217728' >> /etc/sysctl.conf
    echo 'net.core.wmem_max = 134217728' >> /etc/sysctl.conf
```

### Custom CNI Configuration
```yaml
apiVersion: karpenter.ibm.sh/v1alpha1
kind: IBMNodeClass
metadata:
  name: vpc-custom-cni
spec:
  region: us-south
  zone: us-south-1
  vpc: vpc-custom-12345
  image: r006-ubuntu-20-04
  userData: |
    #!/bin/bash
    # Custom CNI setup before cluster join

    # Install Cilium CNI
    curl -L -o /opt/cni/bin/cilium-cni \
      https://github.com/cilium/cilium/releases/download/v1.14.0/cilium-linux-amd64.tar.gz

    # Custom CNI configuration
    mkdir -p /etc/cni/net.d
    cat > /etc/cni/net.d/05-cilium.conf <<EOF
    {
      "cniVersion": "0.4.0",
      "name": "cilium",
      "type": "cilium-cni",
      "enable-debug": false
    }
    EOF

    # Bootstrap script handles the rest
```

## Dynamic Instance Selection

Unlike IKS mode, VPC integration provides full flexibility in instance type selection. There are three ways to control instance selection:

### 1. NodePool Requirements (Recommended)
When neither `instanceProfile` nor `instanceRequirements` are specified in the NodeClass, instance types are selected based on NodePool requirements. This provides maximum flexibility:

### 2. NodeClass Instance Requirements
You can set filtering criteria in the NodeClass to limit available instance types globally for all NodePools using that NodeClass.

### 3. NodeClass Specific Instance Profile
You can lock the NodeClass to a single instance type, though this limits flexibility.

**Example: NodePool-driven selection (most flexible approach):**

```yaml
# NodeClass with NO instance specifications - lets NodePool control everything
apiVersion: karpenter.ibm.sh/v1alpha1
kind: IBMNodeClass
metadata:
  name: flexible-nodeclass
spec:
  region: us-south
  vpc: "r006-your-vpc-id"
  image: "r006-your-image-id"
  securityGroups: ["r006-your-sg-id"]
  apiServerEndpoint: "https://10.240.0.1:6443"
  bootstrapMode: cloud-init
  # Note: No instanceProfile or instanceRequirements specified
---
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: flexible-nodepool
spec:
  template:
    spec:
      nodeClassRef:
        name: flexible-nodeclass
      requirements:
      # Karpenter will choose the best instance type based on pod requirements
      - key: node.kubernetes.io/instance-type
        operator: In
        values: [
          "bx2-2x8", "bx2-4x16", "bx2-8x32",    # Balanced instances
          "cx2-2x4", "cx2-4x8", "cx2-8x16",     # Compute optimized
          "mx2-2x16", "mx2-4x32", "mx2-8x64"    # Memory optimized
        ]
      - key: karpenter.ibm.sh/instance-family
        operator: In
        values: ["bx2", "cx2", "mx2"]
      # Resource-based selection
      - key: kubernetes.io/arch
        operator: In
        values: ["amd64"]
      - key: karpenter.sh/capacity-type
        operator: In
        values: ["on-demand"]
```

## VPC-Specific Troubleshooting

### Bootstrap Issues

#### Wrong API Endpoint Configuration

**Symptoms**:
- NodeClaims created but nodes never register with cluster
- Kubelet logs show: `"Client.Timeout exceeded while awaiting headers"`
- Node status remains "Unknown" with "Drifted" = True

**Solution**:
```bash
# 1. Find correct internal endpoint
kubectl get endpointslice -n default -l kubernetes.io/service-name=kubernetes

# 2. Update NodeClass with internal endpoint (NOT external)
kubectl patch ibmnodeclass your-nodeclass --type='merge' \
  -p='{"spec":{"apiServerEndpoint":"https://<INTERNAL-IP>:6443"}}'

# 3. Delete old NodeClaims to trigger new ones with correct config
kubectl delete nodeclaims --all

# 4. Monitor node registration
kubectl get nodes -w
```

**Verification**:
```bash
# Test connectivity from worker instance
ssh ubuntu@<node-ip> "telnet <INTERNAL-IP> 6443"
# Should connect successfully, not timeout

# Check kubelet logs
ssh ubuntu@<node-ip> "sudo journalctl -u kubelet -f"
# Should see successful API server communication
```

#### Cluster Discovery Failures
```bash
# Check if controller can reach cluster API
kubectl logs -n karpenter deployment/karpenter | grep "cluster discovery"

# Verify internal API endpoint is accessible
ssh ubuntu@<node-ip> "curl -k https://INTERNAL_API_ENDPOINT/healthz"

# Check security group rules
ibmcloud is security-group <sg-id> --output json | jq '.rules'
```

#### Bootstrap Script Problems
```bash
# View generated bootstrap script
ssh ubuntu@<node-ip> "sudo cat /var/lib/cloud/instance/scripts/*"

# Check cloud-init logs
ssh ubuntu@<node-ip> "sudo journalctl -u cloud-final"

# Monitor bootstrap execution
ssh ubuntu@<node-ip> "sudo tail -f /var/log/cloud-init-output.log"
```

### Network Connectivity
```bash
# Test cluster communication
ssh ubuntu@<node-ip> "nc -zv CLUSTER_ENDPOINT 6443"

# Check DNS resolution
ssh ubuntu@<node-ip> "nslookup kubernetes.default.svc.cluster.local"

# Verify route table
ibmcloud is vpc-routes <vpc-id>
```

### Instance Provisioning
```bash
# Check available instances in zone
ibmcloud is instance-profiles --output json | jq '.[] | select(.family=="bx2")'

# Monitor quota usage
ibmcloud is instances --output json | jq 'length'

# Check subnet capacity
ibmcloud is subnet <subnet-id> --output json | jq '.available_ipv4_address_count'
```

#### CNI Initialization Timing Issues

**Problem**: Newly provisioned nodes may be terminated prematurely before CNI (Calico) fully initializes.

**Symptoms**:
- Nodes created successfully but pods fail with CNI errors
- `Failed to create pod sandbox: plugin type="calico" failed (add): stat /var/lib/calico/nodename: no such file or directory`
- Nodes get terminated and recreated in a loop

**Root Cause**: Karpenter's `consolidateAfter` setting is too aggressive, terminating nodes before CNI initialization completes.

**Solution**:
```yaml
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: vpc-nodepool
spec:
  disruption:
    consolidationPolicy: WhenEmpty
    consolidateAfter: 300s  # Wait 5 minutes for CNI initialization
```

**Verification**:
```bash
# Check if Calico pods are running on new nodes
kubectl get pods -n kube-system -o wide | grep calico-node

# Monitor CNI status on a node
ssh ubuntu@<node-ip> "sudo ls -la /var/lib/calico/"

# Check for CNI-related events
kubectl get events --field-selector involvedObject.kind=Pod | grep -i sandbox
```

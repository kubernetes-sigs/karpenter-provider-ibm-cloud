# IKS Integration Guide

This guide focuses specifically on using Karpenter IBM Cloud Provider with IBM Kubernetes Service (IKS) clusters.

## Overview

The IKS integration provides experimental auto-scaling for IBM Kubernetes Service clusters by leveraging existing worker pools and IBM-managed infrastructure.

## Prerequisites

### IKS Cluster Requirements
- **IKS Cluster**: Running IBM Kubernetes Service cluster
- **Worker Pools**: Pre-configured worker pools for different instance types
- **API Access**: Service ID with IKS cluster access permissions
- **Network Configuration**: VPC with proper security groups

### Required Information
Gather the following before starting:
```bash
# Get your IKS cluster ID
ibmcloud ks clusters --provider vpc-gen2

# List existing worker pools
ibmcloud ks worker-pools --cluster <cluster-id>

# Get cluster details
ibmcloud ks cluster get --cluster <cluster-id>
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

### Step 2: Create IKS NodeClass
```yaml
apiVersion: karpenter-ibm.sh/v1alpha1
kind: IBMNodeClass
metadata:
  name: iks-nodeclass
  annotations:
    karpenter-ibm.sh/description: "IKS integration NodeClass"
spec:
  # REQUIRED: Replace with your actual values
  region: us-south                      # Your IBM Cloud region
  vpc: vpc-iks-12345678                 # Your IKS cluster VPC
  image: r006-12345678                  # Ubuntu 20.04 or cluster-compatible image

  # IKS-SPECIFIC CONFIGURATION
  bootstrapMode: iks-api                # Use IKS API for node bootstrapping
  iksClusterID: "cluster-12345678"      # Your IKS cluster ID (required for iks-api mode)
  iksWorkerPoolID: "pool-default"       # Optional: specific worker pool

  # Security and networking
  securityGroups:
  - sg-iks-workers                      # IKS worker security group

  # Optional: SSH access for troubleshooting
  sshKeys:
  - key-iks-access
```

### Step 3: Create NodePool
```yaml
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: iks-nodepool
spec:
  template:
    metadata:
      labels:
        provisioner: karpenter-iks
        cluster-type: iks
    spec:
      nodeClassRef:
        apiVersion: karpenter-ibm.sh/v1alpha1
        kind: IBMNodeClass
        name: iks-nodeclass

      # Instance requirements (limited by worker pool configuration)
      requirements:
      - key: node.kubernetes.io/instance-type
        operator: In
        values: ["bx2-4x16"]  # Must match worker pool instance type
      - key: kubernetes.io/arch
        operator: In
        values: ["amd64"]

  limits:
    cpu: 1000
    memory: 1000Gi

  disruption:
    consolidationPolicy: WhenEmpty
    consolidateAfter: 30s
```

## Important IKS Constraints

### Instance Type Limitations
**Critical**: IKS mode cannot dynamically select instance types because worker pools have pre-configured instance types.

See [IKS Mode Instance Type Constraints](limitations.md#iks-mode-instance-type-constraints) for more details.

## IKS-Specific Troubleshooting

### Common IKS Issues

#### Worker Pool Not Found
```bash
# Verify worker pool exists
ibmcloud ks worker-pools --cluster <cluster-id>

# Check worker pool details
ibmcloud ks worker-pool get --cluster <cluster-id> --worker-pool <pool-id>
```

#### E3917 API Errors
```bash
# Check if CLI fallback is working
kubectl logs -n karpenter deployment/karpenter | grep -i "e3917\|cli"

# Verify IBM Cloud CLI is available in container
kubectl exec -n karpenter deployment/karpenter -- ibmcloud version
```

#### Instance Type Mismatches
```bash
# Check worker pool instance configuration
ibmcloud ks worker-pool get --cluster <cluster-id> --worker-pool <pool-id> --output json

# Verify NodePool requirements match worker pool
kubectl describe nodepool <nodepool-name>
```

### Monitoring IKS Integration
```bash
# Watch worker pool scaling
ibmcloud ks workers --cluster <cluster-id> --worker-pool <pool-id>

# Monitor Karpenter events
kubectl get events --field-selector reason=SuccessfulCreate

# Check node registration
kubectl get nodes -l karpenter.sh/provisioner-name=<nodepool-name>
```

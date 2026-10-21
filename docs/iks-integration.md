# IKS Integration Guide

This guide focuses specifically on using Karpenter IBM Cloud Provider with IBM Kubernetes Service (IKS) clusters.

## Overview

The IKS integration provides experimental auto-scaling for IBM Kubernetes Service clusters through dedicated worker pools with IBM-managed infrastructure. Each NodeClaim owns one single-zone pool and one real worker.

## Prerequisites

### IKS Cluster Requirements
- **IKS Cluster**: Running IBM Kubernetes Service cluster
- **Worker Pools**: Permission and quota to create and delete a dedicated pool for each NodeClaim
- **Account Identity**: Set Helm's `credentials.accountId` to the 32-character hexadecimal account ID containing the IKS cluster; this sets `IBM_ACCOUNT_ID` on the controller
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
# Install via Helm
helm repo add karpenter-ibm https://karpenter-ibm.sh
helm repo update
helm install karpenter karpenter-ibm/karpenter-ibm \
  --namespace karpenter \
  --create-namespace \
  --values iks-values.yaml
```

Create `iks-values.yaml` with the account ID and credentials for your cluster:

```yaml
credentials:
  accountId: "replace-with-32-character-account-id"
  ibmApiKey: "replace-with-api-key"
  vpcApiKey: "replace-with-vpc-api-key"
  region: us-south
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
  apiServerEndpoint: "https://<INTERNAL-API-SERVER-IP>:6443"
  # REQUIRED: Replace with your actual values
  region: us-south                      # Your IBM Cloud region
  vpc: vpc-iks-12345678                 # Your IKS cluster VPC
  image: r006-12345678                  # Ubuntu 20.04 or cluster-compatible image

  # IKS-SPECIFIC CONFIGURATION
  bootstrapMode: iks-api                # Use IKS API for node bootstrapping
  iksClusterID: "cluster-12345678"      # Your IKS cluster ID (required for iks-api mode)
  iksWorkerPoolID: "pool-default"       # Optional: flavor template; this pool is not resized
  instanceProfile: bx2-4x16
  zone: us-south-1
  subnet: 0717-replace-with-subnet-id
  resourceGroup: replace-with-resource-group-id
  iksDynamicPools:
    enabled: true
    allowedInstanceTypes: ["bx2-4x16"]

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
        group: karpenter-ibm.sh
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
`iksDynamicPools.enabled: true` is required. Shared worker pools are not resized because their scale-down operation cannot identify the worker owned by a particular NodeClaim. An optional `iksWorkerPoolID` supplies the flavor for a new dedicated pool. The chosen flavor, zone, and resources must satisfy the NodeClaim requirements.

Launch completes once the worker exists and reports the flavor's catalog capacity until the Node registers its own resources. The claim stores its original cluster, account, pool, and worker identities before returning a provider ID. Deletion removes only that owned allocation after Karpenter drains the Node. Do not enable another autoscaler on these pools.

A lost cloud response can leave an allocation uncertain. Its finalizer and reservation remain in place until the controller can prove ownership and absence; investigate the reported cloud error rather than removing the finalizer.

See [IKS Mode Instance Type Constraints](limitations.md#iks-mode-instance-type-constraints) for more details.

The IKS clients share a per-process limit of 10 HTTP requests/second with burst 1. Replicas and other processes sharing a public IP need an aggregate limit outside the controller. Legacy shared-pool workers require the [retirement identity checks](troubleshooting.md#upgrade-and-cleanup-recovery); retiring one worker never resizes or deletes its shared pool.

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

# Current Limitations and Constraints

This document outlines the current limitations, constraints, and known issues with the Karpenter IBM Cloud Provider.

## IBM Cloud Platform Limitations

### Networking Constraints

#### Single Zone per NodeClass
- **Limitation**: Each IBMNodeClass can only specify one zone
- **Impact**: Cannot auto-balance across multiple zones in single NodeClass
- **Workaround**: Create multiple NodeClasses for different zones

```yaml
# Required: Separate NodeClass per zone
---
apiVersion: karpenter.ibm.sh/v1alpha1
kind: IBMNodeClass
metadata:
  name: nodeclass-us-south-1
spec:
  zone: us-south-1
---
apiVersion: karpenter.ibm.sh/v1alpha1
kind: IBMNodeClass
metadata:
  name: nodeclass-us-south-2
spec:
  zone: us-south-2
```

#### No Multi-Zone Auto-Placement
- **Status**: Not implemented
- **Impact**: Manual zone specification required

### Storage Limitations

#### Limited Storage Integration
- **Current**: Basic boot volume support only
- **Missing**:
  - Dynamic storage provisioning during node creation
  - Multiple storage attachments
  - Custom storage profiles
- **Workaround**: Configure storage post-provisioning via storage classes

#### No Instance Store Support
- **Status**: Not implemented
- **Impact**: Cannot use local NVMe storage
- **Alternative**: Use IBM Cloud Block Storage

## Provider-Specific Limitations

### Bootstrap Mode Limitations

#### IKS Mode Instance Type Constraints {#iks-mode-instance-type-constraints}
- **Impact**: When using IKS mode (when `iksClusterID` is specified or `bootstrapMode: "iks-api"`), the provisioner cannot dynamically select instance types based on pod requirements
- **Root Cause**: IKS Worker Pool Resize API (`PATCH /v1/clusters/{id}/workerpools/{poolId}`) adds nodes with instance types pre-configured in the worker pool
- **Current Behavior**:
  - `instanceProfile` and `instanceRequirements` fields in IBMNodeClass are ignored in IKS mode
  - All new nodes use the instance type configured in the existing worker pool
- **Workarounds**:
  - Pre-create separate worker pools for different instance types
  - Use multiple NodeClasses targeting different worker pools

```yaml
# Example: Multiple NodeClasses for different instance types in IKS mode
---
apiVersion: karpenter.ibm.sh/v1alpha1
kind: IBMNodeClass
metadata:
  name: small-instances
spec:
  iksClusterID: "cluster-id"
  iksWorkerPoolID: "worker-pool-small"  # Pre-configured with small instances
---
apiVersion: karpenter.ibm.sh/v1alpha1
kind: IBMNodeClass
metadata:
  name: large-instances
spec:
  iksClusterID: "cluster-id"
  iksWorkerPoolID: "worker-pool-large"  # Pre-configured with large instances
```

### Tagging and Metadata

#### Basic Tagging Support
- **Current**: Limited tag management
- **Missing**:
  - Tag propagation from NodePool to instances
  - Dynamic tag updates
  - Cost allocation tags

#### No Interruption Detection
- **Impact**: Cannot preemptively handle instance interruptions

### Networking Features

#### No Load Balancer Integration
- **Missing**: Automatic load balancer target registration
- **Impact**: Manual load balancer configuration required
- **Workaround**: Use Kubernetes ingress controllers

#### Limited Security Group Management
- **Current**: Uses default or specified security groups
- **Missing**: Dynamic security group creation and management
- **Workaround**: Pre-create security groups with required rules

## Integration Limitations

## Getting Help with Limitations

### Report New Limitations
If you encounter limitations not documented here:

1. **Check existing issues**: [GitHub Issues](https://github.com/pfeifferj/karpenter-provider-ibm-cloud/issues)
2. **Create new issue**
3. **Provide context**

### Request Feature Priority
To prioritize development of specific features:

1. **Upvote existing issues**: Show demand for features
2. **Comment with use case**: Explain business impact
3. **Contribute**: Submit PRs for high-priority features

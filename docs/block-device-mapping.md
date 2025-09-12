# Block Device Mapping

Block Device Mapping allows you to customize the storage configuration for instances provisioned by Karpenter. This feature provides fine-grained control over boot volumes and additional data volumes, similar to AWS Karpenter's block device mapping functionality.

## Overview

By default, Karpenter provisions instances with a 100GB general-purpose boot volume. With Block Device Mapping, you can:

- **Customize boot volume size and performance** (capacity, IOPS, bandwidth)
- **Add additional data volumes** with specific configurations
- **Configure encryption** using IBM Key Protect or Hyper Protect Crypto Services
- **Control volume lifecycle** (delete on termination or persist)
- **Apply custom tags** for organization and billing

## Configuration

Block device mappings are configured in the `IBMNodeClass` specification using the `blockDeviceMappings` field:

```yaml
apiVersion: karpenter.ibm.sh/v1alpha1
kind: IBMNodeClass
metadata:
  name: custom-storage
spec:
  # ... other IBMNodeClass fields ...
  blockDeviceMappings:
    - rootVolume: true
      volumeSpec:
        capacity: 200
        profile: "10iops-tier"
    - deviceName: "data-storage"
      volumeSpec:
        capacity: 500
        profile: "5iops-tier"
```

### Field Reference

#### `BlockDeviceMapping`

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `deviceName` | `string` | No | Custom name for the volume attachment. Auto-generated if not specified. |
| `volumeSpec` | `VolumeSpec` | No | Volume configuration. Uses defaults for root volumes if not specified. |
| `rootVolume` | `boolean` | No | Indicates if this is the boot/root volume. Only one mapping can have this set to `true`. |

#### `VolumeSpec`

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `capacity` | `int64` | No | Volume size in GB. Range: 1-16000 GB. Boot volumes max: 250 GB. |
| `profile` | `string` | No | Storage profile. Options: `general-purpose`, `5iops-tier`, `10iops-tier`, `custom`. |
| `iops` | `int64` | No | I/O operations per second. Only valid for `custom` profile. Range: 100-64000. |
| `bandwidth` | `int64` | No | Maximum bandwidth in Mbps. Range: 1-16000. Auto-calculated if not specified. |
| `encryptionKeyID` | `string` | No | CRN of customer root key for encryption. Uses provider-managed encryption if not specified. |
| `deleteOnTermination` | `boolean` | No | Whether to delete volume when instance terminates. Default: `true`. |
| `tags` | `[]string` | No | User tags to apply to the volume. Maximum: 10 tags. |

## IBM Cloud Storage Profiles

IBM Cloud VPC offers different storage profiles optimized for various workloads:

### Standard Profiles

| Profile | Description | IOPS/GB | Max IOPS | Use Case |
|---------|-------------|---------|----------|----------|
| `general-purpose` | Balanced performance and cost | 3 | 16,000 | General workloads |
| `5iops-tier` | Higher performance | 5 | 48,000 | I/O intensive applications |
| `10iops-tier` | High performance | 10 | 48,000 | Database workloads |

### Custom Profile

| Profile | Description | IOPS Range | Bandwidth Range |
|---------|-------------|------------|-----------------|
| `custom` | User-defined performance | 100-64,000 | Varies by capacity and IOPS |

## Examples

### Basic Custom Boot Volume

```yaml
apiVersion: karpenter.ibm.sh/v1alpha1
kind: IBMNodeClass
metadata:
  name: large-boot-volume
spec:
  region: us-south
  instanceProfile: bx2-4x16
  image: ubuntu-24-04-amd64
  vpc: r010-12345678-1234-5678-9abc-def012345678
  subnet: 0717-197e06f4-b500-426c-bc0f-900b215f996c
  securityGroups:
    - r010-87654321-4321-8765-cba9-876543210fed

  blockDeviceMappings:
    - rootVolume: true
      volumeSpec:
        capacity: 200                    # 200GB boot volume
        profile: "10iops-tier"          # High performance
        deleteOnTermination: true       # Clean up on termination
```

### Multiple Data Volumes

```yaml
blockDeviceMappings:
  # Custom boot volume
  - rootVolume: true
    volumeSpec:
      capacity: 100
      profile: "general-purpose"

  # Application storage
  - deviceName: "app-data"
    volumeSpec:
      capacity: 500
      profile: "5iops-tier"
      deleteOnTermination: false      # Persist data
      tags:
        - "application-storage"

  # Database storage with custom IOPS
  - deviceName: "database"
    volumeSpec:
      capacity: 1000
      profile: "custom"
      iops: 8000                      # Custom IOPS
      bandwidth: 400                  # Custom bandwidth
      deleteOnTermination: false
      encryptionKeyID: "crn:v1:bluemix:public:kms:..."
      tags:
        - "database"
        - "high-performance"
```

## Default Behavior

When `blockDeviceMappings` is not specified or empty:

- **Boot Volume**: 100GB general-purpose storage
- **Delete on Termination**: `true`
- **Profile**: `general-purpose`
- **Encryption**: Provider-managed
- **Data Volumes**: None

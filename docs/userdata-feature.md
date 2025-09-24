# UserData and UserDataAppend Feature Documentation

## Overview

The Karpenter IBM Cloud Provider supports two methods for customizing node initialization:

1. **`userData`** - Complete override of the bootstrap script
2. **`userDataAppend`** - Append custom commands to the standard bootstrap script

## Usage Examples

### Example 1: Using UserDataAppend

```yaml
apiVersion: karpenter.ibm.sh/v1alpha1
kind: IBMNodeClass
metadata:
  name: example-append
spec:
  region: us-south
  vpc: r010-12345678-1234-5678-9abc-def012345678
  image: ubuntu-24-04-amd64
  securityGroups:
    - r010-87654321-4321-8765-9abc-def098765432

  # Append custom commands after bootstrap
  userDataAppend: |
    echo "Running post-bootstrap configuration..."
    apt-get update && apt-get install -y monitoring-tools
    systemctl enable node-exporter
    echo "Configuration complete"
```

### Example 2: Using UserData (Complete Override)

```yaml
apiVersion: karpenter.ibm.sh/v1alpha1
kind: IBMNodeClass
metadata:
  name: example-override
spec:
  region: us-south
  vpc: r010-12345678-1234-5678-9abc-def012345678
  image: ubuntu-24-04-amd64
  securityGroups:
    - r010-87654321-4321-8765-9abc-def098765432

  # Complete custom bootstrap script
  userData: |
    #!/bin/bash
    set -euo pipefail

    echo "Custom bootstrap starting..."
    # You are responsible for:
    # - Installing container runtime
    # - Installing kubelet
    # - Configuring CNI
    # - Joining the cluster

  # Note: userDataAppend is ignored when userData is set
  userDataAppend: |
    echo "This will be ignored"
```

## Precedence Rules

1. If `userData` is set:
   - Use `userData` as the complete script
   - Ignore `userDataAppend`
   - Skip all Karpenter bootstrap logic

2. If only `userDataAppend` is set:
   - Generate standard Karpenter bootstrap script
   - Append custom commands at the end

3. If neither is set:
   - Use standard Karpenter bootstrap script only

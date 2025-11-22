# UserData and UserDataAppend Feature Documentation

## Overview

The Karpenter IBM Cloud Provider supports multiple methods for customizing node initialization:

1. **`userData`** - Complete override of the bootstrap script
2. **`userDataAppend`** - Append custom commands to the standard bootstrap script
3. **`BOOTSTRAP_*` Environment Variables** - Inject credentials and configuration from Secrets

## Usage Examples

### Example 1: Using UserDataAppend

```yaml
apiVersion: karpenter-ibm.sh/v1alpha1
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
apiVersion: karpenter-ibm.sh/v1alpha1
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

## BOOTSTRAP_* Environment Variable Injection

### Overview

The Karpenter provider automatically injects environment variables prefixed with `BOOTSTRAP_*` from the operator deployment into userData scripts. This enables secure credential passing from Kubernetes Secrets without exposing sensitive data in IBMNodeClass CRDs.

### Use Cases

- **RKE2/K3s Bootstrapping**: Pass server URLs and join tokens
- **Custom Authentication**: Inject API keys and certificates
- **Configuration Management**: Pass cluster-specific settings
- **Secret Management**: Reference values from Kubernetes Secrets

### How It Works

```
Karpenter Deployment → BOOTSTRAP_* env vars (with Secret refs)
         ↓
Cloud-init generation → Inject into userData
         ↓
Rendered script → IBM Cloud VPC API
         ↓
Instance boots → Variables available in script
```

### Configuration Example

#### Step 1: Create a Secret

```bash
kubectl create secret generic rke2-join-config \
  --from-literal=server=https://10.20.5.71:9345 \
  --from-literal=token=K1060b31293... \
  -n karpenter
```

#### Step 2: Configure Karpenter Deployment

```yaml
# In Karpenter Helm values or deployment
env:
  # Literal value
  - name: BOOTSTRAP_RKE2_SERVER
    value: "https://10.20.5.71:9345"

  # From Secret
  - name: BOOTSTRAP_RKE2_TOKEN
    valueFrom:
      secretKeyRef:
        name: rke2-join-config
        namespace: karpenter
        key: token

  # Optional version
  - name: BOOTSTRAP_RKE2_VERSION
    value: "v1.30.2+rke2r1"
```

#### Step 3: Use in UserData

```yaml
apiVersion: karpenter-ibm.sh/v1alpha1
kind: IBMNodeClass
metadata:
  name: rke2-nodeclass
spec:
  region: us-south
  vpc: r010-xxx
  securityGroups: [r010-xxx]

  userData: |
    #!/bin/bash
    set -euo pipefail

    # BOOTSTRAP_* variables are automatically injected
    echo "Joining RKE2 cluster at: $BOOTSTRAP_RKE2_SERVER"

    # Install RKE2
    curl -sfL https://get.rke2.io | \
        INSTALL_RKE2_VERSION="${BOOTSTRAP_RKE2_VERSION}" \
        INSTALL_RKE2_TYPE="agent" \
        sh -

    # Configure with injected credentials
    mkdir -p /etc/rancher/rke2
    cat > /etc/rancher/rke2/config.yaml <<EOF
    server: ${BOOTSTRAP_RKE2_SERVER}
    token: ${BOOTSTRAP_RKE2_TOKEN}
    EOF

    systemctl enable --now rke2-agent
```

### Complete Example: RKE2 Integration

See `examples/rke2-userdata.sh` for a complete working example.

## Precedence Rules

1. If `userData` is set:
   - Use `userData` as the complete script
   - `BOOTSTRAP_*` variables are still injected
   - Ignore `userDataAppend`
   - Skip all Karpenter bootstrap logic

2. If only `userDataAppend` is set:
   - Generate standard Karpenter bootstrap script
   - `BOOTSTRAP_*` variables are injected
   - Append custom commands at the end

3. If neither is set:
   - Use standard Karpenter bootstrap script only
   - `BOOTSTRAP_*` variables are injected if defined

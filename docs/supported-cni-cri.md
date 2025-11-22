# Supported CNI and CRI

This document outlines the supported Container Network Interface (CNI) plugins and Container Runtime Interface (CRI) implementations with the Karpenter IBM Cloud Provider.

## Container Network Interface (CNI) Support

### Tested and Supported CNI Plugins

| CNI Plugin | Status | Bootstrap Mode | Notes |
|------------|--------|----------------|-------|
| **Calico** | ✅ Fully Supported | cloud-init, iks-api | Default for most IBM Cloud deployments |
| **Cilium** | ✅ Fully Supported | cloud-init | Advanced networking features supported |
| **Flannel** | ✅ Basic Support | cloud-init | Simple overlay networking |

### CNI Auto-Detection

The Karpenter IBM Cloud Provider includes **automatic CNI detection** in cloud-init bootstrap mode:

```yaml
apiVersion: karpenter-ibm.sh/v1alpha1
kind: IBMNodeClass
spec:
  bootstrapMode: cloud-init  # Enables auto-detection
  # CNI will be automatically detected and configured
```

**Detection Process:**
1. Scans cluster for CNI-specific DaemonSets and ConfigMaps
2. Identifies CNI plugin from `kube-system` namespace resources
3. Configures node networking accordingly
4. Falls back to generic CNI configuration if detection fails

### CNI-Specific Configuration

#### Calico Configuration
- **Detection**: Looks for `calico-node` DaemonSet
- **Configuration**: Automatically configures IPPool and network policies
- **Features**: Full support for network policies and BGP routing

#### Cilium Configuration
- **Detection**: Looks for `cilium` DaemonSet and ConfigMap
- **Configuration**: Configures eBPF datapath and cluster mesh
- **Features**: Advanced security policies and observability

#### Flannel Configuration
- **Detection**: Looks for `kube-flannel` DaemonSet
- **Configuration**: Sets up VXLAN overlay networking
- **Features**: Basic pod-to-pod communication

## Container Runtime Interface (CRI) Support

### Supported Container Runtimes

| Runtime        | Status             | Detection Method     | Default Version     |
|----------------|--------------------|----------------------|---------------------|
| **containerd** | ✅ Fully Supported | Auto-detected        | 1.7.0+              |
| **CRI-O**      | 🔄 Basic Support   | Manual configuration | 1.24+               |

### Runtime Auto-Detection

**Automatic Detection Process:**
1. Queries existing cluster nodes for runtime information
2. Reads `node.status.nodeInfo.containerRuntimeVersion`
3. Configures new nodes with the detected runtime
4. Falls back to containerd if detection fails

**Manual Runtime Configuration:**
```bash
# Set via environment variable
export CONTAINER_RUNTIME=containerd

# Or configure in bootstrap script
echo "CONTAINER_RUNTIME=containerd" >> /etc/environment
```

## Custom Configuration

### Advanced CNI Configuration
```yaml
apiVersion: karpenter-ibm.sh/v1alpha1
kind: IBMNodeClass
spec:
  bootstrapMode: cloud-init
  userData: |
    #!/bin/bash
    # Custom CNI configuration
    curl -L -o /opt/cni/bin/my-cni https://releases.example.com/cni
    chmod +x /opt/cni/bin/my-cni

    # Custom CNI config
    cat > /etc/cni/net.d/10-mycni.conf <<EOF
    {
      "cniVersion": "0.4.0",
      "name": "mycni",
      "type": "my-cni"
    }
    EOF
```

### Runtime Configuration Override
```yaml
spec:
  userData: |
    #!/bin/bash
    # Force specific container runtime
    export CONTAINER_RUNTIME=cri-o

    # Install CRI-O
    curl -L -o /tmp/crio.tar.gz https://releases.cri-o.io/...
    tar -xzf /tmp/crio.tar.gz -C /
    systemctl enable --now crio
```

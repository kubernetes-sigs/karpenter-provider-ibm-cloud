# Container Images

## Overview

The Karpenter Provider for IBM Cloud publishes multi-architecture container images for production use and nightly builds for testing.

## Production Images

Production images are published to Quay.io on every release:

```
quay.io/karpenter-provider-ibm-cloud/controller
```

### Supported Architectures

| Architecture | Platform | Description |
|-------------|----------|-------------|
| AMD64 | linux/amd64 | Standard x86-64 systems |
| ARM64 | linux/arm64 | ARM-based systems |
| IBM Z | linux/s390x | IBM Z and LinuxONE systems |

All release tags are multi-arch manifests that automatically pull the correct architecture for your platform.

### Image Tags

| Tag | Description |
|-----|-------------|
| `latest` | Latest build from main branch |
| `vX.Y.Z` | Specific release version (e.g., `v0.4.0`) |
| `nightly` | Latest nightly build (see [Nightly Builds](nightly-builds.md)) |

### Pulling Images

```bash
# Pull latest release
docker pull quay.io/karpenter-provider-ibm-cloud/controller:latest

# Pull specific version
docker pull quay.io/karpenter-provider-ibm-cloud/controller:v0.4.0

# Pull for specific architecture
docker pull --platform linux/s390x quay.io/karpenter-provider-ibm-cloud/controller:latest
```

### Using with Helm

The Helm chart uses the Quay.io image by default:

```bash
helm install karpenter karpenter-ibm/karpenter-ibm \
  --namespace karpenter \
  --create-namespace \
  --set image.tag=v0.4.0
```

To override the image:

```bash
helm install karpenter karpenter-ibm/karpenter-ibm \
  --namespace karpenter \
  --set image.repository=quay.io/karpenter-provider-ibm-cloud/controller \
  --set image.tag=v0.4.0
```

## Nightly Builds

For testing unreleased features, see [Nightly Builds](nightly-builds.md).

## Image Verification

All production images include:

- **SBOM (Software Bill of Materials)**: Published alongside each image
- **Provenance attestations**: Build provenance for supply chain security

To inspect the SBOM:

```bash
docker buildx imagetools inspect quay.io/karpenter-provider-ibm-cloud/controller:latest --format '{{json .SBOM}}'
```

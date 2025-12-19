# Nightly Builds

## Overview

Nightly builds are automatically created every day at 2 AM UTC from the latest commit on the main branch. These builds allow users to test the latest features and fixes before they are included in an official release.

## Image Tags

Nightly builds are published with two tags:

1. **Versioned nightly tag**: `v<latest-tag>-<date>-<commit-sha>-nightly`
   - Example: `v0.5.0-2024-01-15-a1b2c3d-nightly`

2. **Latest nightly tag**: `nightly`
   - Always points to the most recent nightly build

## Using Nightly Builds

### Pull the latest nightly build

```bash
podman pull ghcr.io/kubernetes-sigs/karpenter-provider-ibm-cloud/controller:nightly
```

### Pull a specific nightly build

```bash
podman pull ghcr.io/kubernetes-sigs/karpenter-provider-ibm-cloud/controller:v0.5.0-2024-01-15-a1b2c3d-nightly
```

### Deploy with Helm using nightly builds

```bash
helm upgrade --install karpenter-ibm oci://ghcr.io/kubernetes-sigs/karpenter-provider-ibm-cloud/charts/karpenter-provider-ibm-cloud \
  --set controller.image.tag=nightly \
  --namespace karpenter \
  --create-namespace
```

## Build Schedule

- **Daily builds**: Every day at 2 AM UTC
- **Manual builds**: Can be triggered manually through GitHub Actions
- **Retention**: Nightly builds older than 7 days are automatically cleaned up

## Stability Notice

**Important**: Nightly builds are created from the latest code on the main branch and may contain:

- Unreleased features
- Breaking changes
- Bugs that haven't been discovered yet

Nightly builds are intended for:

- Testing new features early
- Development and staging environments
- Contributing to the project

**Do not use nightly builds in production environments.**

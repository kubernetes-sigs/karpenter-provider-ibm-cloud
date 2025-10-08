# Image Selection in IBM Cloud Karpenter Provider

The IBM Cloud Karpenter Provider offers two methods for specifying node images: explicit image specification and semantic image selection. This document explains both approaches and when to use each.

## Overview

Traditionally, specifying node images required knowing exact image IDs or names, which frequently change as IBM Cloud releases new images. The **ImageSelector** feature allows you to specify image requirements semantically, and the system automatically selects the most recent image matching your criteria.

## Image Specification Methods

### Method 1: Explicit Image Specification (Traditional)

Use this method when you need to use a specific image ID or name:

```yaml
apiVersion: karpenter-ibm.sh/v1alpha1
kind: IBMNodeClass
metadata:
  name: explicit-image-example
spec:
  image: "r006-72b27b5c-f4b0-48bb-b954-5becc7c1dcb8"  # Specific image ID
  # OR
  # image: "ibm-ubuntu-22-04-minimal-amd64-3"          # Specific image name

  region: "us-south"
  vpc: "r010-12345678-1234-5678-9abc-def012345678"
  securityGroups:
    - "r010-87654321-4321-4321-4321-210987654321"
```

**Pros:**
- Predictable and deterministic
- Guaranteed image availability (if it exists)
- Full control over exact image version

**Cons:**
- Requires manual updates when images are deprecated
- Risk of provisioning failures if image becomes unavailable
- Need to track IBM Cloud image lifecycle

### Method 2: Semantic Image Selection (New)

Use this method for automatic selection of the latest available image matching your requirements:

```yaml
apiVersion: karpenter-ibm.sh/v1alpha1
kind: IBMNodeClass
metadata:
  name: semantic-image-example
spec:
  imageSelector:
    os: "ubuntu"              # Required: Operating system
    majorVersion: "22"        # Required: Major version
    minorVersion: "04"        # Optional: Minor version (omit for latest)
    architecture: "amd64"     # Optional: CPU architecture (default: amd64)
    variant: "minimal"        # Optional: Image variant (minimal, server, etc.)

  region: "us-south"
  vpc: "r010-12345678-1234-5678-9abc-def012345678"
  securityGroups:
    - "r010-87654321-4321-4321-4321-210987654321"
```

**Pros:**
- Automatically uses latest available images
- Reduces maintenance overhead
- Resilient to image deprecation
- Future-proof configuration

**Cons:**
- Less predictable (image may change between deployments)
- Requires understanding of IBM Cloud naming conventions

## ImageSelector Fields

### Required Fields

- **`os`**: Operating system name
  - Common values: `"ubuntu"`, `"rhel"`, `"centos"`, `"rocky"`, `"fedora"`, `"debian"`, `"suse"`
  - Must be lowercase letters only

- **`majorVersion`**: Major version number
  - Examples: `"20"` for Ubuntu 20.x, `"8"` for RHEL 8.x, `"22"` for Ubuntu 22.x
  - Must be numeric only

### Optional Fields

- **`minorVersion`**: Minor version number
  - Examples: `"04"` for Ubuntu x.04, `"10"` for Ubuntu x.10
  - If omitted, the latest available minor version is selected
  - Must be numeric only

- **`architecture`**: CPU architecture
  - Valid values: `"amd64"`, `"arm64"`, `"s390x"`
  - Default: `"amd64"`

- **`variant`**: Image variant or flavor
  - Common values: `"minimal"`, `"server"`, `"cloud"`, `"base"`
  - If omitted, any variant is considered with preference for `"minimal"`
  - Must be lowercase letters only

## Image Selection Logic

When using `imageSelector`, the system:

1. **Filters** all available images by the specified criteria
2. **Sorts** matching images by:
   - Minor version (highest first, if not specified in selector)
   - Build number (highest first)
   - Creation date (newest first)
3. **Selects** the first (most recent) image from the sorted list

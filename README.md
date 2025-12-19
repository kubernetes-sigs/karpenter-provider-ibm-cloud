# Karpenter Provider for IBM Cloud

[![Go Tests](https://github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/actions/workflows/go-test.yaml/badge.svg)](https://github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/actions/workflows/go-test.yaml)
[![codecov](https://codecov.io/github/kubernetes-sigs/karpenter-provider-ibm-cloud/graph/badge.svg?token=VF3SOM6IMR)](https://codecov.io/github/kubernetes-sigs/karpenter-provider-ibm-cloud)
[![Go Report Card](https://goreportcard.com/badge/github.com/kubernetes-sigs/karpenter-provider-ibm-cloud)](https://goreportcard.com/report/github.com/kubernetes-sigs/karpenter-provider-ibm-cloud)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![GitHub release (latest by date)](https://img.shields.io/github/v/release/kubernetes-sigs/karpenter-provider-ibm-cloud)](https://github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/releases)
[![Documentation](https://img.shields.io/badge/docs-latest-blue.svg)](https://karpenter-ibm.sh/)

This repository contains the Karpenter Provider implementation for IBM Cloud, enabling dynamic node provisioning in IBM Cloud Kubernetes clusters using Karpenter.

**[View Full Documentation](https://karpenter-ibm.sh/)** | **[Getting Started Guide](https://karpenter-ibm.sh/getting-started/)**

## Overview

Karpenter is an open-source node provisioning project for Kubernetes. This provider extends Karpenter's functionality to work with IBM Cloud, allowing for:

- Dynamic node provisioning in IBM Cloud VPC
- Instance type management and selection
- Automated scaling based on workload demands
- Integration with IBM Cloud APIs


## Kubernetes Support

| Kubernetes Version | Status |
|-------------------|--------|
| 1.29 | ✅ Supported |
| 1.30 | ✅ Supported |
| 1.31 | ✅ Supported |
| 1.32 | ✅ Supported |
| 1.33 | ✅ Supported |
| 1.34 | ✅ Supported |
| 1.35 | ✅ Supported |
| 1.36+ | ⚠️ Untested |

*Based on dependency analysis. Generated on 2025-11-30.*

## Container Images

Multi-architecture images (amd64, arm64, s390x) are published to `quay.io/karpenter-provider-ibm-cloud/controller`.

See [Container Images](docs/container-images.md) for details on pulling images, supported architectures, and [Nightly Builds](docs/nightly-builds.md) for testing pre-release versions.

## Development

### Testing and CI

The project includes automated testing and continuous integration workflows:

#### Helm Chart Testing

Tests run automatically on:

- All pull requests (validates changes before merge)
- Manual trigger via GitHub Actions UI

The tests perform:

- Chart linting for syntax and best practices
- Template rendering validation
- Kubernetes manifest validation
- Custom Resource Definition (CRD) verification

#### Chart Publishing

After changes pass tests and are merged to main:

- The chart is automatically packaged
- The Helm repository index is updated
- Changes are published to GitHub Pages

These CI workflows ensure chart quality through pre-merge validation and maintain the Helm repository for easy installation.

## Subprojects and Additional Repositories

This project does not currently produce, maintain, or release any additional
subproject codebases or separate repositories.

**Status:** Not applicable
**Intent:** The project is intentionally maintained as a single repository.
If additional subprojects or repositories are introduced in the future, they
will be documented in this section.

## Getting Help

### Community Support

- **[Join our Slack](https://cloud-native.slack.com/archives/C094SDPCVLN)** - Get help from the community and maintainers
- **[Report Issues](https://github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/issues)** - Found a bug or have a feature request?
- **[Documentation](https://karpenter-ibm.sh/)** - Complete guides and API reference

## Contributing

Contributions are welcome! Please read our [Contributing Guidelines](CONTRIBUTING.md) for details on how to submit pull requests.

## License

This project is licensed under the [Apache 2](LICENSE) License.

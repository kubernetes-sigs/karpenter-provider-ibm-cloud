# Development

## Git Hooks

This project uses [pre-commit](https://pre-commit.com/) for linting and formatting checks. Hooks are **automatically installed** when you run `make ci`.

## Testing and CI

The project includes automated testing and continuous integration workflows:

### Helm Chart Testing

Tests run automatically on:

- All pull requests (validates changes before merge)
- Manual trigger via GitHub Actions UI

The tests perform:

- Chart linting for syntax and best practices
- Template rendering validation
- Kubernetes manifest validation
- Custom Resource Definition (CRD) verification

### Chart Publishing

After changes pass tests and are merged to main:

- The chart is automatically packaged
- The Helm repository index is updated
- Changes are published to GitHub Pages

These CI workflows ensure chart quality through pre-merge validation and maintain the Helm repository for easy installation.

## Release Process

[Governance document](https://github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/blob/main/RELEASE.md)

### During Development

- Keep roadmap up to date
- Assign milestones to issues

### Preparing the release

- Don't bump dependencies until release with the exception of security relevant fixes
- Bump tags in helm chart
- Create release tag + changelog/release notes
- CI builds tagged image
- CI publishes docs
- Close milestone on gh

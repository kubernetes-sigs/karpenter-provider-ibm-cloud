# Documentation

This directory contains the documentation for the Karpenter IBM Cloud Provider, built with [MkDocs](https://www.mkdocs.org/) and the [Material theme](https://squidfunk.github.io/mkdocs-material/).

## Local Development

### Prerequisites

- Python 3.8+
- pip

### Setup

1. Install dependencies:
```bash
pip install -r docs/requirements.txt
```

2. Serve documentation locally:
```bash
mkdocs serve
```

3. View at http://localhost:8000

### Building

Build static site:
```bash
mkdocs build
```

Output will be in `site/` directory.

## Deployment

Documentation is automatically deployed to GitHub Pages when changes are pushed to the `main` branch. The deployment workflow:

1. Builds documentation with MkDocs
2. Deploys to GitHub Pages alongside Helm charts
3. Available at https://pfeifferj.github.io/karpenter-provider-ibm-cloud/

## Writing Documentation

### Style Guide

- Use clear, concise language
- Include code examples for all configurations
- Use admonitions for important notes:
  - `!!! note` for general information
  - `!!! warning` for important warnings
  - `!!! danger` for critical warnings
  - `!!! success` for success messages
  - `!!! info` for informational content

### Adding Pages

1. Create new `.md` file in `docs/`
2. Add to navigation in `mkdocs.yml`
3. Follow existing page structure

### Code Examples

Use fenced code blocks with language hints:

````markdown
```yaml
apiVersion: karpenter-ibm.sh/v1alpha1
kind: IBMNodeClass
...
```
````

### Diagrams

Use Mermaid for diagrams:

````markdown
```mermaid
graph LR
    A[Start] --> B[Process]
    B --> C[End]
```
````

## Contributing

1. Fork the repository
2. Create a feature branch
3. Make documentation changes
4. Test locally with `mkdocs serve`
5. Submit pull request

## Resources

- [MkDocs Documentation](https://www.mkdocs.org/)
- [Material for MkDocs](https://squidfunk.github.io/mkdocs-material/)
- [Markdown Guide](https://www.markdownguide.org/)

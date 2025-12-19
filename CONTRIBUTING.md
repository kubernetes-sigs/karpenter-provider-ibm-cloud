# Contributing Guidelines

Welcome to Kubernetes. We are excited about the prospect of you joining our [community](https://git.k8s.io/community)! The Kubernetes community abides by the CNCF [code of conduct](code-of-conduct.md). Here is an excerpt:

_As contributors and maintainers of this project, and in the interest of fostering an open and welcoming community, we pledge to respect all people who contribute through reporting issues, posting feature requests, updating documentation, submitting pull requests or patches, and other activities._

## Getting Started

We have full documentation on how to get started contributing here:

<!---
If your repo has certain guidelines for contribution, put them here ahead of the general k8s resources
-->

- [Contributor License Agreement](https://git.k8s.io/community/CLA.md) - Kubernetes projects require that you sign a Contributor License Agreement (CLA) before we can accept your pull requests
- [Kubernetes Contributor Guide](https://k8s.dev/guide) - Main contributor documentation, or you can just jump directly to the [contributing page](https://k8s.dev/docs/guide/contributing/)
- [Contributor Cheat Sheet](https://k8s.dev/cheatsheet) - Common resources for existing developers


## Commit Message Guidelines

We follow the [Conventional Commits](https://www.conventionalcommits.org/) specification to maintain a clean and readable git history. All commit messages are automatically validated using [gitlint](https://github.com/jorisroovers/gitlint).

### Commit Message Format

```
<type>[optional scope]: <description>

[optional body]

[optional footer(s)]
```

### Types

- **feat**: A new feature
- **fix**: A bug fix
- **docs**: Documentation only changes
- **style**: Changes that do not affect the meaning of the code (white-space, formatting, missing semi-colons, etc)
- **refactor**: A code change that neither fixes a bug nor adds a feature
- **perf**: A code change that improves performance
- **test**: Adding missing tests or correcting existing tests
- **build**: Changes that affect the build system or external dependencies (example scopes: gulp, broccoli, npm)
- **ci**: Changes to our CI configuration files and scripts (example scopes: Travis, Circle, BrowserStack, SauceLabs)
- **chore**: Other changes that don't modify src or test files
- **revert**: Reverts a previous commit

### Scope (Optional)

The scope should be the name of the component affected:

- **nodepool**: Changes related to node pool management
- **instance**: Changes related to instance management
- **provider**: Changes related to the cloud provider interface
- **controller**: Changes related to Kubernetes controllers
- **errors**: Changes related to error handling
- **config**: Changes related to configuration
- **docs**: Changes related to documentation
- **deps**: Changes related to dependencies

### Examples

#### Good Examples ✅

```
feat(nodepool): add support for spot instances
fix(errors): replace HTTP status literals with constants
docs: update installation instructions
test(provider): add unit tests for instance creation
chore(deps): update IBM SDK to v1.2.3
refactor(controller): simplify nodepool reconciliation logic
perf(instance): optimize instance discovery caching
ci: add gitlint validation to PR workflow
style: fix code formatting in errors package
build: update Go version to 1.21
```

#### Bad Examples ❌

```
Fixed stuff                           # Too vague, no type
Add feature                          # Too vague, no scope context
WIP: working on nodepool             # Contains WIP
feat: added new feature.             # Ends with period
FEAT(nodepool): Add spot support     # Wrong case
fix(nodepool):add spot instances     # Missing space after colon
feat(nodepool): Added spot instances # Past tense instead of imperative
```

### Detailed Rules

1. **Use the imperative mood** in the subject line:
   - ✅ "Add feature" not "Added feature"
   - ✅ "Fix bug" not "Fixed bug"

2. **Limit the subject line to 72 characters**

3. **Do not end the subject line with a period**

4. **Use lowercase for type and scope**

5. **Separate subject from body with a blank line**

6. **Use the body to explain what and why vs. how**

7. **Wrap the body at 80 characters**

8. **Reference issues and pull requests when applicable**

### Body and Footer Examples

```
feat(nodepool): add support for spot instances

Add the ability to create spot instances for cost optimization.
This includes new configuration options and validation logic.

Closes #123
```

```
fix(provider): resolve memory leak in instance polling

The instance polling loop was not properly cleaning up goroutines,
causing memory usage to grow over time. This fix ensures proper
cleanup when the context is cancelled.

Fixes #456
Related: #457
```

## Mentorship

- [Mentoring Initiatives](https://k8s.dev/community/mentoring) - We have a diverse set of mentorship programs available that are always looking for volunteers!

<!---
Custom Information - if you're copying this template for the first time you can add custom content here, for example:

## Contact Information

- [Slack channel](https://kubernetes.slack.com/messages/kubernetes-users) - Replace `kubernetes-users` with your slack channel string, this will send users directly to your channel.
- [Mailing list](URL)
-->

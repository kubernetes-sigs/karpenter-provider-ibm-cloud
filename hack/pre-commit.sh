#!/bin/bash
set -eu -o pipefail

# Pre-commit hook to verify license headers and run linting
echo "Running pre-commit checks..."

# Check license headers
echo "Checking license headers..."
if ! make verify-license; then
    echo "❌ License header check failed!"
    echo "Run 'make license' to add missing license headers."
    exit 1
fi

# Run linter
echo "Running linter..."
if ! golangci-lint run; then
    echo "❌ Linting failed!"
    exit 1
fi

echo "✅ All pre-commit checks passed!"

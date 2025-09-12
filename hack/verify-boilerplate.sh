#!/bin/bash
set -eu -o pipefail

# Check that all Go files have Apache License headers
MISSING_FILES=()

for i in $(
  find ./tools ./cmd ./pkg ./test ./hack -name "*.go" 2>/dev/null | grep -v vendor/ | grep -v zz_generated
); do
  if ! grep -q "Apache License" "$i"; then
    MISSING_FILES+=("$i")
  fi
done

if [ ${#MISSING_FILES[@]} -ne 0 ]; then
  echo "ERROR: The following files are missing Apache License headers:"
  for file in "${MISSING_FILES[@]}"; do
    echo "  - $file"
  done
  echo ""
  echo "Run 'make license' to add missing license headers."
  exit 1
fi

echo "All Go files have proper license headers."

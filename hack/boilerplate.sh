#!/bin/bash
set -eu -o pipefail

# Add Apache License headers to all Go files that don't already have them
for i in $(
  find ./tools ./cmd ./pkg ./test ./hack -name "*.go" 2>/dev/null | grep -v vendor/ | grep -v zz_generated
); do
  if ! grep -q "Apache License" "$i"; then
    echo "Adding license header to $i"
    cat hack/boilerplate.go.txt "$i" > "$i.new" && mv "$i.new" "$i"
  fi
done

echo "License header check completed."

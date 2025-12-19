#!/bin/bash

# generate-k8s-support-table.sh - Generate Kubernetes version support table based on dependencies
# Usage: ./generate-k8s-support-table.sh [--format markdown|json|yaml] [--output FILE] [--update-readme]

set -euo pipefail

# Configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
GO_MOD_FILE="$PROJECT_ROOT/go.mod"
DEFAULT_FORMAT="markdown"
OUTPUT_FILE=""
README_FILE="$PROJECT_ROOT/README.md"
UPDATE_README=false

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1" >&2
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1" >&2
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1" >&2
}

show_usage() {
    cat <<EOF
Usage: $0 [OPTIONS]

Generate Kubernetes version support table based on go.mod dependencies.

OPTIONS:
    --format FORMAT     Output format: markdown, json, or yaml (default: markdown)
    --output FILE       Output to file instead of stdout
    --update-readme     Update README.md with Kubernetes support table
    --help             Show this help message

EXAMPLES:
    # Generate markdown table to stdout
    ./generate-k8s-support-table.sh

    # Generate JSON format to file
    ./generate-k8s-support-table.sh --format json --output k8s-support.json

    # Generate YAML format
    ./generate-k8s-support-table.sh --format yaml

    # Update README.md with support table
    ./generate-k8s-support-table.sh --update-readme

The script analyzes:
- k8s.io/* dependencies for Kubernetes API compatibility
- sigs.k8s.io/karpenter for Karpenter version compatibility
- sigs.k8s.io/controller-runtime for controller-runtime compatibility
- Go version requirements
EOF
}

# Parse command line arguments
parse_args() {
    local format="$DEFAULT_FORMAT"

    while [[ $# -gt 0 ]]; do
        case $1 in
            --format)
                format="$2"
                shift 2
                ;;
            --output)
                OUTPUT_FILE="$2"
                shift 2
                ;;
            --update-readme)
                UPDATE_README=true
                shift
                ;;
            --help)
                show_usage
                exit 0
                ;;
            *)
                log_error "Unknown option: $1"
                show_usage
                exit 1
                ;;
        esac
    done

    # Validate format
    case "$format" in
        markdown|json|yaml)
            FORMAT="$format"
            ;;
        *)
            log_error "Invalid format: $format. Must be one of: markdown, json, yaml"
            exit 1
            ;;
    esac
}

# Extract version from go.mod
extract_dependency_version() {
    local dep_name="$1"
    local version=""

    if [[ -f "$GO_MOD_FILE" ]]; then
        # Try to find the dependency in the require section
        version=$(awk "/^require \(/,/^\)/" "$GO_MOD_FILE" | grep -E "^\s*${dep_name}" | awk '{print $2}' | head -1)

        # If not found in require block, try direct require
        if [[ -z "$version" ]]; then
            version=$(grep -E "^require ${dep_name}" "$GO_MOD_FILE" | awk '{print $3}' | head -1)
        fi

        # Clean up version (remove 'v' prefix if present, handle indirect comments)
        version=$(echo "$version" | sed -E 's|^v||' | sed -E 's| //.*||')
    fi

    echo "$version"
}

# Map k8s.io dependency versions to Kubernetes releases
map_k8s_version() {
    local dep_version="$1"

    # Extract major.minor from version (handle beta versions like 0.34.0-beta.0)
    local major_minor
    major_minor=$(echo "$dep_version" | sed -E 's/^([0-9]+\.[0-9]+).*$/\1/')

    case "$major_minor" in
        "0.35") echo "1.35" ;;
        "0.34") echo "1.34" ;;
        "0.33") echo "1.33" ;;
        "0.32") echo "1.32" ;;
        "0.31") echo "1.31" ;;
        "0.30") echo "1.30" ;;
        "0.29") echo "1.29" ;;
        "0.28") echo "1.28" ;;
        "0.27") echo "1.27" ;;
        "0.26") echo "1.26" ;;
        "0.25") echo "1.25" ;;
        *) echo "Unknown (${dep_version})" ;;
    esac
}

# Get Karpenter compatibility info
get_karpenter_k8s_compatibility() {
    local karpenter_version="$1"

    # Karpenter version to Kubernetes compatibility mapping
    # Based on Karpenter release notes and compatibility matrix
    case "$karpenter_version" in
        1.8.*) echo "1.29 - 1.35" ;;
        1.7.*) echo "1.29 - 1.35" ;;
        1.6.*) echo "1.28 - 1.34" ;;
        1.5.*) echo "1.28 - 1.31" ;;
        1.4.*) echo "1.27 - 1.30" ;;
        1.3.*) echo "1.26 - 1.29" ;;
        1.2.*) echo "1.25 - 1.28" ;;
        1.1.*) echo "1.24 - 1.27" ;;
        1.0.*) echo "1.23 - 1.26" ;;
        *) echo "See Karpenter docs" ;;
    esac
}

# Collect dependency information
collect_dependency_info() {
    log_info "Analyzing dependencies in $GO_MOD_FILE"

    # Extract key dependency versions
    local k8s_api_version
    local k8s_client_version
    local k8s_apimachinery_version
    local controller_runtime_version
    local karpenter_version
    local go_version

    k8s_api_version=$(extract_dependency_version "k8s.io/api")
    k8s_client_version=$(extract_dependency_version "k8s.io/client-go")
    k8s_apimachinery_version=$(extract_dependency_version "k8s.io/apimachinery")
    controller_runtime_version=$(extract_dependency_version "sigs.k8s.io/controller-runtime")
    karpenter_version=$(extract_dependency_version "sigs.k8s.io/karpenter")
    go_version=$(grep "^go " "$GO_MOD_FILE" | awk '{print $2}')

    # Map versions to Kubernetes releases
    local k8s_api_k8s
    local k8s_client_k8s
    local k8s_apimachinery_k8s
    local karpenter_k8s_compat

    k8s_api_k8s=$(map_k8s_version "$k8s_api_version")
    k8s_client_k8s=$(map_k8s_version "$k8s_client_version")
    k8s_apimachinery_k8s=$(map_k8s_version "$k8s_apimachinery_version")
    karpenter_k8s_compat=$(get_karpenter_k8s_compatibility "$karpenter_version")

    # Determine overall supported Kubernetes versions dynamically
    # Parse Karpenter compatibility range
    local karpenter_min_k8s
    local karpenter_max_k8s

    # Check if compatibility string contains valid version range
    if [[ "$karpenter_k8s_compat" =~ ^[0-9]+\.[0-9]+.*-.*[0-9]+\.[0-9]+$ ]]; then
        karpenter_min_k8s=$(echo "$karpenter_k8s_compat" | sed -E 's/^([0-9]+\.[0-9]+).*/\1/')
        karpenter_max_k8s=$(echo "$karpenter_k8s_compat" | sed -E 's/.*- ([0-9]+\.[0-9]+)$/\1/')
    else
        # For unknown versions, use fallback based on k8s.io dependencies
        log_warn "Unknown Karpenter version compatibility: $karpenter_k8s_compat"
        karpenter_min_k8s=$(echo "$k8s_api_k8s" | sed -E 's/^([0-9]+\.[0-9]+).*/\1/')
        karpenter_max_k8s="$karpenter_min_k8s"
    fi

    # Get k8s.io dependency version (use the most common one)
    local k8s_dep_version
    k8s_dep_version=$(echo "$k8s_api_k8s" | sed -E 's/^([0-9]+\.[0-9]+).*/\1/')

    # Use the most restrictive range
    local min_k8s="$karpenter_min_k8s"
    local max_k8s="$karpenter_max_k8s"

    # If k8s.io deps are older than karpenter max, use the k8s dep version as max (only if both are valid version numbers)
    if [[ "$max_k8s" =~ ^[0-9]+\.[0-9]+$ ]] && [[ "$k8s_dep_version" =~ ^[0-9]+\.[0-9]+$ ]]; then
        if [[ $(echo "$k8s_dep_version" | cut -d. -f2) -lt $(echo "$max_k8s" | cut -d. -f2) ]]; then
            max_k8s="$k8s_dep_version"
        fi
    fi

    # Store results in associative arrays (simulated with variables)
    DEPS_INFO=$(cat <<EOF
{
    "go_version": "$go_version",
    "dependencies": {
        "k8s.io/api": {
            "version": "$k8s_api_version",
            "k8s_version": "$k8s_api_k8s"
        },
        "k8s.io/client-go": {
            "version": "$k8s_client_version",
            "k8s_version": "$k8s_client_k8s"
        },
        "k8s.io/apimachinery": {
            "version": "$k8s_apimachinery_version",
            "k8s_version": "$k8s_apimachinery_k8s"
        },
        "sigs.k8s.io/controller-runtime": {
            "version": "$controller_runtime_version"
        },
        "sigs.k8s.io/karpenter": {
            "version": "$karpenter_version",
            "k8s_compatibility": "$karpenter_k8s_compat"
        }
    },
    "supported_k8s_range": "$min_k8s - $max_k8s",
    "min_k8s_version": "$min_k8s",
    "max_k8s_version": "$max_k8s",
    "generation_date": "$(date -u +"%Y-%m-%d %H:%M:%S UTC")"
}
EOF
    )
}

# Generate dynamic testing matrix based on supported versions
generate_testing_matrix() {
    local min_version
    local max_version
    min_version=$(echo "$DEPS_INFO" | jq -r '.min_k8s_version')
    max_version=$(echo "$DEPS_INFO" | jq -r '.max_k8s_version')

    # Extract major.minor versions
    local min_minor
    local max_minor
    min_minor=$(echo "$min_version" | cut -d. -f2)
    max_minor=$(echo "$max_version" | cut -d. -f2)

    echo "| Kubernetes Version | Status | Notes |"
    echo "|-------------------|--------|-------|"

    # Generate rows for supported versions
    for ((i=min_minor; i<=max_minor; i++)); do
        local version="1.$i"
        if [[ "$version" == "$min_version" ]]; then
            echo "| $version | ✅ Supported | Minimum supported version |"
        elif [[ "$version" == "$max_version" ]]; then
            echo "| $version | ✅ Supported | Latest supported version |"
        else
            echo "| $version | ✅ Supported | |"
        fi
    done

    # Add untested version
    local next_minor=$((max_minor + 1))
    echo "| 1.${next_minor}+ | ⚠️ Untested | May work but not officially supported |"
}

# Generate markdown table
generate_markdown() {
    cat <<EOF
# Kubernetes Version Support Matrix

Generated on $(date -u +"%Y-%m-%d %H:%M:%S UTC") based on go.mod dependencies.

## Supported Kubernetes Versions

**Kubernetes $(echo "$DEPS_INFO" | jq -r '.supported_k8s_range')** (based on dependency analysis)

## Dependency Details

| Component | Version | Kubernetes API Version | Notes |
|-----------|---------|------------------------|-------|
| Go | $(echo "$DEPS_INFO" | jq -r '.go_version') | N/A | Minimum Go version |
| k8s.io/api | $(echo "$DEPS_INFO" | jq -r '.dependencies."k8s.io/api".version') | $(echo "$DEPS_INFO" | jq -r '.dependencies."k8s.io/api".k8s_version') | Kubernetes API definitions |
| k8s.io/client-go | $(echo "$DEPS_INFO" | jq -r '.dependencies."k8s.io/client-go".version') | $(echo "$DEPS_INFO" | jq -r '.dependencies."k8s.io/client-go".k8s_version') | Kubernetes client library |
| k8s.io/apimachinery | $(echo "$DEPS_INFO" | jq -r '.dependencies."k8s.io/apimachinery".version') | $(echo "$DEPS_INFO" | jq -r '.dependencies."k8s.io/apimachinery".k8s_version') | Kubernetes API machinery |
| sigs.k8s.io/controller-runtime | $(echo "$DEPS_INFO" | jq -r '.dependencies."sigs.k8s.io/controller-runtime".version') | Compatible with k8s.io deps | Controller framework |
| sigs.k8s.io/karpenter | $(echo "$DEPS_INFO" | jq -r '.dependencies."sigs.k8s.io/karpenter".version') | $(echo "$DEPS_INFO" | jq -r '.dependencies."sigs.k8s.io/karpenter".k8s_compatibility') | Karpenter core |

## Compatibility Notes

- **Kubernetes API Compatibility**: This provider uses Kubernetes $(echo "$DEPS_INFO" | jq -r '.dependencies."k8s.io/api".k8s_version') APIs
- **Karpenter Compatibility**: Karpenter $(echo "$DEPS_INFO" | jq -r '.dependencies."sigs.k8s.io/karpenter".version') supports Kubernetes $(echo "$DEPS_INFO" | jq -r '.dependencies."sigs.k8s.io/karpenter".k8s_compatibility')
- **Recommended Range**: Kubernetes $(echo "$DEPS_INFO" | jq -r '.supported_k8s_range')

## Testing Matrix

$(generate_testing_matrix)

## Update Guidance

To update Kubernetes support:

1. Check [Karpenter releases](https://github.com/kubernetes-sigs/karpenter/releases) for latest compatibility
2. Update k8s.io dependencies to match target Kubernetes version
3. Update sigs.k8s.io/karpenter to compatible version
4. Run tests against target Kubernetes versions
5. Update this documentation

EOF
}

# Generate compact markdown table for README
generate_readme_table() {
    local min_version
    local max_version
    min_version=$(echo "$DEPS_INFO" | jq -r '.min_k8s_version')
    max_version=$(echo "$DEPS_INFO" | jq -r '.max_k8s_version')

    # Extract major.minor versions
    local min_minor
    local max_minor
    min_minor=$(echo "$min_version" | cut -d. -f2)
    max_minor=$(echo "$max_version" | cut -d. -f2)

    cat <<EOF
## Kubernetes Support

| Kubernetes Version | Status |
|-------------------|--------|
EOF

    # Generate rows for supported versions
    for ((i=min_minor; i<=max_minor; i++)); do
        local version="1.$i"
        echo "| $version | ✅ Supported |"
    done

    # Add untested version
    local next_minor=$((max_minor + 1))
    echo "| 1.${next_minor}+ | ⚠️ Untested |"

    echo ""
    echo "*Based on dependency analysis. Generated on $(date -u +"%Y-%m-%d").*"
    echo ""
}

# Generate JSON output
generate_json() {
    echo "$DEPS_INFO" | jq '.'
}

# Generate YAML output
generate_yaml() {
    echo "$DEPS_INFO" | yq -P '.' 2>/dev/null || {
        log_warn "yq not available, using JSON format"
        generate_json
    }
}

# Update README.md with Kubernetes support table
update_readme() {
    local readme_table
    readme_table=$(generate_readme_table)

    if [[ ! -f "$README_FILE" ]]; then
        log_error "README.md not found at $README_FILE"
        return 1
    fi

    log_info "Updating README.md with Kubernetes support table"

    # Create a backup
    cp "$README_FILE" "${README_FILE}.bak"

    # Create a temporary file for the table content
    local temp_table_file
    temp_table_file=$(mktemp)
    echo "$readme_table" > "$temp_table_file"

    # Check if the Kubernetes Support section already exists
    if grep -q "## Kubernetes Support" "$README_FILE"; then
        log_info "Existing Kubernetes Support section found, replacing it"

        # Use awk to replace the section between "## Kubernetes Support" and the next "##" section
        awk -v table_file="$temp_table_file" '
        /^## Kubernetes Support/ {
            in_k8s_section = 1
            while ((getline line < table_file) > 0) {
                print line
            }
            close(table_file)
            next
        }
        /^## / && in_k8s_section {
            in_k8s_section = 0
        }
        !in_k8s_section { print }
        ' "$README_FILE" > "${README_FILE}.tmp" && mv "${README_FILE}.tmp" "$README_FILE"
    else
        log_info "No existing Kubernetes Support section found, adding after Overview section"

        # Insert after the Overview section
        awk -v table_file="$temp_table_file" '
        /^## Overview/ {
            print
            # Read and print all lines until we hit the next ## section or EOF
            while (getline && !/^## /) {
                print
            }
            # Print the Kubernetes Support section
            print ""
            while ((getline line < table_file) > 0) {
                print line
            }
            close(table_file)
            # Print the line that broke us out of the while loop (if any)
            if (NF > 0) print
            next
        }
        { print }
        ' "$README_FILE" > "${README_FILE}.tmp" && mv "${README_FILE}.tmp" "$README_FILE"
    fi

    # Clean up temp file
    rm -f "$temp_table_file"

    log_info "README.md updated successfully"
    log_info "Backup created at ${README_FILE}.bak"
}

# Main execution
main() {
    local format="$DEFAULT_FORMAT"

    parse_args "$@"

    # Check if go.mod exists
    if [[ ! -f "$GO_MOD_FILE" ]]; then
        log_error "go.mod not found at $GO_MOD_FILE"
        exit 1
    fi

    # Check for required tools
    if ! command -v jq >/dev/null 2>&1; then
        log_error "jq is required but not installed"
        exit 1
    fi

    # Collect dependency information
    collect_dependency_info

    # Generate output based on format
    local output_content=""
    case "$FORMAT" in
        markdown)
            output_content=$(generate_markdown)
            ;;
        json)
            output_content=$(generate_json)
            ;;
        yaml)
            output_content=$(generate_yaml)
            ;;
    esac

    # Handle README update
    if [[ "$UPDATE_README" == "true" ]]; then
        update_readme
        return 0
    fi

    # Output to file or stdout
    if [[ -n "$OUTPUT_FILE" ]]; then
        echo "$output_content" > "$OUTPUT_FILE"
        log_info "Kubernetes support table written to $OUTPUT_FILE"
    else
        echo "$output_content"
    fi
}

# Run main function
main "$@"

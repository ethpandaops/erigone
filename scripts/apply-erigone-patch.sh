#!/bin/bash

set -e

# This script applies the erigone patches + overlay to an upstream erigon clone
# Usage: ./apply-erigone-patch.sh <org/repo> <branch> [target_dir]

if [ $# -lt 2 ]; then
    echo "Usage: $0 <org/repo> <branch> [target_dir]"
    echo "Example: $0 erigontech/erigon main"
    echo "         $0 erigontech/erigon main /path/to/erigon"
    exit 1
fi

# Parse org/repo
IFS='/' read -ra REPO_PARTS <<< "$1"
if [ ${#REPO_PARTS[@]} -ne 2 ]; then
    echo "Error: Repository must be in format 'org/repo'"
    exit 1
fi
ORG="${REPO_PARTS[0]}"
REPO="${REPO_PARTS[1]}"
BRANCH="$2"
TARGET_DIR="${3:-erigon}"

# Get the script's directory (where the erigone overlay repo is)
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# Change to target directory
if [ ! -d "$TARGET_DIR" ]; then
    echo "Error: Target directory '$TARGET_DIR' does not exist"
    exit 1
fi

cd "$TARGET_DIR"

# Check if we're in a git repository
if [ ! -d ".git" ]; then
    echo "Error: Target directory is not a git repository"
    exit 1
fi

# Find all applicable patch files (base + extensions like -01-gas-fix)
# Patches are applied in order: base.patch, then base-*.patch sorted alphabetically
find_patch_files() {
    local org="$1"
    local repo="$2"
    local branch="$3"
    local patch_dir="$REPO_ROOT/patches/$org/$repo"
    local patches=()

    # Base patch is required
    local base_patch="$patch_dir/$branch.patch"
    if [ -f "$base_patch" ]; then
        patches+=("$base_patch")
    else
        echo "Error: Base patch not found at patches/$org/$repo/$branch.patch" >&2
        return 1
    fi

    # Look for extension patches (e.g., main-01-gas-fix.patch)
    for ext_patch in "$patch_dir/$branch"-*.patch; do
        if [ -f "$ext_patch" ]; then
            patches+=("$ext_patch")
        fi
    done

    printf '%s\n' "${patches[@]}"
}

# Find all patch files
PATCH_FILES=()
while IFS= read -r line; do
    [ -n "$line" ] && PATCH_FILES+=("$line")
done < <(find_patch_files "$ORG" "$REPO" "$BRANCH")
if [ ${#PATCH_FILES[@]} -eq 0 ]; then
    echo "Error: No patch files found"
    exit 1
fi

echo "Found ${#PATCH_FILES[@]} patch file(s):"
for pf in "${PATCH_FILES[@]}"; do
    echo "  - $(basename "$pf")"
done

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# Source the shared patch validation function
source "$REPO_ROOT/scripts/validate-patch.sh"

# Apply each patch in sequence
PATCHES_APPLIED=0
for PATCH_FILE in "${PATCH_FILES[@]}"; do
    echo ""
    echo -e "${BLUE}=== Applying patch: $(basename "$PATCH_FILE") ===${NC}"

    # Validate patch file structure first
    echo -e "${BLUE}  Validating patch file structure...${NC}"
    if ! validate_patch_file "$PATCH_FILE"; then
        echo -e "${RED}Patch file is corrupt or malformed${NC}"
        echo -e "${RED}  Please regenerate the patch using save-patch.sh${NC}"
        exit 1
    fi
    echo -e "${GREEN}  Patch file structure is valid${NC}"

    # Try to apply cleanly first
    if git apply --check "$PATCH_FILE" 2>/dev/null; then
        echo -e "${GREEN}  Patch check passed, applying...${NC}"
        git apply "$PATCH_FILE"
        echo -e "${GREEN}  Patch applied successfully${NC}"
        ((++PATCHES_APPLIED))
    else
        # Check if already applied
        if git apply --check --reverse "$PATCH_FILE" 2>/dev/null; then
            echo -e "${GREEN}  Patch is already applied (verified by reverse check)${NC}"
        else
            # Try 3-way merge
            echo -e "${YELLOW}  Direct apply failed, trying 3-way merge...${NC}"
            if git apply --3way "$PATCH_FILE" 2>/dev/null; then
                echo -e "${GREEN}  Patch applied with 3-way merge${NC}"
                ((++PATCHES_APPLIED))
            else
                echo -e "${RED}  Failed to apply patch: $(basename "$PATCH_FILE")${NC}"
                echo ""
                echo "Attempting apply with rejects for diagnostics..."
                git apply --reject "$PATCH_FILE" 2>&1 || true

                REJECT_FILES=$(find . -name "*.rej" 2>/dev/null | sort)
                if [ -n "$REJECT_FILES" ]; then
                    echo ""
                    echo -e "${YELLOW}=== Patch Conflict Details ===${NC}"
                    for reject in $REJECT_FILES; do
                        echo -e "${RED}  Conflict in: ${reject%.rej}${NC}"
                        cat "$reject" | sed 's/^/    /'
                        echo ""
                    done
                    find . -name "*.rej" -delete 2>/dev/null
                    find . -name "*.orig" -delete 2>/dev/null
                fi

                echo -e "${RED}Common causes:${NC}"
                echo "  - The target branch has diverged from when patch was created"
                echo "  - Recent commits modified the same lines"
                echo ""
                CURRENT_BRANCH=$(git branch --show-current 2>/dev/null || echo "detached")
                LATEST_COMMIT=$(git log -1 --oneline)
                echo -e "${BLUE}Current state:${NC}"
                echo "  Branch: $CURRENT_BRANCH"
                echo "  Latest: $LATEST_COMMIT"
                exit 1
            fi
        fi
    fi
done

# Copy overlay files
echo ""
echo -e "${BLUE}=== Copying overlay files ===${NC}"

# Copy xatu package
if [ -d "$REPO_ROOT/overlay/node/xatu" ]; then
    mkdir -p node/xatu
    cp -r "$REPO_ROOT/overlay/node/xatu/"* node/xatu/
    echo -e "${GREEN}  Copied node/xatu/ ($(ls "$REPO_ROOT/overlay/node/xatu/" | wc -l | tr -d ' ') files)${NC}"
fi

# Copy backend_xatu files
if [ -d "$REPO_ROOT/overlay/node/eth" ]; then
    for f in "$REPO_ROOT/overlay/node/eth/"*; do
        [ -f "$f" ] || continue
        cp "$f" node/eth/
        echo -e "${GREEN}  Copied node/eth/$(basename "$f")${NC}"
    done
fi

# Copy execution/vm overlay files (gas_schedule.go, etc.)
if [ -d "$REPO_ROOT/overlay/execution/vm" ]; then
    for f in "$REPO_ROOT/overlay/execution/vm/"*; do
        [ -f "$f" ] || continue
        cp "$f" execution/vm/
        echo -e "${GREEN}  Copied execution/vm/$(basename "$f")${NC}"
    done
fi

# Copy CI files
echo ""
echo -e "${BLUE}=== Copying CI files ===${NC}"

# Copy Dockerfile
if [ -f "$REPO_ROOT/ci/Dockerfile.ethpandaops" ]; then
    cp "$REPO_ROOT/ci/Dockerfile.ethpandaops" .
    echo -e "${GREEN}  Copied Dockerfile.ethpandaops${NC}"
fi

# Copy ethpandaops workflows
mkdir -p .github/workflows
for wf in "$REPO_ROOT/.github/workflows/ethpandaops-"*.yml; do
    if [ -f "$wf" ]; then
        cp "$wf" .github/workflows/
        echo -e "${GREEN}  Copied workflow: $(basename "$wf")${NC}"
    fi
done

# Update dependencies
echo ""
echo -e "${BLUE}=== Updating dependencies ===${NC}"
"$REPO_ROOT/scripts/update-deps.sh"

# Disable upstream workflows
echo ""
echo -e "${BLUE}=== Disabling upstream workflows ===${NC}"
"$REPO_ROOT/ci/disable-upstream-workflows.sh"

# Final summary
echo ""
if [ "$PATCHES_APPLIED" -eq 0 ]; then
    echo -e "${YELLOW}=========================================${NC}"
    echo -e "${YELLOW}  Erigone patches were already applied${NC}"
    echo -e "${YELLOW}=========================================${NC}"
else
    echo -e "${GREEN}=========================================${NC}"
    echo -e "${GREEN}  Successfully applied $PATCHES_APPLIED erigone patch(es)!${NC}"
    echo -e "${GREEN}=========================================${NC}"
    for pf in "${PATCH_FILES[@]}"; do
        echo -e "  ${GREEN}Applied:${NC} $(basename "$pf")"
    done
fi
echo -e "  ${GREEN}Overlay files copied${NC}"
echo -e "  ${GREEN}Dependencies updated${NC}"
echo -e "  ${GREEN}Upstream workflows disabled${NC}"

#!/bin/bash

# erigone-build.sh - Clone upstream erigon, apply patches + overlay, and build
# Usage: ./erigone-build.sh -r <org/repo> -b <branch> [-c <commit>] [--ci] [--skip-build]

set -e

# Default values
ORG=""
REPO=""
BRANCH=""
COMMIT=""
CI_MODE=false
SKIP_BUILD=false

# Parse arguments
while [ $# -gt 0 ]; do
    case "$1" in
        -r|--repo)
            IFS='/' read -ra REPO_PARTS <<< "$2"
            if [ ${#REPO_PARTS[@]} -ne 2 ]; then
                echo "Error: Repository must be in format 'org/repo'"
                exit 1
            fi
            ORG="${REPO_PARTS[0]}"
            REPO="${REPO_PARTS[1]}"
            shift 2
            ;;
        -b|--branch)
            BRANCH="$2"
            shift 2
            ;;
        -c|--commit)
            COMMIT="$2"
            shift 2
            ;;
        --ci)
            CI_MODE=true
            shift
            ;;
        --skip-build)
            SKIP_BUILD=true
            shift
            ;;
        *)
            echo "Unknown option: $1"
            echo "Usage: $0 -r org/repo -b branch [-c commit] [--ci] [--skip-build]"
            echo "  -c, --commit: Pin to specific commit SHA (optional)"
            echo "  --ci: Run in CI mode (non-interactive, auto-clean, auto-update patches)"
            echo "  --skip-build: Skip the build step (useful for Docker builds)"
            exit 1
            ;;
    esac
done

# Validate required arguments
if [ -z "$ORG" ] || [ -z "$REPO" ] || [ -z "$BRANCH" ]; then
    echo "Error: Missing required arguments"
    echo "Usage: $0 -r org/repo -b branch [-c commit] [--ci] [--skip-build]"
    echo "Example: $0 -r erigontech/erigon -b main"
    echo "Example: $0 -r erigontech/erigon -b main -c 5aff1fcb75befcde2f956a5b38a9deec5cc4123c"
    exit 1
fi

if [ -n "$COMMIT" ]; then
    echo "Testing with repository: $ORG/$REPO on branch: $BRANCH at commit: $COMMIT"
else
    echo "Testing with repository: $ORG/$REPO on ref: $BRANCH"
fi

# Store the script directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Check if the current checkout matches the desired ref (branch or tag)
is_on_correct_ref() {
    # Check branch first
    local current_branch
    current_branch=$(git branch --show-current 2>/dev/null)
    if [ -n "$current_branch" ] && [ "$current_branch" = "$BRANCH" ]; then
        return 0
    fi

    # Check tag (detached HEAD on correct tag)
    local current_tag
    current_tag=$(git describe --tags --exact-match HEAD 2>/dev/null)
    if [ -n "$current_tag" ] && [ "$current_tag" = "$BRANCH" ]; then
        return 0
    fi

    return 1
}

# Check if BRANCH refers to a tag on the remote
is_remote_tag() {
    git ls-remote --tags "https://github.com/$ORG/$REPO.git" "$BRANCH" 2>/dev/null | grep -q "refs/tags/$BRANCH"
}

# Clone the repository fresh
clone_repo() {
    echo "Cloning repository..."
    if [ -n "$COMMIT" ]; then
        git clone --branch "$BRANCH" "https://github.com/$ORG/$REPO.git" erigon
        cd erigon && git checkout "$COMMIT" && cd ..
    else
        git clone --depth 1 --branch "$BRANCH" "https://github.com/$ORG/$REPO.git" erigon
    fi
}

# Clean working directory (handles interactive/CI)
clean_working_dir() {
    if ! git diff --quiet || ! git diff --cached --quiet || [ -n "$(git ls-files --others --exclude-standard)" ]; then
        if [ "$CI_MODE" = true ]; then
            echo "CI mode: Auto-cleaning erigon directory..."
            git reset --hard
            git clean -fd
        else
            echo "WARNING: erigon directory has uncommitted changes"
            git status --short | head -20
            read -p "Clean erigon directory before continuing? (y/N) " -n 1 -r
            echo ""
            if [[ $REPLY =~ ^[Yy]$ ]]; then
                git reset --hard
                git clean -fd
            else
                echo "Cannot continue with uncommitted changes. Exiting."
                cd ..
                exit 1
            fi
        fi
    fi
}

# Check if erigon directory exists and handle it
if [ -d "erigon" ]; then
    echo "Found existing erigon directory, checking..."
    cd erigon

    if [ -d ".git" ]; then
        CURRENT_REMOTE=$(git config --get remote.origin.url || echo "")
        EXPECTED_REMOTE="https://github.com/$ORG/$REPO.git"

        if [ "$CURRENT_REMOTE" != "$EXPECTED_REMOTE" ]; then
            echo "Remote mismatch: current=$CURRENT_REMOTE, expected=$EXPECTED_REMOTE"
            cd ..
            echo "Removing existing erigon directory..."
            rm -rf erigon
            clone_repo
        elif is_on_correct_ref; then
            # Already on correct ref, check for updates
            if [ -n "$COMMIT" ]; then
                LOCAL=$(git rev-parse HEAD)
                if [ "$LOCAL" != "$COMMIT" ]; then
                    echo "Fetching and checking out commit $COMMIT..."
                    git fetch origin "$BRANCH"
                    git checkout "$COMMIT"
                else
                    echo "Already on target commit"
                fi
            elif is_remote_tag; then
                # Tags are immutable, no update needed
                echo "On tag $BRANCH, no update needed"
            else
                echo "Checking for updates..."
                git fetch --depth 1 origin "$BRANCH" || true

                if git rev-parse --verify "origin/$BRANCH" >/dev/null 2>&1; then
                    LOCAL=$(git rev-parse HEAD)
                    REMOTE=$(git rev-parse "origin/$BRANCH")

                    if [ "$LOCAL" != "$REMOTE" ]; then
                        echo "Local branch is behind remote, pulling latest..."
                        git pull --depth 1 origin "$BRANCH"
                    else
                        echo "Already on latest commit"
                    fi
                fi
            fi

            cd ..
        else
            echo "Ref mismatch: expected=$BRANCH"
            clean_working_dir
            # Easiest path: re-clone for the correct ref
            cd ..
            echo "Removing erigon directory for ref switch..."
            rm -rf erigon
            clone_repo
        fi
    else
        cd ..
        echo "Directory exists but is not a git repository, removing..."
        rm -rf erigon
        clone_repo
    fi
else
    clone_repo
fi

# Clean erigon directory if it has leftover changes
echo "Checking erigon directory status..."
cd erigon
if ! git diff --quiet || ! git diff --cached --quiet || [ -n "$(git ls-files --others --exclude-standard)" ]; then
    echo "WARNING: erigon directory has changes"
    if [ "$CI_MODE" = true ]; then
        echo "CI mode: Auto-cleaning..."
        git reset --hard
        git clean -fd
    else
        git status --short | head -20
        read -p "Clean erigon directory before continuing? (y/N) " -n 1 -r
        echo ""
        if [[ $REPLY =~ ^[Yy]$ ]]; then
            git reset --hard
            git clean -fd
        fi
    fi
fi
cd ..

# Apply the erigone patches + overlay
echo "Applying erigone patches..."
"$SCRIPT_DIR/apply-erigone-patch.sh" "$ORG/$REPO" "$BRANCH" erigon

if [ "$SKIP_BUILD" = true ]; then
    echo ""
    echo "Build skipped (--skip-build). Patched source is in erigon/"
    exit 0
fi

# Build the project
echo ""

# Determine build tags: main gets erigon_main for build-tagged overlay variants
BUILD_TAGS="embedded,nosqlite,noboltdb,nosilkworm"
if [ "$BRANCH" = "main" ]; then
    BUILD_TAGS="$BUILD_TAGS,erigon_main"
fi

echo "Building erigon with tags: $BUILD_TAGS..."
cd erigon
if make erigon BUILD_TAGS="$BUILD_TAGS"; then
    echo "Build completed successfully!"
    cd ..

    echo ""
    echo "Generating patch from build changes..."

    CI_FLAGS=""
    if [ "$CI_MODE" = true ]; then
        CI_FLAGS="--ci"
    fi

    if [ "$CI_MODE" = true ]; then
        PATCH_OUTPUT=$("$SCRIPT_DIR/save-patch.sh" -r "$ORG/$REPO" -b "$BRANCH" $CI_FLAGS erigon 2>&1) || PATCH_EXIT_CODE=$?
        PATCH_EXIT_CODE=${PATCH_EXIT_CODE:-0}
    else
        "$SCRIPT_DIR/save-patch.sh" -r "$ORG/$REPO" -b "$BRANCH" erigon || PATCH_EXIT_CODE=$?
        PATCH_EXIT_CODE=${PATCH_EXIT_CODE:-0}
    fi

    if [ $PATCH_EXIT_CODE -eq 0 ]; then
        [ "$CI_MODE" = true ] && echo "Patch saved: $PATCH_OUTPUT"
        echo ""
        echo "Build and patch generation completed successfully!"
    elif [ $PATCH_EXIT_CODE -eq 2 ]; then
        echo "No changes detected - patch unchanged"
    else
        echo "Warning: Failed to generate patch (exit code: $PATCH_EXIT_CODE)"
    fi
else
    echo "Build failed!"
    exit 1
fi

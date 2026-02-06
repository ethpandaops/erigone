#!/bin/bash

# erigone-build.sh - Clone upstream erigon, apply patches + overlay, and build
# Usage: ./erigone-build.sh -r <org/repo> -b <branch> [--ci] [--skip-build]

set -e

# Default values
ORG=""
REPO=""
BRANCH=""
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
            echo "Usage: $0 -r org/repo -b branch [--ci] [--skip-build]"
            echo "  --ci: Run in CI mode (non-interactive, auto-clean, auto-update patches)"
            echo "  --skip-build: Skip the build step (useful for Docker builds)"
            exit 1
            ;;
    esac
done

# Validate required arguments
if [ -z "$ORG" ] || [ -z "$REPO" ] || [ -z "$BRANCH" ]; then
    echo "Error: Missing required arguments"
    echo "Usage: $0 -r org/repo -b branch [--ci] [--skip-build]"
    echo "Example: $0 -r erigontech/erigon -b main"
    exit 1
fi

echo "Testing with repository: $ORG/$REPO on branch: $BRANCH"

# Store the script directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

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
            echo "Cloning repository..."
            git clone --depth 1 --branch "$BRANCH" "https://github.com/$ORG/$REPO.git" erigon
        else
            CURRENT_BRANCH=$(git branch --show-current)
            if [ "$CURRENT_BRANCH" != "$BRANCH" ]; then
                echo "Branch mismatch: current=$CURRENT_BRANCH, expected=$BRANCH"

                if ! git diff --quiet || ! git diff --cached --quiet || [ -n "$(git ls-files --others --exclude-standard)" ]; then
                    if [ "$CI_MODE" = true ]; then
                        echo "CI mode: Auto-cleaning erigon directory..."
                        git reset --hard
                        git clean -fd
                    else
                        echo "WARNING: erigon directory has uncommitted changes"
                        git status --short | head -20
                        read -p "Clean erigon directory before switching branches? (y/N) " -n 1 -r
                        echo ""
                        if [[ $REPLY =~ ^[Yy]$ ]]; then
                            git reset --hard
                            git clean -fd
                        else
                            echo "Cannot switch branches with uncommitted changes. Exiting."
                            cd ..
                            exit 1
                        fi
                    fi
                fi

                echo "Switching to branch $BRANCH..."
                git fetch --depth 1 origin "$BRANCH":"$BRANCH"
                git checkout "$BRANCH"
            fi

            # Update to latest
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

            cd ..
        fi
    else
        cd ..
        echo "Directory exists but is not a git repository, removing..."
        rm -rf erigon
        echo "Cloning repository..."
        git clone --depth 1 --branch "$BRANCH" "https://github.com/$ORG/$REPO.git" erigon
    fi
else
    echo "Cloning repository..."
    git clone --depth 1 --branch "$BRANCH" "https://github.com/$ORG/$REPO.git" erigon
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
echo "Building erigon with embedded tags..."
cd erigon
if make erigon BUILD_TAGS=embedded,nosqlite,noboltdb,nosilkworm; then
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

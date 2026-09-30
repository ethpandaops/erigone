#!/bin/bash
# overlay-variant.sh - Print the overlay variant ("main" or "v3") for a patch target
# Usage: ./overlay-variant.sh <org/repo> <ref>
#
# The "main" variant copies *_main overlay files and builds with the erigon_main tag;
# "v3" copies *_v3 files and builds without it. Targets default to "main"; a target on
# the older API opts out with a `variant` file next to its base.patch.

set -e

if [ $# -ne 2 ]; then
    echo "Usage: $0 <org/repo> <ref>" >&2
    exit 1
fi

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VARIANT_FILE="$REPO_ROOT/patches/$1/$2/variant"

VARIANT="main"
if [ -f "$VARIANT_FILE" ]; then
    VARIANT="$(tr -d '[:space:]' < "$VARIANT_FILE")"
fi

case "$VARIANT" in
    main|v3) echo "$VARIANT" ;;
    *)
        echo "Error: unknown overlay variant '$VARIANT' in $VARIANT_FILE (expected main or v3)" >&2
        exit 1
        ;;
esac

# Erigone

Patch-based overlay for integrating [ethpandaops execution-processor](https://github.com/ethpandaops/execution-processor) (Xatu) observability into [Erigon](https://github.com/erigontech/erigon).

## Overview

Erigone uses a **patch + overlay** approach instead of maintaining a full fork. The repo stores only custom code and small patches; upstream Erigon is cloned fresh each build.

### Repository Structure

```
erigone/
├── overlay/                      # Custom code (copied into upstream clone)
│   ├── node/xatu/                # Xatu package (service, datasource, adapters, tracer)
│   └── node/eth/                 # Build-tagged integration files
│       ├── backend_xatu.go       # Xatu init (//go:build embedded)
│       └── backend_xatu_stub.go  # No-op stub (//go:build !embedded)
├── patches/
│   └── erigontech/erigon/
│       ├── main/
│       │   └── base.patch        # Integration patch for main branch
│       └── v3.3.10/
│           └── base.patch        # Integration patch for v3.3.10 tag
├── ci/
│   ├── Dockerfile.ethpandaops    # Multi-arch Docker build
│   └── disable-upstream-workflows.sh
├── .github/workflows/
│   ├── check-patches.yml         # Daily: verify patches apply + build
│   ├── docker-release.yml        # On release: build + push Docker image
│   ├── docker-pr-build.yml       # On PR label: build test image
│   └── validate-patches.yml      # On PR: validate patch file structure
├── scripts/
│   ├── apply-erigone-patch.sh    # Apply patches + overlay + deps
│   ├── save-patch.sh             # Regenerate patches from modified clone
│   ├── erigone-build.sh          # Full orchestrator: clone -> patch -> build
│   ├── update-deps.sh            # go get + go mod tidy (pinned versions)
│   └── validate-patch.sh         # Patch file structural validation
└── .gitignore                    # Ignore erigon/ working directory
```

## Quick Start

### Build

```bash
# Full build against main (latest upstream)
./scripts/erigone-build.sh -r erigontech/erigon -b main

# Full build against a tagged release (stable)
./scripts/erigone-build.sh -r erigontech/erigon -b v3.3.10

# The binary will be at erigon/build/bin/erigon
```

### Docker

```bash
# Prepare patched source (skip Go build, let Docker handle it)
./scripts/erigone-build.sh -r erigontech/erigon -b main --skip-build

# Build Docker image from the patched source
cd erigon
docker build -f Dockerfile.ethpandaops -t ethpandaops/erigone:latest .
```

### Run

```bash
./erigon/build/bin/erigon --xatu.config /path/to/xatu-config.yaml --chain mainnet
```

## Scripts

| Script | Purpose |
|---|---|
| `erigone-build.sh` | Full orchestrator: clone upstream, apply patches + overlay, build |
| `apply-erigone-patch.sh` | Apply patches to an existing erigon clone + copy overlay + deps |
| `save-patch.sh` | Regenerate patches from a modified erigon clone |
| `update-deps.sh` | Add erigone-specific Go dependencies via `go get` |
| `validate-patch.sh` | Validate patch file structure (hunk counts, etc.) |
| `disable-upstream-workflows.sh` | Rename upstream CI workflows to `.disabled` |

## How It Works

### Custom Code as Overlay

`node/xatu/` and `backend_xatu*.go` are **new files** that are copied into the upstream clone. They use Go build tags (`//go:build embedded`) so they never conflict with upstream code.

### Dependencies via Script

Instead of patching `go.mod` (which breaks on every upstream dependency change), `update-deps.sh` uses `go get` to add erigone-specific dependencies:

```bash
go get github.com/ethpandaops/execution-processor@<version>
go get github.com/creasty/defaults@v1.8.0
go get github.com/redis/go-redis/v9@v9.17.2
go get github.com/sirupsen/logrus@v1.9.3
go mod tidy
```

### CI Workflow Disabling

Instead of patching 39+ workflow renames, a simple script renames all non-ethpandaops workflows to `.disabled`.

### Patches

Patches are organized per upstream ref in subdirectories under `patches/org/repo/`:

```
patches/erigontech/erigon/
  main/base.patch       # Patch for main branch
  v3.3.10/base.patch    # Patch for v3.3.10 tag
```

Extension patches (e.g., `01-gas-fix.patch`) can be placed in the same directory and are applied alphabetically after `base.patch`.

The `-b` flag to build scripts accepts both branch names and tags.

## Development

### Adding a new feature

Most features fall into one or more of these categories:

#### New files (overlay)

Self-contained new code goes in `overlay/`. These files are copied verbatim into the upstream clone at build time — no patches, no conflicts.

```bash
# Add new files directly in the overlay directory
vim overlay/node/xatu/new_feature.go

# If you need build-tagged integration points:
vim overlay/node/eth/backend_newfeature.go       # //go:build embedded
vim overlay/node/eth/backend_newfeature_stub.go   # //go:build !embedded

# Commit to the overlay branch
git add overlay/
git commit -m "feat: add new feature"
```

Use `//go:build embedded` tags so overlay code is only compiled when building with `BUILD_TAGS=embedded`.

#### Modifying upstream files (patch)

If your feature requires changing existing upstream code (e.g. adding a CLI flag or wiring a new call into `backend.go`):

```bash
# 1. Build to get the working upstream clone
./scripts/erigone-build.sh -r erigontech/erigon -b main

# 2. Edit upstream files in the clone
vim erigon/node/eth/backend.go
vim erigon/cmd/utils/flags.go

# 3. Regenerate the patch
./scripts/save-patch.sh -r erigontech/erigon -b main erigon

# 4. Commit the updated patch
git add patches/
git commit -m "feat: add new-feature wiring to main patch"
```

Changes are folded into `base.patch`. If the change is logically separate, create a new extension patch instead — name it `01-your-feature.patch` in the same directory and it will be picked up automatically in alphabetical order.

> **Note:** If you maintain patches for multiple upstream refs (e.g., `main` and `v3.3.10`), you'll need to port changes to each ref's patch directory separately.

#### New dependency

```bash
# Edit the deps script
vim scripts/update-deps.sh
# Add: go get github.com/some/package@v1.2.3

git add scripts/update-deps.sh
git commit -m "deps: add some/package for new feature"
```

> **Tip:** Most features are overlay files + maybe a new dependency. Touching upstream files should be rare and minimal — the less patch surface, the fewer sync conflicts.

### Fixing a patch conflict

When upstream changes the same lines our patches touch, `apply-erigone-patch.sh` will fail. To fix:

```bash
# 1. Run the build — it will show exactly which hunks failed
./scripts/erigone-build.sh -r erigontech/erigon -b main

# 2. Fix the conflicts in the upstream clone
vim erigon/node/eth/backend.go

# 3. Regenerate the patch
./scripts/save-patch.sh -r erigontech/erigon -b main erigon

# 4. Commit the updated patch
git add patches/
git commit -m "fix: update patches for upstream changes"
git push
```

### Dropping a patch

Extension patches (e.g. `main/01-gas-fix.patch`) are independently droppable. If upstream fixes the bug:

```bash
git rm patches/erigontech/erigon/main/01-gas-fix.patch
git commit -m "chore: drop gas-fix patch, merged upstream"
```

## Updating

### Bump upstream

```bash
# Build against latest upstream - if patches need updating, save-patch.sh will detect it
./scripts/erigone-build.sh -r erigontech/erigon -b main
```

### Bump execution-processor

Edit `scripts/update-deps.sh` and change the version:
```bash
go get github.com/ethpandaops/execution-processor@<new-version>
```

## CI

| Workflow | Trigger | What it does |
|---|---|---|
| `check-patches.yml` | Daily (cron) | Checks all patch targets (main + tags), auto-commits if patches needed updating |
| `docker-release.yml` | GitHub release | Builds + pushes multi-arch Docker images for all patch targets |
| `docker-pr-build.yml` | PR with `build-image` label | Builds test Docker images for all patch targets |
| `validate-patches.yml` | PR | Validates patch file structure (hunk counts, etc.) |

CI discovers all patch targets automatically from the `patches/` directory structure. Each directory containing a `base.patch` becomes a build target. When the daily CI detects that patches needed a 3-way merge, it auto-commits the updated patches. If patches completely fail, CI fails and you'll need to fix the conflict manually (see [Fixing a patch conflict](#fixing-a-patch-conflict)).

Docker images are tagged with the upstream ref as a suffix (e.g., `erigone:v0.1.0-main`, `erigone:v0.1.0-v3.3.10`). The `main` variant also gets the unqualified `latest` tag.

## Requirements

- Go 1.25+
- GCC 10+ or Clang
- Git
- Make

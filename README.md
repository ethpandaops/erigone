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
│       ├── main.patch            # Integration: backend.go, config.go, flags.go
│       └── main-01-gas-fix.patch # Bug fix: callGas underflow guard
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
# Full build: clone upstream -> apply patches -> build binary
./scripts/erigone-build.sh -r erigontech/erigon -b main

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

The actual patch surface is minimal:
- **`main.patch`** (~70 lines): Adds `--xatu.config` flag and `initXatu()` call
- **`main-01-gas-fix.patch`** (~15 lines): Guards `callGas()` against underflow

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

### Manual patch editing

```bash
# 1. Clone and patch
./scripts/erigone-build.sh -r erigontech/erigon -b main

# 2. Make changes in erigon/
cd erigon
# ... edit files ...
cd ..

# 3. Regenerate patches
./scripts/save-patch.sh -r erigontech/erigon -b main erigon
```

## CI

- **Daily `check-patches.yml`**: Clones upstream, applies patches, builds, auto-commits if patches needed updating
- **Release `docker-release.yml`**: Builds multi-arch Docker image on GitHub release
- **PR `docker-pr-build.yml`**: Builds test image when `build-image` label is applied
- **Validation `validate-patches.yml`**: Validates patch file structure on PR

## Requirements

- Go 1.25+
- GCC 10+ or Clang
- Git
- Make

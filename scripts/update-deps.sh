#!/bin/bash
# update-deps.sh - Add erigone-specific Go dependencies
# Run this from the upstream erigon clone directory after applying patches
set -e

echo "Adding erigone-specific dependencies..."

go get github.com/ethpandaops/execution-processor@v0.1.6-0.20260211014607-8e71e1720ddf
go get github.com/creasty/defaults@v1.8.0
go get github.com/redis/go-redis/v9@v9.17.2
go get github.com/sirupsen/logrus@v1.9.3

echo "Running go mod tidy..."
go mod tidy

echo "Dependencies updated successfully"

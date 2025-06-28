#!/usr/bin/env bash
set -e

# build locally with version info for testing ... releases use goreleaser, not this file

# Get version from git tag or use "dev"
VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo "dev")

# Get commit hash
COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")

# Get build date
BUILD_DATE=$(date -u +"%Y-%m-%d %H:%M:%S UTC")

# Build with version info
echo "Building fatimg ${VERSION}..."
go build -ldflags "-X 'main.version=${VERSION}' -X 'main.commit=${COMMIT}' -X 'main.buildDate=${BUILD_DATE}'" -o fatimg

echo "Build complete: ./fatimg"
echo "Version: ${VERSION}"
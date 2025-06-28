# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

`fatimg` is a Go utility for creating and managing FAT32 boot (EFI) partition disk images. It provides an alternative to mtools or loopback mounts by using the go-diskfs library.

## Common Development Commands

### Build

#### Development build
```bash
go build
```

#### Build with version information
```bash
./build.sh
```

#### Release build (using GoReleaser)
```bash
# Create and push a git tag
git tag v0.4.0
git push origin v0.4.0
# GitHub Actions will automatically build and release using GoReleaser
```

### Test boot image with QEMU
```bash
./run.sh
```
Note: Requires QEMU and expects a `disk.img` file. The script uses UEFI firmware from homebrew's QEMU installation.

### Create test disk images
```bash
cd test
./mkimage.sh
```
Note: Uses Docker with mtools to create reference disk images for testing.

## Architecture

The project implements a command pattern with three subcommands:

1. **create** - Creates a disk image with FAT32 EFI partition
   - Key file: `create.go`
   - Creates MBR-partitioned disk with a single FAT32 partition
   - Supports gzip compression, partition sizing, volume labels
   - Recursively copies files/folders into the partition

2. **ls** - Lists contents of the first partition
   - Key file: `list.go`
   - Supports long format (`-long`) output
   - Automatically handles gzipped disk images

3. **cp** - Extracts files from disk image
   - Key file: `copy.go`
   - Currently only supports copying from disk image to local filesystem

## Key Implementation Details

- Uses a custom fork of `go-diskfs` to fix issues with 8.3 lowercase files created by mtools
- All disk operations assume the first partition is FAT32
- Automatic gzip compression/decompression based on file extension (.gz)
- When creating compressed images, uses parallel gzip (pgzip) for performance
- Temporary files are used for atomic operations (write to temp, then rename)

## Testing Approach

No formal Go test suite exists. Testing is done by:
1. Building the binary
2. Creating disk images with various options
3. Testing boot with QEMU using `run.sh`
4. Comparing behavior with mtools-created images using `test/mkimage.sh`
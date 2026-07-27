# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

See [README.md](README.md) for what fatimg does, the subcommands and their
flags, installation, and the prerequisites for building and testing. This file
covers only what is not there: how the code is put together and what to be
careful about.

## Development commands

Use the Makefile rather than raw `go build` / `go test` — the targets check
prerequisites and disable the test cache where it would otherwise mislead.

```bash
make build         # build with version info
make test          # all tests; fails early if a required tool is missing
make unit          # only the tests that need no external tools
make integration   # round-trip and external validation tests, verbose
make boot          # boot a --bios-boot image under QEMU, verbose
make syslinux      # download the SYSLINUX release the boot test installs
make check         # what CI runs: gofmt, vet, tests
make help          # list all targets
```

`make test` requires `mtools`, `dosfstools` and `qemu-system-x86_64` (see the
README). A missing tool is a failure, not a skip, because a skipped validation
tier looks exactly like a passing one. The boot test also needs a SYSLINUX
release; `make` downloads one into `.syslinux/` and exports
`FATIMG_SYSLINUX_DIR`, so run the boot test through make rather than calling
`go test` directly.

Releases are built by GoReleaser from a pushed tag; `build-with-version.sh`
predates the Makefile and `make build` supersedes it.

## Architecture

A small command pattern: `main.go` dispatches to one `Runner` per subcommand.

| Subcommand | File | Notes |
|---|---|---|
| `create` | `create.go` | MBR-partitioned disk with a single FAT32 partition; recursively copies files in |
| `ls` | `list.go` | Also holds the gzip sniffing and temp-file decompression shared with `cp` |
| `cp` | `copy.go` | Extract to a local directory |

`boot.go` is not a subcommand: it is the SYSLINUX installer behind
`create --bios-boot`, and runs after the filesystem is closed, on the image as
a plain file.

Commands return errors; they must not call `os.Exit`. Tests drive
`executeSubcommand` in-process, so an exit would take the test binary with it.
The one remaining exit path is `flag.ExitOnError` on the FlagSets, so an
unrecognised flag still exits — do not write a test that passes one until those
are switched to `flag.ContinueOnError`.

## Key implementation details

- The first partition is assumed to be FAT32 throughout.
- Gzip is detected by magic bytes, not by file extension — `create --gzip` will
  happily write a compressed image to a name not ending in `.gz`.
- `create` writes to a temp file in the output directory and renames on success,
  removing it if anything fails.
- `copyFile` reads the whole file into memory before writing so that FAT32
  allocates the cluster chain once; see go-diskfs issue #130.
- go-diskfs writes placeholders into two BPB fields: hidden sectors is always
  0, and the CHS geometry is 1/1. Both are wrong for a filesystem inside a
  partition, and SYSLINUX adds hidden sectors to every sector it reads, so
  `fixBPBGeometry` corrects them (in the primary and the backup boot sector)
  for every image, bootable or not.

## SYSLINUX installation (`boot.go`)

Four things make a FAT32 image BIOS-bootable, and only the last is real work:
`mbr.bin` in the first 440 bytes of sector 0; a correct hidden-sector count;
the SYSLINUX boot sector merged into the partition, keeping the BPB; and
`ldlinux.sys` patched with the list of disk sectors it occupies. A 512-byte
boot sector cannot walk a FAT chain, so the sector map is stamped in at install
time, along with a checksum ldlinux.sys verifies against itself at boot.

- This follows `syslinux_patch()` in syslinux-6.03
  `libinstaller/syslxmod.c`. The structure offsets are read out of the image
  rather than hardcoded, because they are a property of the ldlinux.sys build
  being installed — which is also why the files must come from one release.
- `mbr.Table.Write` only touches bytes 446 onwards, so writing the bootstrap at
  offset 0 cannot disturb the partition table.
- ldlinux.sys is written before the user's files so it lands contiguously.
  It is addressed by an extent list with room for 192 entries, and a fragmented
  one would overflow it; `generateExtents` errors rather than truncating.
- SYSLINUX is not vendored or embedded. It is GPL-2.0 and fatimg is Apache-2.0,
  so the files come from `--syslinux-dir` at run time. Do not add a `go:embed`
  of them without settling that first.
- A FAT32 with 65524 clusters or fewer reads as FAT16 by the standard rule, and
  SYSLINUX applies it. `installSyslinux` rejects such an image; without that
  check the symptom is a boot that prints the SYSLINUX banner and then cannot
  find ldlinux.c32, which is a long way from the cause.

## go-diskfs is pinned to v1.6.0 — do not upgrade casually

v1.9.4 was tried and reverted. It is source-compatible but changes semantics,
and shipped a release (v0.5.2) in which every subcommand was broken:

- `mbr.Partition` gained `Index`, and partitions are resolved by it rather than
  by slice position, so a partition built without one is never found.
- `filesystem.FileSystem` embeds the `io/fs` interfaces, whose `fs.ValidPath`
  rejects a leading slash, so `ReadDir("/")` returns `invalid argument`.
- It writes an invalid `..` entry in every subdirectory, and sizes the FAT two
  entries short at 64MB and every 65MB after it. Neither is fixable from here.

If you upgrade it, run `make integration` and read the output. The last two
defects are only visible through `fsck.fat`, which is why that tier exists.

## Testing approach

Four tiers, all under `go test`:

1. **Unit** (`create_test.go`, `boot_test.go`) — `trimFile` across chunk
   boundaries, path re-rooting, and the SYSLINUX patch arithmetic.
2. **Round-trip** (`roundtrip_test.go`) — create an image from a fixture tree
   and extract it again, asserting byte-identical contents across the option
   matrix. This is the tier that catches a broken `create`/`ls`/`cp`.
3. **External validation** (`external_test.go`) — `mdir`, `mcopy` and
   `fsck.fat` check the image against independent FAT implementations, so a
   self-consistent but non-conforming filesystem cannot pass.
4. **Boot** (`qemu_test.go`) — build a `--bios-boot` image and run it on an
   emulated PC, checking that SYSLINUX reached the `syslinux.cfg` in the image.
   Nothing below this tier can catch a broken bootloader install: an image with
   no boot code at all passes every other test here, because a boot sector is
   not part of the filesystem as far as `fsck.fat` is concerned.

When fixing a bug, confirm the new test fails with the fix reverted. Every
regression covered here was verified that way.

`test/mkimage.sh` builds reference images with Docker and mtools; it predates
the Go tests and is no longer part of the normal loop.

## CI

`ci.yml` runs on push and PR. `release.yml` runs on tags, and its goreleaser job
has `needs: test`, so a tag whose tests fail cannot publish. Do not remove that
dependency — it exists because v0.5.2 was released broken.

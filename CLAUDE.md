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
make test          # all tests; fails early if mtools/dosfstools are missing
make unit          # only the tests that need no external tools
make integration   # round-trip and external validation tests, verbose
make check         # what CI runs: gofmt, vet, tests
make help          # list all targets
```

`make test` requires `mtools` and `dosfstools` (see the README). A missing tool
is a failure, not a skip, because a skipped validation tier looks exactly like a
passing one.

Releases are built by GoReleaser from a pushed tag; `build-with-version.sh`
predates the Makefile and `make build` supersedes it.

## Architecture

A small command pattern: `main.go` dispatches to one `Runner` per subcommand.

| Subcommand | File | Notes |
|---|---|---|
| `create` | `create.go` | MBR-partitioned disk with a single FAT32 partition; recursively copies files in |
| `ls` | `list.go` | Also holds the gzip sniffing and temp-file decompression shared with `cp` |
| `cp` | `copy.go` | Extract to a local directory |

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

Three tiers, all under `go test`:

1. **Unit** (`create_test.go`) — `trimFile` across chunk boundaries, path
   re-rooting.
2. **Round-trip** (`roundtrip_test.go`) — create an image from a fixture tree
   and extract it again, asserting byte-identical contents across the option
   matrix. This is the tier that catches a broken `create`/`ls`/`cp`.
3. **External validation** (`external_test.go`) — `mdir`, `mcopy` and
   `fsck.fat` check the image against independent FAT implementations, so a
   self-consistent but non-conforming filesystem cannot pass.

When fixing a bug, confirm the new test fails with the fix reverted. Every
regression covered here was verified that way.

`test/mkimage.sh` builds reference images with Docker and mtools; it predates
the Go tests and is no longer part of the normal loop.

## CI

`ci.yml` runs on push and PR. `release.yml` runs on tags, and its goreleaser job
has `needs: test`, so a tag whose tests fail cannot publish. Do not remove that
dependency — it exists because v0.5.2 was released broken.

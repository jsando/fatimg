# fatimg

Experimental utility to build an fat32 boot (EFI) partition in a disk image using the excellent go-diskfs library, 
instead of mtools or loopback mounts.

Assuming you have your linux kernel, initrd, and whatever other files you need you can point this program
at the folder with them and it will install them into a disk image with an EFI partition.
 
fatimg can create, extract, and list files on the first partition (which must be FAT32) of a disk image.

The disk image file can optionally be gzipped, in which case fatimg will automatically gunzip it to a tmp file.

With `--syslinux` it can also make the image bootable by a legacy BIOS, by installing SYSLINUX. See
[Legacy BIOS boot](#legacy-bios-boot) below.

WARNING: there will probably be breaking changes as I intend to extend it to support GPT
(so additional cli flags to specify what is now assumed defaults).

## Installation

### Using Homebrew

```bash
brew tap jsando/tools
brew install fatimg
```

### Using Go

```bash
go install github.com/jsando/fatimg@latest
```

### Pre-built binaries

Download a pre-built binary from the [releases](https://github.com/jsando/fatimg/releases) page.

## Building and testing

Building needs nothing but a Go toolchain:

```bash
make build
```

Running the tests additionally requires **mtools**, **dosfstools** and **QEMU**:

```bash
# macOS
brew install mtools dosfstools qemu

# Debian/Ubuntu
sudo apt-get install mtools dosfstools qemu-system-x86
```

The first two provide `mdir`, `mcopy` and `fsck.fat`. The tests use them to
check that disk images are valid according to independent FAT implementations,
rather than only checking that fatimg agrees with itself — a filesystem can be
self-consistent and still be wrong. QEMU is used by the boot test, which builds
a `--syslinux` image and runs it on an emulated PC; no filesystem check can
tell you whether a bootloader install worked, because a boot sector is not part
of the filesystem. They are a hard requirement: if a tool is missing the tests
fail rather than skip, because a skipped check is indistinguishable from a
passing one.

The boot test also needs a SYSLINUX release to install. `make` downloads one
into `.syslinux/` for you rather than vendoring it into the repository.

```bash
make test         # everything (fails early if a tool is missing)
make unit         # only the tests that need no external tools
make integration  # round-trip and validation tests, verbose
make boot         # boot a --syslinux image under QEMU, verbose
make check        # what CI runs: gofmt, vet, tests
make help         # list all targets
```

## Commands

### Create a disk image

```
Create a disk image with a FAT32 partition.

The contents of the partition are specified as a list of one or more paths.
Folders are copied recursively, and include the folder name itself
unless it ends with a trailing '/'.

Usage:
  fatimg create [options] <path> [<path> ...]

Options:
  -gzip
    	compress output file with gzip (automatic if output ends with '.gz')
  -label string
    	EFI partition volume label (default "boot")
  -output string
    	output path (required)
  -part-type string
    	MBR partition type, "efi" or "fat32" (default "efi")
  -size int
    	partition size in megabytes (default 1024)
  -syslinux
    	make the image bootable by a legacy BIOS, using SYSLINUX
  -syslinux-dir string
    	directory holding the SYSLINUX release to install (required with --syslinux)
  -trim
    	trim disk image before compressing (truncate zero-filled sectors at the end)
```

Example:
```bash
# Create a compressed 512MB disk image with label "BOOT"
fatimg create --output boot.img.gz --size 512 --label BOOT ./EFI/
```

### Legacy BIOS boot

`--syslinux` installs SYSLINUX into the image so a PC BIOS will boot it:
the SYSLINUX MBR bootstrap in sector 0, its boot sector merged into the
partition, and `ldlinux.sys` and `ldlinux.c32` written to the filesystem root.
You supply the `syslinux.cfg`, as one of the paths copied in.

```bash
fatimg create --output disk.img --size 512 \
    --syslinux --syslinux-dir ~/syslinux-6.03 --part-type fat32 ./boot/
```

SYSLINUX is not bundled with fatimg, so `--syslinux-dir` has to point at your
own copy. It needs `mbr.bin`, `ldlinux.bss`, `ldlinux.sys` and `ldlinux.c32`,
and they should all come from the same release. An unpacked
[syslinux release tarball](https://mirrors.edge.kernel.org/pub/linux/utils/boot/syslinux/)
works as-is: the tarball ships them prebuilt. Most distribution packages do
**not**, because their `syslinux` installer has `ldlinux.bss` and `ldlinux.sys`
linked into the binary rather than shipped as files.

Two things to know:

- `--part-type fat32` writes partition type `0x0c` instead of the default EFI
  system partition (`0xef`). Both boot on a normal BIOS, which goes by the
  active flag rather than the type byte, but `0x0c` is the conventional choice
  for a BIOS-only image and some firmware is fussier than others.
- The partition has to be large enough to be a real FAT32 — more than 65524
  clusters, which in practice means `--size 33` or above. Below that, SYSLINUX
  applies the standard rule and reads the filesystem as FAT16, and the boot
  stops after printing its banner. fatimg refuses to build such an image rather
  than let you find out at boot time.

### List contents

```
List contents of the first partition in a disk image.

The disk image file can optionally be gzipped, in which case fatimg
will automatically decompress it to a temporary file.

Usage:
  fatimg ls [options] <disk-image>

Options:
  -long
    	like ls -l, show file size and mod timestamp
```

Example:
```bash
# List files with sizes and timestamps
$ fatimg ls -long disk.img
[167.4KB]  Mar 15 2024  /efi/boot/bootx64.efi
[133.7KB]  Mar 15 2024  /efi/boot/ldlinux.e64
[   204B]  Mar 15 2024  /efi/boot/syslinux.cfg
[ 27.3MB]  Mar 15 2024  /system/initrd.img
[149.1MB]  Mar 15 2024  /system/rootfs.squashfs
```

### Copy/Extract files

```
Copy files from a disk image to a local directory.

Extracts all files from the first partition of the disk image to the
specified destination directory. The disk image file can optionally be
gzipped, in which case fatimg will automatically decompress it.

Usage:
  fatimg cp <disk-image> <dest-directory>

Options:
  -progress
    	show progress bar with transfer rate for large files
```

Example:
```bash
# Extract with progress information
$ fatimg cp -progress disk.img ./extracted/
Writing ./extracted/efi/boot/bootx64.efi (167.4 KB)
Writing ./extracted/system/initrd.img (27.3 MB)
initrd.img: 100.0% (845.2 MB/s, ~0s remaining)
Writing ./extracted/system/rootfs.squashfs (149.1 MB)
rootfs.squashfs: 100.0% (523.1 MB/s, ~0s remaining)
```

## License

fatimg is licensed under the [GNU General Public License, version 3 or
later](LICENSE).

It was previously Apache-2.0. The BIOS boot support in `boot.go` is derived
from [SYSLINUX](https://www.syslinux.org/) — `syslinux_patch()` and
`generate_extents()` in `libinstaller/syslxmod.c`, and `cleanup_adv()` in
`libinstaller/setadv.c`, copyright 1998-2008 H. Peter Anvin and 2009-2014 Intel
Corporation. Those are GPL-2.0-or-later, so the project moved to GPLv3 rather
than misrepresent what it is. Releases up to and including the last Apache-2.0
tag remain available under those terms.

SYSLINUX itself is not distributed with fatimg, in source or binary form. The
dependencies are all MIT or BSD licensed.

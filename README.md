# fatimg

Experimental utility to build an fat32 boot (EFI) partition in a disk image using the excellent go-diskfs library, 
instead of mtools or loopback mounts.

Assuming you have your linux kernel, initrd, and whatever other files you need you can point this program
at the folder with them and it will install them into a disk image with an EFI partition.
 
fatimg can create, extract, and list files on the first partition (which must be FAT32) of a disk image.

The disk image file can optionally be gzipped, in which case fatimg will automatically gunzip it to a tmp file.

WARNING: there will probably be breaking changes as I intend to extend it to support legacy boot images with MBR,
as well as GPT (so additional cli flags to specify what is now assumed defaults).

## Installation

```bash
go install github.com/jsando/fatimg@latest
```

Or download a pre-built binary from the [releases](https://github.com/jsando/fatimg/releases) page.

## Commands

### Create a disk image

```
Create a disk image with an EFI partition.

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
  -size int
    	partition size in megabytes (default 1024)
  -trim
    	trim disk image before compressing (truncate zero-filled sectors at the end)
```

Example:
```bash
# Create a compressed 512MB disk image with label "BOOT"
fatimg create --output boot.img.gz --size 512 --label BOOT ./EFI/
```

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


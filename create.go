// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2023-2026 Jason Sando

package main

import (
	"flag"
	"fmt"
	"github.com/diskfs/go-diskfs"
	"github.com/diskfs/go-diskfs/disk"
	"github.com/diskfs/go-diskfs/filesystem"
	"github.com/diskfs/go-diskfs/partition/mbr"
	"github.com/dustin/go-humanize"
	gzip "github.com/klauspost/pgzip"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ldlinuxSysPath is where the SYSLINUX core lives in the image. It has to be in
// the root, and it is the one file `cp` will not overwrite.
const ldlinuxSysPath = "/ldlinux.sys"

type CreateCommand struct {
	fs              *flag.FlagSet
	overwriteOutput bool
	gzipOutput      bool
	label           string
	outputPath      string
	partitionMB     int
	trimImage       bool
	syslinuxBoot    bool
	syslinuxDir     string
	partType        string
	syslinux        *syslinuxFiles
	includes        []string
}

// Partition type names accepted by --part-type. EFI is the historical default;
// a BIOS-only image is more conventionally 0x0c, and some firmware refuses to
// boot an EFI system partition.
const (
	partTypeEFI   = "efi"
	partTypeFAT32 = "fat32"
)

func mbrPartitionType(name string) (mbr.Type, error) {
	switch name {
	case partTypeEFI:
		return mbr.EFISystem, nil
	case partTypeFAT32:
		return mbr.Fat32LBA, nil
	default:
		return 0, fmt.Errorf("unknown partition type %q, expected %q or %q", name, partTypeEFI, partTypeFAT32)
	}
}

func NewCreateCommand() *CreateCommand {
	cmd := &CreateCommand{
		fs:              flag.NewFlagSet("create", flag.ExitOnError),
		overwriteOutput: true,
	}
	cmd.fs.StringVar(&cmd.outputPath, "output", "", "output path (required)")
	cmd.fs.StringVar(&cmd.label, "label", "boot", "EFI partition volume label")
	cmd.fs.IntVar(&cmd.partitionMB, "size", 1024, "partition size in megabytes")
	cmd.fs.BoolVar(&cmd.gzipOutput, "gzip", false, "compress output file with gzip (automatic if output ends with '.gz')")
	cmd.fs.BoolVar(&cmd.trimImage, "trim", false, "trim disk image before compressing (truncate zero-filled sectors at the end)")
	cmd.fs.BoolVar(&cmd.syslinuxBoot, "syslinux", false, "make the image bootable by a legacy BIOS, using SYSLINUX")
	cmd.fs.StringVar(&cmd.syslinuxDir, "syslinux-dir", "", "directory holding the SYSLINUX release to install (required with --syslinux)")
	cmd.fs.StringVar(&cmd.partType, "part-type", partTypeEFI, fmt.Sprintf("MBR partition type, %q or %q", partTypeEFI, partTypeFAT32))
	cmd.fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `Create a disk image with a FAT32 partition.

The contents of the partition are specified as a list of one or more paths.
Folders are copied recursively, and include the folder name itself
unless it ends with a trailing '/'.

With --syslinux the image is also made bootable by a legacy BIOS. This
installs SYSLINUX, whose files are read from --syslinux-dir; they are not
bundled with fatimg because SYSLINUX is licensed under the GPL. Point it at
an unpacked syslinux release tarball, which ships the mbr.bin, ldlinux.bss,
ldlinux.sys and ldlinux.c32 an install needs; most distribution packages do
not, because their installer has them built in. You supply the syslinux.cfg
yourself, as one of the paths to copy in.

Usage:
  fatimg create [options] <path> [<path> ...]

Options:
`)
		cmd.fs.PrintDefaults()
		fmt.Fprintf(os.Stderr, `
Examples:
  fatimg create --output disk.img ./boot/
  fatimg create --output boot.img.gz --size 512 --label BOOT ./EFI/
  fatimg create --output disk.img --syslinux --syslinux-dir ~/syslinux-6.03 \
      --part-type fat32 ./boot/
`)
	}
	return cmd
}

func (c *CreateCommand) Name() string {
	return c.fs.Name()
}

func (c *CreateCommand) Run(args []string) error {
	// Check for help flag before parsing
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			c.fs.Usage()
			return nil
		}
	}

	err := c.fs.Parse(args)
	if err != nil {
		return err
	}
	// Must specify output path
	if c.outputPath == "" {
		c.fs.Usage()
		return fmt.Errorf("output path is required")
	}

	// Ensure at least one valid path is given as an argument
	if len(c.fs.Args()) == 0 {
		c.fs.Usage()
		return fmt.Errorf("at least one valid path is required")
	}
	c.includes = c.fs.Args()

	if _, err := mbrPartitionType(c.partType); err != nil {
		c.fs.Usage()
		return err
	}

	// SYSLINUX is GPL-licensed, so its files are not bundled; the user has
	// to point at a copy.
	if c.syslinuxBoot && c.syslinuxDir == "" {
		c.fs.Usage()
		return fmt.Errorf("--syslinux requires --syslinux-dir")
	}
	if c.syslinuxBoot {
		c.syslinux, err = loadSyslinuxFiles(c.syslinuxDir)
		if err != nil {
			return err
		}
	}

	// if outputPath ends with ".gz" then automatically turn on the doGzip flag
	if strings.HasSuffix(c.outputPath, ".gz") {
		c.gzipOutput = true
	}

	// Delete the output file if it exists already and -force was specified
	if _, err := os.Stat(c.outputPath); err == nil {
		if !c.overwriteOutput {
			return fmt.Errorf("output path '%s' exists, remove it or use --force to overwrite", c.outputPath)
		}
		os.Remove(c.outputPath)
	}

	// Generate a unique temporary file in the same folder as *outputPath
	tempFile, err := os.CreateTemp(filepath.Dir(c.outputPath), "disk.img.")
	if err != nil {
		return fmt.Errorf("error creating temporary file: %w", err)
	}
	tempFileName := tempFile.Name()
	tempFile.Close()
	os.Remove(tempFileName)

	// Any failure from here on leaves a partial image behind, so clean it up
	// rather than littering the output directory with disk.img.* files.
	defer func() {
		if err != nil {
			os.Remove(tempFileName)
		}
	}()

	// Package everything into an EFI partition
	if err = c.createDiskImage(tempFileName); err != nil {
		return fmt.Errorf("error creating disk image: %w", err)
	}

	// Truncate?
	if c.trimImage {
		fmt.Printf("Truncating disk image ... ")
		var trimSize int64
		trimSize, err = c.trimFile(tempFileName)
		if err != nil {
			return fmt.Errorf("error finding trimmed file size: %w", err)
		}
		if err = os.Truncate(tempFileName, trimSize); err != nil {
			return fmt.Errorf("error truncating disk image: %w", err)
		}
		fmt.Printf("truncated image to %s\n", humanize.Bytes(uint64(trimSize)))
	}

	// Optionally compress the output
	if c.gzipOutput {
		fmt.Fprintf(os.Stderr, "Compressing %s ... \n", c.outputPath)
		if err = c.compressOutput(tempFileName, c.outputPath); err != nil {
			return fmt.Errorf("error compressing output: %w", err)
		}
	} else {
		// just rename temp file to outputfile
		if err = os.Rename(tempFileName, c.outputPath); err != nil {
			return fmt.Errorf("error renaming disk image: %w", err)
		}
	}
	return nil
}

func (c *CreateCommand) trimFile(filePath string) (int64, error) {
	const chunkSize = 1048576

	file, err := os.Open(filePath)
	if err != nil {
		return -1, err
	}
	defer file.Close()

	// Get file size
	fileInfo, err := file.Stat()
	if err != nil {
		return -1, err
	}
	fileSize := fileInfo.Size()
	trimSize := fileSize

	// Read the file in reverse, in chunks
scanLoop:
	for offset := fileSize; offset > 0; offset -= chunkSize {
		// Calculate the size of the chunk to read
		readSize := int64(chunkSize)
		if offset < chunkSize {
			readSize = offset
		}

		// Move the offset back to read the chunk
		buf := make([]byte, readSize)
		_, err := file.ReadAt(buf, offset-readSize)
		if err != nil {
			return -1, err
		}

		// Scan the chunk from the end towards the beginning
		for i := len(buf) - 1; i >= 0; i-- {
			if buf[i] != 0 {
				trimSize = offset - readSize + int64(i) + 1
				break scanLoop
			}
		}
	}

	// If no non-zero byte is found, return -1
	return trimSize, nil
}

func (c *CreateCommand) compressOutput(inputFileName string, outputFileName string) error {
	if err := gzipFile(inputFileName, outputFileName); err != nil {
		return err
	}
	return os.Remove(inputFileName)
}

// gzipFile compresses inputFileName to outputFileName, leaving the input in
// place. `cp` uses it to put a gzipped image back together after writing to it.
func gzipFile(inputFileName string, outputFileName string) error {
	out, err := os.Create(outputFileName)
	if err != nil {
		return err
	}
	defer out.Close()
	reader, err := os.Open(inputFileName)
	if err != nil {
		return err
	}
	defer reader.Close()
	w := gzip.NewWriter(out)
	//w.SetConcurrency(100000, 10)
	_, err = io.Copy(w, reader)
	err2 := w.Close()
	if err != nil {
		return err
	}
	return err2
}

const MB = 1024 * 1024
const BlockSize = 512
const PartitionStart = 2048

func (c *CreateCommand) createDiskImage(tempFileName string) error {
	// Use int64 to avoid overflow on 32-bit systems
	espSize := int64(c.partitionMB) * MB
	diskSize := espSize + 4*MB
	partitionSectors := espSize / BlockSize
	//partitionEnd := partitionSectors - PartitionStart + 1

	// create raw disk image file
	myDisk, err := diskfs.Create(tempFileName, int64(diskSize), diskfs.SectorSizeDefault)
	if err != nil {
		return err
	}

	partType, err := mbrPartitionType(c.partType)
	if err != nil {
		return err
	}

	// create a partition table
	table := &mbr.Table{
		Partitions: []*mbr.Partition{
			{
				Start:    PartitionStart,
				Size:     uint32(partitionSectors), // Note: limits partition to ~2TB
				Type:     partType,
				Bootable: true,
			},
		},
	}
	err = myDisk.Partition(table)
	if err != nil {
		return err
	}

	spec := disk.FilesystemSpec{Partition: 1, FSType: filesystem.TypeFat32, VolumeLabel: c.label}
	fs, err := myDisk.CreateFilesystem(spec)
	if err != nil {
		return err
	}

	// Write the SYSLINUX files before anything else, so that ldlinux.sys
	// lands at the front of the data area in one piece. It is addressed by
	// a sector map with limited room for extents, so fragmenting it behind
	// a few hundred megabytes of payload is a real failure mode.
	if c.syslinuxBoot {
		if err = writeFileBytes(fs, ldlinuxSysPath, ldlinuxPayload(c.syslinux.ldlinux)); err != nil {
			return err
		}
		if err = writeFileBytes(fs, "/ldlinux.c32", c.syslinux.c32); err != nil {
			return err
		}
	}

	for _, include := range c.includes {
		paths, err := filepath.Glob(include)
		if err != nil {
			return err
		}
		for _, path := range paths {
			// If its a folder, copy it recursively including the folder name in the destination
			finfo, err := os.Stat(path)
			if err != nil {
				return fmt.Errorf("error accessing file '%s': %s\n", path, err.Error())
			}
			if finfo.IsDir() {
				prefix := filepath.Dir(path)
				err = copyDir(prefix, path, "/", fs)
				if err != nil {
					return err
				}
			} else {
				// If its a file, copy it straight over
				err = copyFile(path, "/"+filepath.Base(path), fs)
				if err != nil {
					return err
				}
			}
		}
	}
	if err = myDisk.Close(); err != nil {
		return err
	}

	// From here the image is just a file. go-diskfs leaves placeholders in
	// two BPB fields that are wrong for a filesystem inside a partition, so
	// correct them whether or not this image is going to be bootable.
	img, err := os.OpenFile(tempFileName, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	err = fixBPBGeometry(img, PartitionStart)
	if cerr := img.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}

	if c.syslinuxBoot {
		if err = installSyslinux(tempFileName, PartitionStart, c.syslinux); err != nil {
			return fmt.Errorf("error installing SYSLINUX: %w", err)
		}
	}
	return nil
}

// writeFileBytes writes an in-memory blob to a file in the image. Like
// copyFile it writes in a single call so that FAT32 allocates the cluster
// chain once.
func writeFileBytes(fs filesystem.FileSystem, dst string, data []byte) error {
	rw, err := fs.OpenFile(dst, os.O_CREATE|os.O_RDWR)
	if err != nil {
		return fmt.Errorf("error writing output file '%s': %s", dst, err.Error())
	}
	n, err := rw.Write(data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return fmt.Errorf("error writing output file '%s': %d bytes written, s/b %d", dst, n, len(data))
	}
	return rw.Close()
}

// reRootPath maps a source path to its destination inside the image by
// stripping prefix, which re-roots the folder at "/":
//
//	prefix=/a/b  path=/a/b/c    -->  /c
//	prefix=/a/b  path=/a/b/c/d  -->  /c/d
//
// A source folder given with a trailing slash has its own name stripped by the
// caller, leaving path == prefix; its contents are copied to the root.
func reRootPath(prefix string, path string) (string, error) {
	if !strings.HasPrefix(path, prefix) {
		return "", fmt.Errorf("path '%s' is not rooted in %s", path, prefix)
	}
	targetPath := path[len(prefix):]
	if targetPath == "" {
		targetPath = "/"
	}
	return targetPath, nil
}

// /a/b/c/ --> 			c/
// /a/b/c/d/ --> 		c/d/
// /a/b/c/f.txt -> 		c/f.txt
// this is re-rooting folder c to / from /a/b ... therefore the trick is to
// pass in the prefix to subtract. dstRoot is where the re-rooted tree lands in
// the image: "/" for create, or the destination folder for `cp`.
func copyDir(prefix string, path string, dstRoot string, fs filesystem.FileSystem) error {
	files, err := os.ReadDir(path)
	if err != nil {
		return err
	}

	targetPath, err := reRootPath(prefix, path)
	if err != nil {
		return err
	}
	targetPath = imagePath(dstRoot, targetPath)
	if err := fs.Mkdir(targetPath); err != nil {
		return fmt.Errorf("error creating directory '%s': %w", targetPath, err)
	}
	for _, file := range files {
		if file.IsDir() {
			err = copyDir(prefix, filepath.Join(path, file.Name()), dstRoot, fs)
		} else {
			err = copyFile(filepath.Join(path, file.Name()), imagePath(targetPath, file.Name()), fs)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// imagePath joins path elements for use inside the image, which uses forward
// slashes whatever the host does.
func imagePath(elem ...string) string {
	for i, e := range elem {
		elem[i] = filepath.ToSlash(e)
	}
	return path.Join(elem...)
}

func copyFile(src string, dst string, fs filesystem.FileSystem) error {
	// ldlinux.sys is located by a map of the sectors it occupies, stamped into
	// it at install time; rewriting it through the filesystem leaves an image
	// that still passes every check here and no longer boots. Overwriting one
	// that is already in the image is therefore refused. A user's own
	// ldlinux.sys copied into an image that has none is left alone.
	if strings.EqualFold(path.Clean(dst), ldlinuxSysPath) {
		if _, err := imageStat(fs, dst); err == nil {
			return fmt.Errorf("refusing to overwrite %s: it is the SYSLINUX bootloader, "+
				"which is located by a sector map written when it was installed; "+
				"rebuild the image with 'create --syslinux' instead", dst)
		}
	}
	file, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("error opening file '%s': %s\n", src, err.Error())
	}
	defer file.Close()
	// O_TRUNC because the destination may already hold a longer file, whose
	// tail would otherwise survive the copy.
	rw, err := fs.OpenFile(dst, os.O_CREATE|os.O_RDWR|os.O_TRUNC)
	if err != nil {
		return fmt.Errorf("error writing output file '%s': %s\n", dst, err.Error())
	}
	// Make the buffer the same size as the file so that the fat32 file can see how many clusters
	// to allocate, otherwise it will re-allocate the cluster chain size/len(buf) times.
	// See https://github.com/diskfs/go-diskfs/issues/130
	// For very large files (ie 512M) it takes several minutes, versus loading the entire file into ram takes 1s or less.
	// This is temporary until the above issue can be addressed better ie FileSystem.Truncate(name,size).
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("error getting file info for '%s': %s\n", dst, err.Error())
	}
	fmt.Printf("Copying %s -> %s (%d bytes)\n", src, dst, info.Size())
	buf, err := io.ReadAll(file)
	n, err := rw.Write(buf)
	if err != nil {
		return err
	}
	if int64(n) != info.Size() {
		return fmt.Errorf("error writing output file '%s': %d bytes written, s/b %d\n", dst, n, info.Size())
	}
	err = rw.Close()
	if err != nil {
		return err
	}
	return nil
}

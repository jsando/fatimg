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
	"path/filepath"
	"strings"
)

type CreateCommand struct {
	fs              *flag.FlagSet
	overwriteOutput bool
	gzipOutput      bool
	label           string
	outputPath      string
	partitionMB     int
	trimImage       bool
	includes        []string
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
	cmd.fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `Create a disk image with an EFI partition.

The contents of the partition are specified as a list of one or more paths.
Folders are copied recursively, and include the folder name itself
unless it ends with a trailing '/'.

Usage:
  fatimg create [options] <path> [<path> ...]

Options:
`)
		cmd.fs.PrintDefaults()
		fmt.Fprintf(os.Stderr, `
Examples:
  fatimg create --output disk.img ./boot/
  fatimg create --output boot.img.gz --size 512 --label BOOT ./EFI/
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
		fmt.Fprintf(os.Stderr, "Output path is required\n")
		c.fs.Usage()
		os.Exit(1)
	}

	// Ensure at least one valid path is given as an argument
	if len(c.fs.Args()) == 0 {
		fmt.Fprintf(os.Stderr, "At least one valid path is required\n")
		c.fs.Usage()
		os.Exit(1)
	}
	c.includes = c.fs.Args()

	// if outputPath ends with ".gz" then automatically turn on the doGzip flag
	if strings.HasSuffix(c.outputPath, ".gz") {
		c.gzipOutput = true
	}

	// Delete the output file if it exists already and -force was specified
	if _, err := os.Stat(c.outputPath); err == nil {
		if !c.overwriteOutput {
			fmt.Fprintf(os.Stderr, "Output path '%s' exists, remove it or use --force to overwrite\n", c.outputPath)
			os.Exit(1)
		}
		os.Remove(c.outputPath)
	}

	// Generate a unique temporary file in the same folder as *outputPath
	tempFile, err := os.CreateTemp(filepath.Dir(c.outputPath), "disk.img.")
	tempFileName := tempFile.Name()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating temporary file: %s\n", err.Error())
		os.Exit(1)
	}
	os.Remove(tempFileName)

	// Package everything into an EFI partition
	err = c.createDiskImage(tempFileName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating disk image: %s\n", err.Error())
		os.Exit(1)
	}

	// Truncate?
	if c.trimImage {
		fmt.Printf("Truncating disk image ... ")
		trimSize, err := c.trimFile(tempFileName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error finding trimmed file size: %s\n", err.Error())
			os.Exit(1)
		}
		err = os.Truncate(tempFileName, trimSize)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error truncating disk image: %s\n", err.Error())
			os.Exit(1)
		}
		fmt.Printf("truncated image to %s\n", humanize.Bytes(uint64(trimSize)))
	}

	// Optionally compress the output
	if c.gzipOutput {
		fmt.Fprintf(os.Stderr, "Compressing %s ... \n", c.outputPath)
		err = c.compressOutput(tempFileName, c.outputPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error compressing output: %s\n", err.Error())
			os.Exit(1)
		}
	} else {
		// just rename temp file to outputfile
		err = os.Rename(tempFileName, c.outputPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error renaming disk image: %s\n", err.Error())
			os.Exit(1)
		}
	}
	// todo return error, don't os.Exit everywhere
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
				trimSize = offset - readSize + int64(i)
				break scanLoop
			}
		}
	}

	// If no non-zero byte is found, return -1
	return trimSize, nil
}

func (c *CreateCommand) compressOutput(inputFileName string, outputFileName string) error {
	gzipFile, err := os.Create(outputFileName)
	if err != nil {
		panic(err)
	}
	reader, err := os.Open(inputFileName)
	defer reader.Close()
	w := gzip.NewWriter(gzipFile)
	//w.SetConcurrency(100000, 10)
	_, err = io.Copy(w, reader)
	err2 := w.Close()
	if err != nil {
		return err
	}
	err = os.Remove(inputFileName)
	if err != nil {
		return err
	}
	return err2
}

const MB = 1024 * 1024
const BlockSize = 512
const PartitionStart = 2048

func (c *CreateCommand) createDiskImage(tempFileName string) error {
	espSize := c.partitionMB * MB
	diskSize := espSize + 4*MB
	partitionSectors := espSize / BlockSize
	//partitionEnd := partitionSectors - PartitionStart + 1

	// create raw disk image file
	myDisk, err := diskfs.Create(tempFileName, int64(diskSize), diskfs.SectorSizeDefault)
	if err != nil {
		return err
	}

	// create a partition table
	table := &mbr.Table{
		Partitions: []*mbr.Partition{
			{
				Start:    PartitionStart,
				Size:     uint32(partitionSectors),
				Type:     mbr.EFISystem,
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
				err = copyDir(prefix, path, fs)
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
	err = myDisk.Close()
	return err
}

// /a/b/c/ --> 			c/
// /a/b/c/d/ --> 		c/d/
// /a/b/c/f.txt -> 		c/f.txt
// this is re-rooting folder c to / from /a/b ... therefore the trick is to
// pass in the prefix to subtract
func copyDir(prefix string, path string, fs filesystem.FileSystem) error {
	files, err := os.ReadDir(path)
	if err != nil {
		return err
	}

	if !strings.HasPrefix(path, prefix) {
		return fmt.Errorf("path '%s' is not rooted in %s", path, prefix)
	}
	targetPath := path[len(prefix):]
	fs.Mkdir(targetPath)
	for _, file := range files {
		if file.IsDir() {
			copyDir(prefix, filepath.Join(path, file.Name()), fs)
		} else {
			copyFile(filepath.Join(path, file.Name()), filepath.Join(targetPath, file.Name()), fs)
		}
	}
	return nil
}

func copyFile(src string, dst string, fs filesystem.FileSystem) error {
	file, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("error opening file '%s': %s\n", src, err.Error())
	}
	defer file.Close()
	rw, err := fs.OpenFile(dst, os.O_CREATE|os.O_RDWR)
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
	if n != int(info.Size()) {
		return fmt.Errorf("error writing output file '%s': %d bytes written, s/b %d\n", dst, n, info.Size())
	}
	err = rw.Close()
	if err != nil {
		return err
	}
	return nil
}

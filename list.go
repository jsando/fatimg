package main

import (
	"flag"
	"fmt"
	"github.com/diskfs/go-diskfs"
	"github.com/diskfs/go-diskfs/filesystem"
	"github.com/dustin/go-humanize"
	gzip "github.com/klauspost/pgzip"
	"io"
	"os"
	"path/filepath"
)

type ListCommand struct {
	fs        *flag.FlagSet
	imageFile string
	long      bool
}

func NewListCommand() *ListCommand {
	c := &ListCommand{
		fs: flag.NewFlagSet("ls", flag.ExitOnError),
	}
	c.fs.BoolVar(&c.long, "long", false, "like ls -l, show file size and mod timestamp")
	c.fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `List contents of the first partition in a disk image.

The disk image file can optionally be gzipped, in which case fatimg
will automatically decompress it to a temporary file.

Usage:
  fatimg ls [options] <disk-image>

Options:
`)
		c.fs.PrintDefaults()
		fmt.Fprintf(os.Stderr, `
Examples:
  fatimg ls disk.img
  fatimg ls -long boot.img.gz
`)
	}
	return c
}

func (c *ListCommand) Name() string {
	return c.fs.Name()
}

func (c *ListCommand) Run(args []string) error {
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
	if len(c.fs.Args()) != 1 {
		c.fs.Usage()
		return fmt.Errorf("expected path as last argument")
	}
	c.imageFile = c.fs.Arg(0)
	// if filename ends with ".gz", gunzip to a tempfile
	if filepath.Ext(c.imageFile) == ".gz" {
		tempFile, err := gunzipToTempFile(c.imageFile)
		if err != nil {
			return fmt.Errorf("could not gunzip file: %s", err)
		}
		c.imageFile = tempFile
		defer os.Remove(tempFile)
	}
	disk, err := diskfs.Open(c.imageFile)
	fs, err := disk.GetFilesystem(1)
	if err != nil {
		return fmt.Errorf("could not open filesystem: %s", err)
	}
	return c.listDir(fs, "/")
}

func gunzipToTempFile(filename string) (tempFilename string, err error) {
	tempFile, err := os.CreateTemp(os.TempDir(), "disk.img.")
	if err != nil {
		return "", err
	}
	defer tempFile.Close()
	fmt.Fprintf(os.Stderr, "uncompressing %s to temp file %s ...\n", filename, tempFile.Name())
	reader, err := os.Open(filename)
	if err != nil {
		return tempFile.Name(), err
	}
	gzipReader, err := gzip.NewReader(reader)
	defer gzipReader.Close()
	if err != nil {
		return tempFile.Name(), err
	}
	_, err = io.Copy(tempFile, gzipReader)
	return tempFile.Name(), err
}

func (c *ListCommand) listDir(fs filesystem.FileSystem, path string) error {
	files, err := fs.ReadDir(path)
	if err != nil {
		return err
	}
	for _, file := range files {
		if file.Name() == "." || file.Name() == ".." {
			continue
		}
		absPath := filepath.Join(path, file.Name())
		if c.long {
			info, err := file.Info()
			if err != nil {
				return err
			}
			// [4.0K  ] Dec 31 1979 EFI/
			fmt.Printf("[%6s]  %s  %s\n",
				humanize.Bytes(uint64(info.Size())),
				info.ModTime().Format("Jan _2 2006"),
				absPath)
		} else {
			fmt.Printf("%s\n", absPath)
		}
		if file.IsDir() {
			err = c.listDir(fs, absPath)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

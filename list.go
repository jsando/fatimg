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
	// if the image is gzipped, gunzip to a tempfile
	gzipped, err := isGzipped(c.imageFile)
	if err != nil {
		return fmt.Errorf("could not open disk image: %w", err)
	}
	if gzipped {
		tempFile, err := gunzipToTempFile(c.imageFile)
		if err != nil {
			return fmt.Errorf("could not gunzip file: %s", err)
		}
		c.imageFile = tempFile
		defer os.Remove(tempFile)
	}
	disk, err := diskfs.Open(c.imageFile)
	if err != nil {
		return fmt.Errorf("could not open disk image: %w", err)
	}
	fs, err := disk.GetFilesystem(1)
	if err != nil {
		return fmt.Errorf("could not open filesystem: %w", err)
	}
	return c.listDir(fs, "/")
}

// isGzipped reports whether the file begins with the gzip magic number.
// `create --gzip` compresses the output regardless of its file name, so
// sniffing the content is more reliable than looking for a ".gz" extension.
func isGzipped(filename string) (bool, error) {
	f, err := os.Open(filename)
	if err != nil {
		return false, err
	}
	defer f.Close()
	magic := make([]byte, 2)
	if _, err := io.ReadFull(f, magic); err != nil {
		// Too short to be a gzip stream; leave it to the disk image reader
		// to report what is actually wrong with it.
		return false, nil
	}
	return magic[0] == 0x1f && magic[1] == 0x8b, nil
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
	defer reader.Close()
	gzipReader, err := gzip.NewReader(reader)
	if err != nil {
		return tempFile.Name(), err
	}
	defer gzipReader.Close()
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
			// [4.0K  ] Dec 31 1979 EFI/
			fmt.Printf("[%6s]  %s  %s\n",
				humanize.Bytes(uint64(file.Size())),
				file.ModTime().Format("Jan _2 2006"),
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

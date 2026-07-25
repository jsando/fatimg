package main

import (
	"bufio"
	"flag"
	"fmt"
	"github.com/diskfs/go-diskfs"
	"github.com/diskfs/go-diskfs/filesystem"
	"io"
	"os"
	"path/filepath"
	"time"
)

type CopyCommand struct {
	fs        *flag.FlagSet
	imageFile string
	destDir   string
	progress  bool
}

func NewCopyCommand() *CopyCommand {
	c := &CopyCommand{
		fs: flag.NewFlagSet("cp", flag.ExitOnError),
	}
	c.fs.BoolVar(&c.progress, "progress", false, "show progress bar with transfer rate for large files")
	c.fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `Copy files from a disk image to a local directory.

Extracts all files from the first partition of the disk image to the
specified destination directory. The disk image file can optionally be
gzipped, in which case fatimg will automatically decompress it.

Usage:
  fatimg cp <disk-image> <dest-directory>

Options:
`)
		c.fs.PrintDefaults()
		fmt.Fprintf(os.Stderr, `
Examples:
  fatimg cp disk.img ./extracted/
  fatimg cp boot.img.gz /tmp/boot-contents/
`)
	}
	return c
}

func (c *CopyCommand) Name() string {
	return c.fs.Name()
}

func (c *CopyCommand) Run(args []string) error {
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
	if len(c.fs.Args()) != 2 {
		c.fs.Usage()
		return fmt.Errorf("expected <source> and <dest> arguments")
	}
	c.imageFile = c.fs.Arg(0)
	c.destDir = c.fs.Arg(1)

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

	// Ensure destDir exists
	err = os.MkdirAll(c.destDir, 0755)
	if err != nil {
		return fmt.Errorf("could not create destination directory: %s", err)
	}
	return c.copyFiles(fs, "/")
}

func (c *CopyCommand) copyFiles(fs filesystem.FileSystem, sourceDir string) error {
	files, err := fs.ReadDir(sourceDir)
	if err != nil {
		return err
	}
	for _, file := range files {
		if file.Name() == "." || file.Name() == ".." {
			continue
		}
		absPath := filepath.Join(sourceDir, file.Name())
		targetPath := filepath.Join(c.destDir, absPath)
		err = os.MkdirAll(filepath.Dir(targetPath), 0755)
		if err != nil {
			return fmt.Errorf("could not create directory for target path: %s", err)
		}
		if file.IsDir() {
			err = c.copyFiles(fs, absPath)
		} else {
			info, err := file.Info()
			if err != nil {
				return err
			}
			err = c.copyFile(fs, absPath, targetPath, info)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *CopyCommand) copyFile(fs filesystem.FileSystem, absPath string, targetPath string, fileInfo os.FileInfo) error {
	sourceFile, err := fs.OpenFile(absPath, os.O_RDONLY)
	if err != nil {
		return fmt.Errorf("could not open source file: %s", err)
	}
	defer sourceFile.Close()

	// Use the passed file info for size
	fileSize := fileInfo.Size()

	// Always print what we're writing
	fmt.Printf("Writing %s (%s)\n", targetPath, formatSize(fileSize))

	destFile, err := os.Create(targetPath)
	if err != nil {
		return fmt.Errorf("could not create destination file: %s", err)
	}
	defer destFile.Close()

	// Use buffered writer with large buffer (1MB) for better performance
	const bufferSize = 1024 * 1024 // 1MB buffer
	bufferedWriter := bufio.NewWriterSize(destFile, bufferSize)
	defer bufferedWriter.Flush()

	// Use io.CopyBuffer with a large buffer for better performance
	buffer := make([]byte, bufferSize)
	
	var pr *progressReader
	if c.progress && fileSize > 10*1024*1024 { // Show progress for files > 10MB if flag is set
		// Create a progress reader wrapper
		pr = &progressReader{
			reader:    sourceFile,
			size:      fileSize,
			startTime: time.Now(),
			lastPrint: time.Now(),
			path:      targetPath,
		}
		_, err = io.CopyBuffer(bufferedWriter, pr, buffer)
	} else {
		// Simple copy without progress
		_, err = io.CopyBuffer(bufferedWriter, sourceFile, buffer)
	}
	if err != nil {
		return fmt.Errorf("could not copy file: %s", err)
	}

	// Final flush
	err = bufferedWriter.Flush()
	if err != nil {
		return fmt.Errorf("could not flush buffer: %s", err)
	}

	// Print final progress and newline after completion if progress was shown
	if pr != nil {
		pr.printProgress() // Show 100% completion
		fmt.Println() // Move to next line after progress bar
	}

	return nil
}

// progressReader wraps an io.Reader and reports progress
type progressReader struct {
	reader    io.Reader
	size      int64
	read      int64
	startTime time.Time
	lastPrint time.Time
	path      string
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.reader.Read(p)
	if n > 0 {
		pr.read += int64(n)
		now := time.Now()
		// Update progress every second for large files
		if pr.size > 10*1024*1024 && now.Sub(pr.lastPrint) >= time.Second {
			pr.printProgress()
			pr.lastPrint = now
		}
	}
	return n, err
}

func (pr *progressReader) printProgress() {
	percent := float64(pr.read) / float64(pr.size) * 100
	elapsed := time.Since(pr.startTime).Seconds()
	speed := float64(pr.read) / elapsed / 1024 / 1024 // MB/s
	
	// Calculate remaining time, but show 0s when nearly complete
	var remaining float64
	if pr.read < pr.size && elapsed > 0 {
		remaining = float64(pr.size-pr.read) / (float64(pr.read) / elapsed)
	}
	
	// Clear the line with spaces to prevent artifacts
	fmt.Printf("\r%-80s", "") // Clear line
	fmt.Printf("\r%s: %.1f%% (%.1f MB/s, ~%.0fs remaining)", 
		filepath.Base(pr.path), percent, speed, remaining)
}

func formatSize(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(size)/float64(div), "KMGTPE"[exp])
}

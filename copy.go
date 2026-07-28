// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2023-2026 Jason Sando

package main

import (
	"bufio"
	"flag"
	"fmt"
	"github.com/diskfs/go-diskfs"
	"github.com/diskfs/go-diskfs/disk"
	"github.com/diskfs/go-diskfs/filesystem"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type CopyCommand struct {
	fs       *flag.FlagSet
	progress bool
}

func NewCopyCommand() *CopyCommand {
	c := &CopyCommand{
		fs: flag.NewFlagSet("cp", flag.ExitOnError),
	}
	c.fs.BoolVar(&c.progress, "progress", false, "show progress bar with transfer rate for large files")
	c.fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `Copy files into or out of a disk image.

A path inside the image is written as <disk-image>:<path>, like scp, and has to
be absolute: disk.img:/EFI/BOOT. Whichever side of the command line carries one
decides the direction; the other side is the local filesystem. Sources may not
span both sides.

Folders are copied recursively, and include the folder name itself unless the
source ends with a trailing '/', the same rule 'create' uses. When the
destination is an existing directory, or is written with a trailing separator,
or there is more than one source, files are copied into it under their own
names; otherwise the destination names the copy.

The disk image file can optionally be gzipped, in which case fatimg will
decompress it, and -- if anything was written -- compress it again afterwards.

Usage:
  fatimg cp [options] <source> [<source> ...] <dest>
  fatimg cp [options] <disk-image> <dest-directory>

The second form extracts the whole image, and is what fatimg cp has always
done.

Options:
`)
		c.fs.PrintDefaults()
		fmt.Fprintf(os.Stderr, `
Examples:
  fatimg cp disk.img ./extracted/                     # extract everything
  fatimg cp disk.img:/EFI/BOOT/syslinux.cfg ./        # extract one file
  fatimg cp disk.img:/EFI ./out/                      # extract a folder
  fatimg cp syslinux.cfg disk.img:/EFI/BOOT/          # replace one file
  fatimg cp vmlinuz initrd.img boot.img.gz:/          # add files to an image
  fatimg cp ./boot/ disk.img:/                        # add a folder's contents
`)
	}
	return c
}

func (c *CopyCommand) Name() string {
	return c.fs.Name()
}

// imageRef is an argument of the form "disk.img:/path", naming a path inside a
// disk image rather than on the local filesystem.
type imageRef struct {
	image string
	path  string // absolute within the image, cleaned; "/" is the root
	// dirOnly records a trailing slash on the source, which means "the
	// contents of this folder" rather than the folder itself.
	dirOnly bool
}

func (r imageRef) String() string {
	return r.image + ":" + r.path
}

// parseImageRef splits "image:path" into the image file and the path inside it.
//
// The image is a path in its own right and may contain colons and slashes, so
// what marks the split is the path on the right: it has to be absolute within
// the image, or empty for the root. That leaves an ordinary local path
// containing a colon -- ./notes:draft, or /tmp/a:b/c -- alone, and it is a rule
// that can be stated in one line of help text. On Windows a single-letter image
// name is left alone too, so that C:/boot stays a local path.
func parseImageRef(arg string) (imageRef, bool) {
	i := len(arg)
	for {
		i = strings.LastIndexByte(arg[:i], ':')
		if i < 1 {
			return imageRef{}, false
		}
		if i == len(arg)-1 || arg[i+1] == '/' {
			break
		}
	}
	if i == 1 && runtime.GOOS == "windows" {
		return imageRef{}, false
	}
	ref := imageRef{image: arg[:i], path: arg[i+1:]}
	ref.dirOnly = ref.path == "" || strings.HasSuffix(ref.path, "/")
	ref.path = path.Clean("/" + ref.path)
	if ref.path == "/" {
		ref.dirOnly = true
	}
	return ref, true
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
	if len(c.fs.Args()) < 2 {
		c.fs.Usage()
		return fmt.Errorf("expected <source> and <dest> arguments")
	}
	rest := c.fs.Args()
	sources, dest := rest[:len(rest)-1], rest[len(rest)-1]

	destRef, destIsImage := parseImageRef(dest)
	var srcRefs []imageRef
	for _, src := range sources {
		if ref, ok := parseImageRef(src); ok {
			srcRefs = append(srcRefs, ref)
		}
	}

	switch {
	case destIsImage && len(srcRefs) > 0:
		return fmt.Errorf("cannot copy from one disk image to another")
	case destIsImage:
		return c.insert(sources, destRef)
	case len(srcRefs) == len(sources):
		for _, ref := range srcRefs[1:] {
			if ref.image != srcRefs[0].image {
				return fmt.Errorf("all sources must come from the same disk image, got %s and %s",
					srcRefs[0].image, ref.image)
			}
		}
		return c.extract(srcRefs, dest)
	case len(srcRefs) > 0:
		return fmt.Errorf("sources must all be inside the disk image or all outside it")
	case len(sources) == 1:
		// The original form: extract the whole image to a directory.
		return c.extract([]imageRef{{image: sources[0], path: "/", dirOnly: true}}, dest)
	default:
		c.fs.Usage()
		return fmt.Errorf("no disk image named; write a path inside the image as <disk-image>:<path>")
	}
}

// imageSession is an open disk image, hiding the fact that a gzipped one has to
// be decompressed to a temporary file first -- and, if it was written to,
// compressed again over the original afterwards.
type imageSession struct {
	fs       filesystem.FileSystem
	disk     *disk.Disk
	origPath string
	workPath string // == origPath unless the image was gzipped
	gzipped  bool
	writable bool
}

func openImage(imagePath string, writable bool) (*imageSession, error) {
	s := &imageSession{origPath: imagePath, workPath: imagePath, writable: writable}

	gzipped, err := isGzipped(imagePath)
	if err != nil {
		return nil, fmt.Errorf("could not open disk image: %w", err)
	}
	if gzipped {
		tempFile, err := gunzipToTempFile(imagePath)
		if err != nil {
			os.Remove(tempFile)
			return nil, fmt.Errorf("could not gunzip file: %w", err)
		}
		s.gzipped, s.workPath = true, tempFile
	}

	var opts []diskfs.OpenOpt
	if !writable {
		// Extracting must work from an image the user can only read.
		opts = append(opts, diskfs.WithOpenMode(diskfs.ReadOnly))
	}
	s.disk, err = diskfs.Open(s.workPath, opts...)
	if err != nil {
		s.cleanup()
		return nil, fmt.Errorf("could not open disk image: %w", err)
	}
	s.fs, err = s.disk.GetFilesystem(1)
	if err != nil {
		s.disk.Close()
		s.cleanup()
		return nil, fmt.Errorf("could not open filesystem: %w", err)
	}
	return s, nil
}

func (s *imageSession) cleanup() {
	if s.gzipped {
		os.Remove(s.workPath)
	}
}

// close releases the image. A gzipped image that was written to is compressed
// again over the original, unless commit is false. The replacement is built
// alongside the original and renamed into place, so a failure part way through
// cannot leave a half-written image where a good one was.
func (s *imageSession) close(commit bool) error {
	err := s.disk.Close()
	defer s.cleanup()
	if !s.gzipped || !s.writable || !commit || err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "Compressing %s ... \n", s.origPath)
	tempFile, err := os.CreateTemp(filepath.Dir(s.origPath), "disk.img.")
	if err != nil {
		return fmt.Errorf("error creating temporary file: %w", err)
	}
	tempName := tempFile.Name()
	tempFile.Close()
	if err := gzipFile(s.workPath, tempName); err != nil {
		os.Remove(tempName)
		return fmt.Errorf("error compressing image: %w", err)
	}
	if err := os.Rename(tempName, s.origPath); err != nil {
		os.Remove(tempName)
		return fmt.Errorf("error replacing image: %w", err)
	}
	return nil
}

// imageStat looks up one path inside the image. go-diskfs has no Stat, so the
// parent directory is listed and searched; the comparison is case-insensitive
// because FAT is.
func imageStat(fs filesystem.FileSystem, p string) (os.FileInfo, error) {
	dir, name := path.Split(path.Clean(p))
	entries, err := fs.ReadDir(path.Clean(dir))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	for _, entry := range entries {
		if strings.EqualFold(entry.Name(), name) {
			return entry, nil
		}
	}
	return nil, fmt.Errorf("%s: no such file or directory in the image", p)
}

// imageIsDir reports whether p exists in the image and is a directory.
func imageIsDir(fs filesystem.FileSystem, p string) bool {
	if path.Clean(p) == "/" {
		return true
	}
	info, err := imageStat(fs, p)
	return err == nil && info.IsDir()
}

// isLocalDir reports whether p is a directory on the local filesystem, or is
// written with a trailing separator and so is meant as one.
func isLocalDir(p string) bool {
	if strings.HasSuffix(p, "/") || strings.HasSuffix(p, string(filepath.Separator)) {
		return true
	}
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// extract copies paths out of an image onto the local filesystem.
func (c *CopyCommand) extract(refs []imageRef, dest string) error {
	session, err := openImage(refs[0].image, false)
	if err != nil {
		return err
	}
	defer session.close(false)

	// A destination that is already a directory, or is written as one, or has
	// more than one source aimed at it, holds the copies under their own
	// names; otherwise it is the name of the copy.
	container := len(refs) > 1 || isLocalDir(dest)
	for _, ref := range refs {
		if err := c.extractRef(session.fs, ref, dest, container); err != nil {
			return err
		}
	}
	return nil
}

func (c *CopyCommand) extractRef(fs filesystem.FileSystem, ref imageRef, dest string, container bool) error {
	if imageIsDir(fs, ref.path) {
		// A folder brings its own name unless the source was given with a
		// trailing slash, which re-roots its contents on the destination.
		target := dest
		if !ref.dirOnly {
			target = filepath.Join(dest, path.Base(ref.path))
		}
		if err := os.MkdirAll(target, 0755); err != nil {
			return fmt.Errorf("could not create destination directory: %s", err)
		}
		return c.extractDir(fs, ref.path, target)
	}
	if ref.dirOnly {
		return fmt.Errorf("%s: no such directory in the image", ref)
	}

	info, err := imageStat(fs, ref.path)
	if err != nil {
		return err
	}
	target := dest
	if container {
		target = filepath.Join(dest, info.Name())
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return fmt.Errorf("could not create directory for target path: %s", err)
	}
	return c.extractFile(fs, ref.path, target, info)
}

// extractDir copies everything under sourceDir in the image into destDir.
func (c *CopyCommand) extractDir(fs filesystem.FileSystem, sourceDir string, destDir string) error {
	files, err := fs.ReadDir(sourceDir)
	if err != nil {
		return err
	}
	for _, file := range files {
		if file.Name() == "." || file.Name() == ".." {
			continue
		}
		sourcePath := path.Join(sourceDir, file.Name())
		targetPath := filepath.Join(destDir, file.Name())
		if file.IsDir() {
			// Create the directory itself, not just its parent, so that
			// directories with no files in them are preserved.
			err = os.MkdirAll(targetPath, 0755)
		} else {
			err = os.MkdirAll(filepath.Dir(targetPath), 0755)
		}
		if err != nil {
			return fmt.Errorf("could not create directory for target path: %s", err)
		}
		if file.IsDir() {
			err = c.extractDir(fs, sourcePath, targetPath)
		} else {
			err = c.extractFile(fs, sourcePath, targetPath, file)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *CopyCommand) extractFile(fs filesystem.FileSystem, absPath string, targetPath string, fileInfo os.FileInfo) error {
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
		fmt.Println()      // Move to next line after progress bar
	}

	return nil
}

// insert copies local paths into an existing image.
func (c *CopyCommand) insert(sources []string, dest imageRef) error {
	// Resolve the sources before touching the image, so that a typo does not
	// leave a compressed image needlessly rewritten.
	paths, err := resolveSources(sources)
	if err != nil {
		return err
	}

	session, err := openImage(dest.image, true)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			session.close(false)
		}
	}()

	// dirOnly rather than a trailing separator: the path has been cleaned by
	// the time it gets here.
	container := dest.dirOnly || len(paths) > 1 || imageIsDir(session.fs, dest.path)
	for _, src := range paths {
		if err := c.insertPath(session.fs, src, dest, container); err != nil {
			return err
		}
	}
	committed = true
	return session.close(true)
}

// resolveSources expands the source arguments the way create does, but treats a
// pattern that matches nothing as an error rather than copying nothing.
func resolveSources(sources []string) ([]string, error) {
	var paths []string
	for _, source := range sources {
		matches, err := filepath.Glob(source)
		if err != nil {
			return nil, fmt.Errorf("bad pattern '%s': %w", source, err)
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("%s: no such file or directory", source)
		}
		// filepath.Glob cleans away a trailing separator, which is how a
		// folder says "copy my contents"; put it back.
		if len(matches) == 1 && strings.HasSuffix(source, string(filepath.Separator)) &&
			!strings.HasSuffix(matches[0], string(filepath.Separator)) {
			matches[0] += string(filepath.Separator)
		}
		paths = append(paths, matches...)
	}
	return paths, nil
}

func (c *CopyCommand) insertPath(fs filesystem.FileSystem, src string, dest imageRef, container bool) error {
	stat := src
	if trimmed := strings.TrimSuffix(src, string(filepath.Separator)); trimmed != "" {
		stat = trimmed
	}
	info, err := os.Stat(stat)
	if err != nil {
		return fmt.Errorf("error accessing file '%s': %s", src, err)
	}
	if info.IsDir() {
		// copyDir applies the same trailing-slash rule as create: the folder
		// brings its own name unless the source ends with a separator.
		return copyDir(filepath.Dir(src), src, dest.path, fs)
	}
	target := dest.path
	if container {
		target = path.Join(dest.path, filepath.Base(src))
	}
	if parent := path.Dir(target); parent != "/" {
		if err := fs.Mkdir(parent); err != nil {
			return fmt.Errorf("could not create directory '%s' in the image: %w", parent, err)
		}
	}
	return copyFile(src, target, fs)
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

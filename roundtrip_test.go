// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2023-2026 Jason Sando

package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// These tests drive the real subcommands end to end: build a source tree,
// `create` an image from it, `ls` it, `cp` it back out, and require that what
// comes out matches what went in.
//
// This is the tier that catches dependency upgrades which change API semantics
// without breaking compilation -- the go-diskfs v1.6 -> v1.9 bump left every
// subcommand broken while still building cleanly.

// fileSpec describes one file in a test fixture tree.
type fileSpec struct {
	path string // slash-separated, relative to the tree root
	size int
}

// content generates deterministic bytes for a file, derived from its path so
// that a misdirected copy is detected rather than passing on size alone.
func (f fileSpec) content() []byte {
	if f.size == 0 {
		return []byte{}
	}
	seed := []byte(f.path + "\x00")
	b := make([]byte, f.size)
	for i := range b {
		b[i] = seed[i%len(seed)] ^ byte(i*31)
	}
	return b
}

// writeTree materializes specs under root, creating parent directories.
func writeTree(t *testing.T, root string, specs []fileSpec, emptyDirs ...string) {
	t.Helper()
	for _, spec := range specs {
		full := filepath.Join(root, filepath.FromSlash(spec.path))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatalf("mkdir for %s: %v", spec.path, err)
		}
		if err := os.WriteFile(full, spec.content(), 0644); err != nil {
			t.Fatalf("write %s: %v", spec.path, err)
		}
	}
	for _, dir := range emptyDirs {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(dir)), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
}

// runCmd executes a fatimg subcommand in-process, silencing its output so test
// logs stay readable. Tests must not run in parallel: this swaps os.Stdout.
func runCmd(t *testing.T, args ...string) error {
	t.Helper()
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("opening %s: %v", os.DevNull, err)
	}
	defer devnull.Close()

	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = devnull, devnull
	defer func() { os.Stdout, os.Stderr = origOut, origErr }()

	return executeSubcommand(args)
}

// walkTree returns a sorted listing of every file and directory under root,
// as slash-separated paths relative to root. Files are recorded as
// "path (size)" so contents can be compared separately.
func walkTree(t *testing.T, root string) []string {
	t.Helper()
	var entries []string
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if info.IsDir() {
			entries = append(entries, rel+"/")
		} else {
			entries = append(entries, fmt.Sprintf("%s (%d)", rel, info.Size()))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	sort.Strings(entries)
	return entries
}

// requireTreesEqual asserts that two directory trees have identical structure
// and identical file contents.
func requireTreesEqual(t *testing.T, want, got string) {
	t.Helper()
	wantEntries, gotEntries := walkTree(t, want), walkTree(t, got)
	if strings.Join(wantEntries, "\n") != strings.Join(gotEntries, "\n") {
		t.Fatalf("tree mismatch\n--- want ---\n%s\n--- got ---\n%s",
			strings.Join(wantEntries, "\n"), strings.Join(gotEntries, "\n"))
	}
	for _, entry := range wantEntries {
		if strings.HasSuffix(entry, "/") {
			continue
		}
		rel := entry[:strings.LastIndex(entry, " (")]
		wantBytes, err := os.ReadFile(filepath.Join(want, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("reading expected %s: %v", rel, err)
		}
		gotBytes, err := os.ReadFile(filepath.Join(got, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("reading actual %s: %v", rel, err)
		}
		if !bytes.Equal(wantBytes, gotBytes) {
			t.Errorf("contents differ for %s (%d bytes expected, %d got)",
				rel, len(wantBytes), len(gotBytes))
		}
	}
}

// TestRoundTrip creates an image from a fixture tree and extracts it again,
// requiring the result to be identical across the option matrix.
func TestRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping image round-trip in short mode")
	}

	specs := []fileSpec{
		{"EFI/BOOT/bootx64.efi", 4096},
		{"EFI/BOOT/grubx64.efi", 100_000},
		{"EFI/BOOT/empty.cfg", 0},
		{"EFI/nested/deeply/file.txt", 17},
		{"README", 3},
		// Larger than one FAT32 cluster, to exercise cluster-chain allocation.
		{"kernel/vmlinuz", 2_000_000},
	}
	emptyDirs := []string{"EFI/empty"}

	tests := []struct {
		name       string
		outputName string
		extraArgs  []string
	}{
		{name: "plain", outputName: "disk.img"},
		{name: "gzip by extension", outputName: "disk.img.gz"},
		{name: "gzip by flag", outputName: "disk.img", extraArgs: []string{"--gzip"}},
		{name: "trimmed", outputName: "disk.img", extraArgs: []string{"--trim"}},
		{name: "trimmed and gzipped", outputName: "disk.img.gz", extraArgs: []string{"--trim"}},
		{name: "custom label", outputName: "disk.img", extraArgs: []string{"--label", "ESP"}},
		{name: "larger partition", outputName: "disk.img", extraArgs: []string{"--size", "128"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "src")
			writeTree(t, src, specs, emptyDirs...)

			image := filepath.Join(dir, tt.outputName)
			args := append([]string{"create", "--output", image, "--size", "256"}, tt.extraArgs...)
			// Everything under src/, re-rooted to the image root.
			args = append(args, src+string(filepath.Separator))
			if err := runCmd(t, args...); err != nil {
				t.Fatalf("create: %v", err)
			}

			// --gzip without a .gz extension still writes to the given path.
			if _, err := os.Stat(image); err != nil {
				t.Fatalf("expected image at %s: %v", image, err)
			}

			out := filepath.Join(dir, "out")
			if err := runCmd(t, "cp", image, out); err != nil {
				t.Fatalf("cp: %v", err)
			}
			requireTreesEqual(t, src, out)
		})
	}
}

// TestListMatchesSource checks that ls reports exactly the files that were put
// into the image, with the absolute-path output format users depend on.
func TestListMatchesSource(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping image round-trip in short mode")
	}

	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	writeTree(t, src, []fileSpec{
		{"EFI/BOOT/bootx64.efi", 512},
		{"EFI/BOOT/startup.nsh", 20},
	}, "EFI/empty")

	image := filepath.Join(dir, "disk.img")
	if err := runCmd(t, "create", "--output", image, "--size", "64", filepath.Join(src, "EFI")); err != nil {
		t.Fatalf("create: %v", err)
	}

	got := captureList(t, image, false)
	want := []string{
		"/EFI",
		"/EFI/BOOT",
		"/EFI/BOOT/bootx64.efi",
		"/EFI/BOOT/startup.nsh",
		"/EFI/empty",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("ls output mismatch\nwant:\n%s\ngot:\n%s",
			strings.Join(want, "\n"), strings.Join(got, "\n"))
	}

	// -long adds size and date columns but must list the same paths.
	longOut := captureList(t, image, true)
	if len(longOut) != len(want) {
		t.Errorf("ls -long listed %d entries, want %d:\n%s",
			len(longOut), len(want), strings.Join(longOut, "\n"))
	}
	for _, line := range longOut {
		if !strings.Contains(line, "/EFI") {
			t.Errorf("ls -long line missing path: %q", line)
		}
	}
}

// captureList runs `ls` against an image and returns its stdout lines.
func captureList(t *testing.T, image string, long bool) []string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}

	origOut := os.Stdout
	os.Stdout = w

	done := make(chan []byte, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r)
		done <- buf.Bytes()
	}()

	args := []string{"ls"}
	if long {
		args = append(args, "-long")
	}
	args = append(args, image)
	runErr := executeSubcommand(args)

	w.Close()
	os.Stdout = origOut
	out := <-done
	r.Close()

	if runErr != nil {
		t.Fatalf("ls: %v", runErr)
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// TestCreateFolderNameIncluded checks the documented difference between passing
// a folder and passing it with a trailing slash.
func TestCreateFolderNameIncluded(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping image round-trip in short mode")
	}

	specs := []fileSpec{{"EFI/BOOT/bootx64.efi", 64}}

	t.Run("without trailing slash includes folder name", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "src")
		writeTree(t, src, specs)

		image := filepath.Join(dir, "disk.img")
		if err := runCmd(t, "create", "--output", image, "--size", "64", filepath.Join(src, "EFI")); err != nil {
			t.Fatalf("create: %v", err)
		}
		got := captureList(t, image, false)
		if got[0] != "/EFI" {
			t.Errorf("expected folder name at root, got %q", got)
		}
	})

	t.Run("with trailing slash re-roots contents", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "src")
		writeTree(t, src, specs)

		image := filepath.Join(dir, "disk.img")
		efiDir := filepath.Join(src, "EFI") + string(filepath.Separator)
		if err := runCmd(t, "create", "--output", image, "--size", "64", efiDir); err != nil {
			t.Fatalf("create: %v", err)
		}
		got := captureList(t, image, false)
		if got[0] != "/BOOT" {
			t.Errorf("expected contents re-rooted, got %q", got)
		}
	})
}

// TestErrorsInsteadOfPanics covers the failure paths that previously either
// panicked on a nil disk or called os.Exit, both of which make the commands
// untestable and unfriendly to script.
func TestErrorsInsteadOfPanics(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "does-not-exist.img")

	tests := []struct {
		name string
		args []string
	}{
		{"ls on missing image", []string{"ls", missing}},
		{"cp from missing image", []string{"cp", missing, filepath.Join(dir, "out")}},
		{"create without output", []string{"create", dir}},
		{"create without inputs", []string{"create", "--output", filepath.Join(dir, "x.img")}},
		{"ls without image", []string{"ls"}},
		{"cp without destination", []string{"cp", missing}},
		{"unknown subcommand", []string{"nope"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A panic or an os.Exit here fails the test run outright, which is
			// the point: these must be ordinary errors.
			if err := runCmd(t, tt.args...); err == nil {
				t.Errorf("expected an error from %v, got nil", tt.args)
			}
		})
	}
}

// TestGzipImageIsReadable checks that a compressed image is transparently
// decompressed by ls and cp.
func TestGzipImageIsReadable(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping image round-trip in short mode")
	}

	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	writeTree(t, src, []fileSpec{{"EFI/BOOT/bootx64.efi", 2048}})

	image := filepath.Join(dir, "disk.img.gz")
	if err := runCmd(t, "create", "--output", image, "--size", "64", filepath.Join(src, "EFI")); err != nil {
		t.Fatalf("create: %v", err)
	}

	info, err := os.Stat(image)
	if err != nil {
		t.Fatalf("stat image: %v", err)
	}
	// A 64MB partition of mostly zeros must compress to far less than the
	// uncompressed size, confirming the output really is gzipped.
	if info.Size() > 4*1024*1024 {
		t.Errorf("gzipped image is %d bytes, expected it to be compressed", info.Size())
	}

	out := filepath.Join(dir, "out")
	if err := runCmd(t, "cp", image, out); err != nil {
		t.Fatalf("cp from gzipped image: %v", err)
	}
	requireTreesEqual(t, src, out)
}

// TestCopyReportsWriteFailures guards against the error-shadowing bug where
// copyFile's error was assigned to a shadowed variable and never checked, so a
// failed extraction reported success.
func TestCopyReportsWriteFailures(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping image round-trip in short mode")
	}

	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	writeTree(t, src, []fileSpec{{"EFI/BOOT/bootx64.efi", 1024}})

	image := filepath.Join(dir, "disk.img")
	if err := runCmd(t, "create", "--output", image, "--size", "64",
		filepath.Join(src, "EFI")); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Occupy the destination file path with a directory, so creating the
	// extracted file must fail.
	out := filepath.Join(dir, "out")
	blocked := filepath.Join(out, "EFI", "BOOT", "bootx64.efi")
	if err := os.MkdirAll(blocked, 0755); err != nil {
		t.Fatalf("preparing blocked path: %v", err)
	}

	if err := runCmd(t, "cp", image, out); err == nil {
		t.Error("cp reported success even though the destination file could not be written")
	}
}

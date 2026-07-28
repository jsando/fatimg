// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2023-2026 Jason Sando

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diskfs/go-diskfs"
)

// Tests for the bidirectional `cp`: copying files into an existing image as
// well as out of one. The insert direction is the only place fatimg writes to
// an image it did not just create, so these check both that the new files
// arrive intact and that what was already there survives.

func TestParseImageRef(t *testing.T) {
	tests := []struct {
		arg     string
		wantOK  bool
		image   string
		path    string
		dirOnly bool
	}{
		{arg: "disk.img:/EFI/BOOT", wantOK: true, image: "disk.img", path: "/EFI/BOOT"},
		{arg: "disk.img:/EFI/BOOT/", wantOK: true, image: "disk.img", path: "/EFI/BOOT", dirOnly: true},
		{arg: "disk.img:/", wantOK: true, image: "disk.img", path: "/", dirOnly: true},
		{arg: "disk.img:", wantOK: true, image: "disk.img", path: "/", dirOnly: true},
		// The image is itself a path, and may contain slashes and colons.
		{arg: "/tmp/build/boot.img.gz:/EFI/BOOT/grub.cfg", wantOK: true,
			image: "/tmp/build/boot.img.gz", path: "/EFI/BOOT/grub.cfg"},
		{arg: "/tmp/a:b/disk.img:/EFI", wantOK: true, image: "/tmp/a:b/disk.img", path: "/EFI"},
		// Plain local paths, including ones that contain a colon. A path
		// inside the image has to be absolute, which is what tells them apart.
		{arg: "disk.img", wantOK: false},
		{arg: "./out/", wantOK: false},
		{arg: "/tmp/a:b", wantOK: false},
		{arg: "./weird:name", wantOK: false},
		{arg: "disk.img:EFI/BOOT", wantOK: false},
		{arg: ":/EFI", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.arg, func(t *testing.T) {
			ref, ok := parseImageRef(tt.arg)
			if ok != tt.wantOK {
				t.Fatalf("parseImageRef(%q) ok = %v, want %v", tt.arg, ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if ref.image != tt.image || ref.path != tt.path || ref.dirOnly != tt.dirOnly {
				t.Errorf("parseImageRef(%q) = {image:%q path:%q dirOnly:%v}, want {image:%q path:%q dirOnly:%v}",
					tt.arg, ref.image, ref.path, ref.dirOnly, tt.image, tt.path, tt.dirOnly)
			}
		})
	}
}

// buildImage creates an image from the given fixture tree and returns the
// image path and the source directory it was built from.
func buildImage(t *testing.T, dir string, specs []fileSpec, createArgs ...string) string {
	t.Helper()
	src := filepath.Join(dir, "src")
	writeTree(t, src, specs)

	image := filepath.Join(dir, "disk.img")
	args := append([]string{"create", "--output", image, "--size", "64"}, createArgs...)
	args = append(args, src+string(filepath.Separator))
	if err := runCmd(t, args...); err != nil {
		t.Fatalf("create: %v", err)
	}
	return image
}

// readFromImage extracts the whole image and returns the contents of one file
// in it, so that a copy is checked through a separate code path from the one
// that wrote it.
func readFromImage(t *testing.T, image, pathInImage string) []byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), "extracted")
	if err := runCmd(t, "cp", image, out); err != nil {
		t.Fatalf("extracting %s: %v", image, err)
	}
	data, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(strings.TrimPrefix(pathInImage, "/"))))
	if err != nil {
		t.Fatalf("reading %s from image: %v", pathInImage, err)
	}
	return data
}

// TestCopyIntoImage covers the destination forms for copying local files in.
func TestCopyIntoImage(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping image round-trip in short mode")
	}

	existing := []fileSpec{
		{"EFI/BOOT/bootx64.efi", 4096},
		{"EFI/BOOT/syslinux.cfg", 8000},
	}

	tests := []struct {
		name string
		// dest is the image-side argument, with %s replaced by the image path.
		dest string
		// want maps a path in the image to the local source it should hold.
		want map[string]string
	}{
		{
			name: "into the root",
			dest: "%s:/",
			want: map[string]string{"/new.cfg": "new.cfg"},
		},
		{
			name: "into an existing folder given with a trailing slash",
			dest: "%s:/EFI/BOOT/",
			want: map[string]string{"/EFI/BOOT/new.cfg": "new.cfg"},
		},
		{
			name: "into an existing folder given without one",
			dest: "%s:/EFI/BOOT",
			want: map[string]string{"/EFI/BOOT/new.cfg": "new.cfg"},
		},
		{
			name: "renaming the copy",
			dest: "%s:/EFI/BOOT/renamed.cfg",
			want: map[string]string{"/EFI/BOOT/renamed.cfg": "new.cfg"},
		},
		{
			name: "creating the folders it needs",
			dest: "%s:/deeply/nested/dir/",
			want: map[string]string{"/deeply/nested/dir/new.cfg": "new.cfg"},
		},
		{
			name: "over an existing file",
			dest: "%s:/EFI/BOOT/syslinux.cfg",
			want: map[string]string{"/EFI/BOOT/syslinux.cfg": "new.cfg"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			image := buildImage(t, dir, existing)

			// The replacement is deliberately shorter than the file it
			// replaces in the overwrite case: without O_TRUNC the tail of the
			// old file survives and the copy reads back too long.
			local := filepath.Join(dir, "new.cfg")
			content := []byte("DEFAULT fatimg\n")
			if err := os.WriteFile(local, content, 0644); err != nil {
				t.Fatalf("writing source file: %v", err)
			}

			if err := runCmd(t, "cp", local, strings.ReplaceAll(tt.dest, "%s", image)); err != nil {
				t.Fatalf("cp into image: %v", err)
			}

			for inImage := range tt.want {
				got := readFromImage(t, image, inImage)
				if !bytes.Equal(got, content) {
					// Trimmed: a copy that failed to truncate reads back as
					// the whole of the file it replaced.
					t.Errorf("%s holds %d bytes starting %q, want %d bytes of %q",
						inImage, len(got), got[:min(len(got), 32)], len(content), content)
				}
			}

			// Whatever was already in the image and not overwritten must
			// still be there, byte for byte.
			for _, spec := range existing {
				if _, replaced := tt.want["/"+spec.path]; replaced {
					continue
				}
				if got := readFromImage(t, image, "/"+spec.path); !bytes.Equal(got, spec.content()) {
					t.Errorf("%s was disturbed: %d bytes, want %d", spec.path, len(got), spec.size)
				}
			}
		})
	}
}

// TestCopyMultipleFilesIntoImage checks that several sources land in the
// destination folder under their own names.
func TestCopyMultipleFilesIntoImage(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping image round-trip in short mode")
	}

	dir := t.TempDir()
	image := buildImage(t, dir, []fileSpec{{"EFI/BOOT/bootx64.efi", 512}})

	names := []string{"vmlinuz", "initrd.img"}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name+" contents"), 0644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}

	args := []string{"cp"}
	for _, name := range names {
		args = append(args, filepath.Join(dir, name))
	}
	args = append(args, image+":/boot")
	if err := runCmd(t, args...); err != nil {
		t.Fatalf("cp: %v", err)
	}

	for _, name := range names {
		got := readFromImage(t, image, "/boot/"+name)
		if string(got) != name+" contents" {
			t.Errorf("/boot/%s holds %q", name, got)
		}
	}
}

// TestCopyFolderIntoImage checks that a folder is copied recursively, and that
// the trailing-slash rule matches the one create documents.
func TestCopyFolderIntoImage(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping image round-trip in short mode")
	}

	added := []fileSpec{
		{"BOOT/bootx64.efi", 2048},
		{"BOOT/nested/grubx64.efi", 700},
	}

	tests := []struct {
		name     string
		trailing bool
		want     map[string]string // path in image -> path in the added tree
	}{
		{
			name:     "without a trailing slash the folder brings its name",
			trailing: false,
			want: map[string]string{
				"/dest/EFI/BOOT/bootx64.efi":        "BOOT/bootx64.efi",
				"/dest/EFI/BOOT/nested/grubx64.efi": "BOOT/nested/grubx64.efi",
			},
		},
		{
			name:     "with one its contents are re-rooted",
			trailing: true,
			want: map[string]string{
				"/dest/BOOT/bootx64.efi":        "BOOT/bootx64.efi",
				"/dest/BOOT/nested/grubx64.efi": "BOOT/nested/grubx64.efi",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			image := buildImage(t, dir, []fileSpec{{"README", 10}})

			tree := filepath.Join(dir, "add", "EFI")
			writeTree(t, tree, added)
			source := tree
			if tt.trailing {
				source += string(filepath.Separator)
			}

			if err := runCmd(t, "cp", source, image+":/dest"); err != nil {
				t.Fatalf("cp folder into image: %v", err)
			}

			for inImage, inTree := range tt.want {
				want, err := os.ReadFile(filepath.Join(tree, filepath.FromSlash(inTree)))
				if err != nil {
					t.Fatalf("reading fixture %s: %v", inTree, err)
				}
				if got := readFromImage(t, image, inImage); !bytes.Equal(got, want) {
					t.Errorf("%s: %d bytes, want %d", inImage, len(got), len(want))
				}
			}
		})
	}
}

// TestCopyOutOfImage covers the source forms for extracting named paths, the
// half of the command that used to be all of it.
func TestCopyOutOfImage(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping image round-trip in short mode")
	}

	specs := []fileSpec{
		{"EFI/BOOT/bootx64.efi", 4096},
		{"EFI/BOOT/syslinux.cfg", 100},
		{"EFI/nested/deep.txt", 33},
		{"README", 12},
	}

	tests := []struct {
		name string
		src  string // image-side argument, %s replaced by the image path
		dest string // relative to the test's temp dir; a trailing / is kept
		// want maps the extracted file, relative to the temp dir, to the
		// fixture path whose contents it should hold.
		want map[string]string
	}{
		{
			name: "one file into an existing folder",
			src:  "%s:/EFI/BOOT/syslinux.cfg",
			dest: "out/",
			want: map[string]string{"out/syslinux.cfg": "EFI/BOOT/syslinux.cfg"},
		},
		{
			name: "one file under a new name",
			src:  "%s:/EFI/BOOT/syslinux.cfg",
			dest: "copied.cfg",
			want: map[string]string{"copied.cfg": "EFI/BOOT/syslinux.cfg"},
		},
		{
			name: "a folder brings its own name",
			src:  "%s:/EFI/BOOT",
			dest: "out",
			want: map[string]string{
				"out/BOOT/bootx64.efi":  "EFI/BOOT/bootx64.efi",
				"out/BOOT/syslinux.cfg": "EFI/BOOT/syslinux.cfg",
			},
		},
		{
			name: "a trailing slash re-roots its contents",
			src:  "%s:/EFI/BOOT/",
			dest: "out",
			want: map[string]string{
				"out/bootx64.efi":  "EFI/BOOT/bootx64.efi",
				"out/syslinux.cfg": "EFI/BOOT/syslinux.cfg",
			},
		},
		{
			name: "the root re-roots the whole image",
			src:  "%s:/",
			dest: "out",
			want: map[string]string{
				"out/README":              "README",
				"out/EFI/nested/deep.txt": "EFI/nested/deep.txt",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			image := buildImage(t, dir, specs)
			src := filepath.Join(dir, "src")

			dest := filepath.Join(dir, filepath.FromSlash(tt.dest))
			if strings.HasSuffix(tt.dest, "/") {
				dest += string(filepath.Separator)
				if err := os.MkdirAll(dest, 0755); err != nil {
					t.Fatalf("preparing destination: %v", err)
				}
			}
			if err := runCmd(t, "cp", strings.ReplaceAll(tt.src, "%s", image), dest); err != nil {
				t.Fatalf("cp out of image: %v", err)
			}

			for extracted, fixture := range tt.want {
				want, err := os.ReadFile(filepath.Join(src, filepath.FromSlash(fixture)))
				if err != nil {
					t.Fatalf("reading fixture %s: %v", fixture, err)
				}
				got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(extracted)))
				if err != nil {
					t.Fatalf("reading extracted %s: %v", extracted, err)
				}
				if !bytes.Equal(got, want) {
					t.Errorf("%s: %d bytes, want %d", extracted, len(got), len(want))
				}
			}
		})
	}
}

// TestCopyMultipleFilesOutOfImage checks that several image paths can be
// extracted in one command.
func TestCopyMultipleFilesOutOfImage(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping image round-trip in short mode")
	}

	dir := t.TempDir()
	image := buildImage(t, dir, []fileSpec{
		{"EFI/BOOT/bootx64.efi", 1024},
		{"README", 20},
	})

	out := filepath.Join(dir, "out")
	if err := runCmd(t, "cp", image+":/README", image+":/EFI/BOOT/bootx64.efi", out); err != nil {
		t.Fatalf("cp: %v", err)
	}
	for _, name := range []string{"README", "bootx64.efi"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Errorf("expected %s in %s: %v", name, out, err)
		}
	}
}

// TestCopyIntoGzippedImage checks that a compressed image is decompressed,
// written to, and compressed again, leaving a file that is still gzipped and
// still holds everything it did before.
func TestCopyIntoGzippedImage(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping image round-trip in short mode")
	}

	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	writeTree(t, src, []fileSpec{{"EFI/BOOT/bootx64.efi", 4096}})

	image := filepath.Join(dir, "disk.img.gz")
	if err := runCmd(t, "create", "--output", image, "--size", "64",
		src+string(filepath.Separator)); err != nil {
		t.Fatalf("create: %v", err)
	}

	local := filepath.Join(dir, "syslinux.cfg")
	content := []byte("DEFAULT linux\n")
	if err := os.WriteFile(local, content, 0644); err != nil {
		t.Fatalf("writing source file: %v", err)
	}
	if err := runCmd(t, "cp", local, image+":/"); err != nil {
		t.Fatalf("cp into gzipped image: %v", err)
	}

	// Still compressed, or the next `create --gzip`-style consumer of the
	// file gets a surprise.
	gzipped, err := isGzipped(image)
	if err != nil {
		t.Fatalf("checking image: %v", err)
	}
	if !gzipped {
		t.Error("image is no longer gzipped after copying into it")
	}
	// No leftover temporary image next to the original.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading temp dir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "disk.img.") && entry.Name() != "disk.img.gz" {
			t.Errorf("left a temporary file behind: %s", entry.Name())
		}
	}

	if got := readFromImage(t, image, "/syslinux.cfg"); !bytes.Equal(got, content) {
		t.Errorf("/syslinux.cfg holds %q, want %q", got, content)
	}
	if got := readFromImage(t, image, "/EFI/BOOT/bootx64.efi"); len(got) != 4096 {
		t.Errorf("existing file is %d bytes after the copy, want 4096", len(got))
	}
}

// TestCopyIntoTrimmedImage copies into an image whose file is far shorter than
// the filesystem inside it says it is, which is what --trim leaves behind.
// Nothing grows the file first: go-diskfs allocates a cluster from the FAT and
// writes at its offset, and the write past the end of the file is what extends
// it. This checks that the data written beyond the old end reads back, and that
// the copy stays inside the partition rather than running off the end of it.
func TestCopyIntoTrimmedImage(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping image round-trip in short mode")
	}

	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	writeTree(t, src, []fileSpec{{"EFI/BOOT/bootx64.efi", 4096}})

	image := filepath.Join(dir, "disk.img")
	if err := runCmd(t, "create", "--output", image, "--size", "64", "--trim",
		src+string(filepath.Separator)); err != nil {
		t.Fatalf("create: %v", err)
	}

	const partitionBytes = PartitionStart*BlockSize + 64*MB
	info, err := os.Stat(image)
	if err != nil {
		t.Fatalf("stat image: %v", err)
	}
	trimmed := info.Size()
	// If --trim ever stops truncating, this test would silently become an
	// ordinary copy into a full-sized image and prove nothing.
	if trimmed >= partitionBytes/2 {
		t.Fatalf("--trim left a %d byte image, expected far less than the %d byte partition",
			trimmed, partitionBytes)
	}

	// Larger than what is left of the file, so most of it lands past the end
	// and the file has to grow to hold it.
	payload := fileSpec{"payload.bin", 2_000_000}.content()
	local := filepath.Join(dir, "payload.bin")
	if err := os.WriteFile(local, payload, 0644); err != nil {
		t.Fatalf("writing source file: %v", err)
	}
	if err := runCmd(t, "cp", local, image+":/"); err != nil {
		t.Fatalf("cp into trimmed image: %v", err)
	}

	if got := readFromImage(t, image, "/payload.bin"); !bytes.Equal(got, payload) {
		t.Errorf("/payload.bin reads back as %d bytes, want %d", len(got), len(payload))
	}
	if got := readFromImage(t, image, "/EFI/BOOT/bootx64.efi"); len(got) != 4096 {
		t.Errorf("existing file is %d bytes after the copy, want 4096", len(got))
	}

	info, err = os.Stat(image)
	if err != nil {
		t.Fatalf("stat image: %v", err)
	}
	if info.Size() <= trimmed {
		t.Errorf("image is still %d bytes after writing %d past its end", info.Size(), len(payload))
	}
	// The FAT bounds the allocation, so a copy can un-trim an image but must
	// never push it past the partition it lives in.
	if info.Size() > partitionBytes {
		t.Errorf("image grew to %d bytes, past the end of its %d byte partition",
			info.Size(), partitionBytes)
	}
}

// TestCopyRefusesToOverwriteLdlinux checks the guard that keeps a copy from
// quietly breaking a bootable image. ldlinux.sys is found by a map of the
// sectors it occupies, written when SYSLINUX was installed; a replacement
// written through the filesystem passes every check in this suite and does not
// boot.
func TestCopyRefusesToOverwriteLdlinux(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping image round-trip in short mode")
	}

	dir := t.TempDir()
	image := buildImage(t, dir, []fileSpec{{"README", 10}})

	// Put an ldlinux.sys in the image without needing a SYSLINUX release:
	// the guard is about the name and does not read the file.
	d, err := diskfs.Open(image)
	if err != nil {
		t.Fatalf("opening image: %v", err)
	}
	fs, err := d.GetFilesystem(1)
	if err != nil {
		t.Fatalf("opening filesystem: %v", err)
	}
	if err := writeFileBytes(fs, ldlinuxSysPath, make([]byte, 1024)); err != nil {
		t.Fatalf("writing %s: %v", ldlinuxSysPath, err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("closing image: %v", err)
	}

	local := filepath.Join(dir, "ldlinux.sys")
	if err := os.WriteFile(local, []byte("not the real thing"), 0644); err != nil {
		t.Fatalf("writing source file: %v", err)
	}

	err = runCmd(t, "cp", local, image+":/")
	if err == nil {
		t.Fatal("cp overwrote ldlinux.sys instead of refusing")
	}
	if !strings.Contains(err.Error(), "SYSLINUX") {
		t.Errorf("error does not explain why: %v", err)
	}

	// The original must be untouched, not truncated by a half-done copy.
	if got := readFromImage(t, image, ldlinuxSysPath); len(got) != 1024 {
		t.Errorf("%s is %d bytes after the refused copy, want 1024", ldlinuxSysPath, len(got))
	}
}

// TestCopyArgumentErrors covers the combinations that cannot be carried out,
// which must be reported rather than half-performed.
func TestCopyArgumentErrors(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping image round-trip in short mode")
	}

	dir := t.TempDir()
	image := buildImage(t, dir, []fileSpec{{"EFI/BOOT/bootx64.efi", 512}})
	local := filepath.Join(dir, "local.cfg")
	if err := os.WriteFile(local, []byte("x"), 0644); err != nil {
		t.Fatalf("writing source file: %v", err)
	}

	tests := []struct {
		name string
		args []string
	}{
		{"image on both sides", []string{"cp", image + ":/README", image + ":/copy"}},
		{"sources on both sides", []string{"cp", local, image + ":/README", filepath.Join(dir, "out")}},
		{"sources from two images", []string{"cp", image + ":/a", "other.img:/b", filepath.Join(dir, "out")}},
		{"no image named", []string{"cp", local, local, filepath.Join(dir, "out")}},
		{"local source does not exist", []string{"cp", filepath.Join(dir, "nope"), image + ":/"}},
		{"image source does not exist", []string{"cp", image + ":/nope.cfg", filepath.Join(dir, "out")}},
		{"image folder does not exist", []string{"cp", image + ":/nope/", filepath.Join(dir, "out")}},
		{"file used as a folder", []string{"cp", image + ":/EFI/BOOT/bootx64.efi/", filepath.Join(dir, "out")}},
		{"destination image does not exist", []string{"cp", local, filepath.Join(dir, "nope.img") + ":/"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := runCmd(t, tt.args...); err == nil {
				t.Errorf("expected an error from %v, got nil", tt.args)
			}
		})
	}
}

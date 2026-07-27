// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2023-2026 Jason Sando

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestTrimFile locks in the off-by-one fixed in #10: trimFile returns a size
// (a count of bytes to keep), not the offset of the last non-zero byte. An
// image trimmed one byte short reads back 0x00 for that byte and silently
// corrupts the last sector.
func TestTrimFile(t *testing.T) {
	const chunkSize = 1048576

	tests := []struct {
		name string
		data []byte
		want int64
	}{
		{"empty file", []byte{}, 0},
		{"all zeros", make([]byte, 1024), 1024},
		{"no trailing zeros", []byte{1, 2, 3, 4}, 4},
		{"single trailing zero", []byte{1, 2, 3, 0}, 3},
		{"many trailing zeros", append([]byte{1, 2, 3}, make([]byte, 5000)...), 3},
		{"single non-zero byte", []byte{7}, 1},
		{"leading zeros kept", []byte{0, 0, 9}, 3},
		{"non-zero at last byte of chunk", bufWithByteAt(chunkSize-1, chunkSize), chunkSize},
		{"non-zero at first byte of chunk", bufWithByteAt(0, chunkSize), 1},
		{"non-zero just past chunk boundary", bufWithByteAt(chunkSize, 2*chunkSize), chunkSize + 1},
		{"non-zero spanning two chunks", bufWithByteAt(chunkSize+12345, 3*chunkSize), chunkSize + 12346},
		{"size not a multiple of chunk", bufWithByteAt(1500000, 1572864), 1500001},
	}

	c := &CreateCommand{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "image.bin")
			if err := os.WriteFile(path, tt.data, 0644); err != nil {
				t.Fatalf("writing fixture: %v", err)
			}
			got, err := c.trimFile(path)
			if err != nil {
				t.Fatalf("trimFile: %v", err)
			}
			if got != tt.want {
				t.Fatalf("trimFile = %d, want %d", got, tt.want)
			}

			// Invariant: truncating to this size must never discard data.
			if !allZero(tt.data[got:]) {
				t.Errorf("truncating to %d would discard non-zero data", got)
			}
			// Invariant: the kept region must end on a non-zero byte, so we
			// are not keeping more than necessary. (An all-zero file has no
			// non-zero byte to land on and is left at its original size.)
			if got > 0 && !allZero(tt.data) && tt.data[got-1] == 0 {
				t.Errorf("trimFile kept %d bytes, but byte %d is zero", got, got-1)
			}
		})
	}
}

// bufWithByteAt returns a buffer of the given size that is all zeros except for
// a single non-zero byte at pos.
func bufWithByteAt(pos, size int) []byte {
	b := make([]byte, size)
	b[pos] = 0xAB
	return b
}

func allZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

// TestReRootPath covers the folder re-rooting rules: a source folder lands at
// the image root, and a trailing slash copies its contents to the root instead.
func TestReRootPath(t *testing.T) {
	tests := []struct {
		name    string
		prefix  string
		path    string
		want    string
		wantErr bool
	}{
		{name: "folder at root of source", prefix: "/a/b", path: "/a/b/c", want: "/c"},
		{name: "nested folder", prefix: "/a/b", path: "/a/b/c/d", want: "/c/d"},
		{name: "relative source", prefix: "src", path: "src/EFI", want: "/EFI"},
		{name: "trailing slash re-roots contents", prefix: "src/EFI", path: "src/EFI", want: "/"},
		{name: "path not under prefix", prefix: "/a/b", path: "/x/y", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := reRootPath(tt.prefix, tt.path)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("reRootPath(%q, %q) = %q, want error", tt.prefix, tt.path, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("reRootPath(%q, %q): %v", tt.prefix, tt.path, err)
			}
			if got != tt.want {
				t.Errorf("reRootPath(%q, %q) = %q, want %q", tt.prefix, tt.path, got, tt.want)
			}
		})
	}
}

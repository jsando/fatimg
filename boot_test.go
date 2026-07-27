// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2023-2026 Jason Sando

package main

import (
	"encoding/binary"
	"testing"
)

// Unit tests for the SYSLINUX patcher. These need no external tools: they
// check the arithmetic that decides where the boot sector will look for
// ldlinux.sys, which is the part with no visible failure mode short of
// booting the image.

// TestMakeADVIsConsistent checks the ADV against the rule adv_consistent()
// applies in libinstaller/setadv.c: the two magic numbers must be in place and
// the dwords from offset 4 to the tail must sum to ADV_MAGIC2.
func TestMakeADVIsConsistent(t *testing.T) {
	adv := makeADV()
	if len(adv) != 2*advSize {
		t.Fatalf("ADV is %d bytes, want %d", len(adv), 2*advSize)
	}

	for copyIndex := range 2 {
		p := adv[copyIndex*advSize : (copyIndex+1)*advSize]

		if got := binary.LittleEndian.Uint32(p[0:4]); got != advMagic1 {
			t.Errorf("copy %d: head magic is %#x, want %#x", copyIndex, got, advMagic1)
		}
		if got := binary.LittleEndian.Uint32(p[advSize-4:]); got != advMagic3 {
			t.Errorf("copy %d: tail magic is %#x, want %#x", copyIndex, got, advMagic3)
		}

		var csum uint32
		for i := 4; i < advSize-4; i += 4 {
			csum += binary.LittleEndian.Uint32(p[i : i+4])
		}
		if csum != advMagic2 {
			t.Errorf("copy %d: checksum is %#x, want %#x", copyIndex, csum, advMagic2)
		}
	}
}

// TestLdlinuxSectors covers the sector count, which includes the two ADV
// sectors and rounds a partial sector up.
func TestLdlinuxSectors(t *testing.T) {
	for _, tc := range []struct {
		length int
		want   int
	}{
		{1, 3},             // one byte still takes a whole sector
		{BlockSize, 3},     // exactly one sector
		{BlockSize + 1, 4}, // one byte into the second
		{2 * BlockSize, 4}, //
		{68599, 136},       // the ldlinux.sys shipped with syslinux 6.03
	} {
		if got := ldlinuxSectors(tc.length); got != tc.want {
			t.Errorf("ldlinuxSectors(%d) = %d, want %d", tc.length, got, tc.want)
		}
	}
}

// decodeExtents unpacks the extent list back into (lba, len) pairs.
func decodeExtents(t *testing.T, b []byte) [][2]uint64 {
	t.Helper()
	if len(b)%extentSize != 0 {
		t.Fatalf("extent list is %d bytes, not a multiple of %d", len(b), extentSize)
	}
	var out [][2]uint64
	for i := 0; i < len(b); i += extentSize {
		out = append(out, [2]uint64{
			binary.LittleEndian.Uint64(b[i : i+8]),
			uint64(binary.LittleEndian.Uint16(b[i+8 : i+10])),
		})
	}
	return out
}

func TestGenerateExtents(t *testing.T) {
	// One extent has to stay under the 64K a single BIOS transfer can move,
	// so a contiguous run is cut every 127 sectors however long it is.
	const maxExtentSectors = 127

	t.Run("a short contiguous run is one extent", func(t *testing.T) {
		var sectors []uint64
		for i := range uint64(100) {
			sectors = append(sectors, 1000+i)
		}
		got := decodeExtents(t, mustExtents(t, sectors, 192))
		assertExtents(t, got, [][2]uint64{{1000, 100}})
	})

	t.Run("a long contiguous run splits at the 64K transfer limit", func(t *testing.T) {
		var sectors []uint64
		for i := range uint64(200) {
			sectors = append(sectors, 1000+i)
		}
		got := decodeExtents(t, mustExtents(t, sectors, 192))
		assertExtents(t, got, [][2]uint64{
			{1000, maxExtentSectors},
			{1000 + maxExtentSectors, 200 - maxExtentSectors},
		})
	})

	t.Run("a gap starts a new extent", func(t *testing.T) {
		sectors := []uint64{10, 11, 12, 500, 501}
		got := decodeExtents(t, mustExtents(t, sectors, 192))
		assertExtents(t, got, [][2]uint64{{10, 3}, {500, 2}})
	})

	t.Run("fully fragmented", func(t *testing.T) {
		sectors := []uint64{10, 20, 30}
		got := decodeExtents(t, mustExtents(t, sectors, 192))
		assertExtents(t, got, [][2]uint64{{10, 1}, {20, 1}, {30, 1}})
	})

	t.Run("empty", func(t *testing.T) {
		if got := mustExtents(t, nil, 192); len(got) != 0 {
			t.Errorf("expected no extents, got %d bytes", len(got))
		}
	})

	// ldlinux.sys has room for a fixed number of extents. Overflowing it
	// would silently truncate the sector map and produce an image that
	// builds cleanly and does not boot.
	t.Run("too fragmented to fit", func(t *testing.T) {
		var sectors []uint64
		for i := range uint64(10) {
			sectors = append(sectors, i*2) // every one a separate extent
		}
		if _, err := generateExtents(sectors, 4); err == nil {
			t.Fatal("expected an error when the extents do not fit, got nil")
		}
	})
}

func mustExtents(t *testing.T, sectors []uint64, max int) []byte {
	t.Helper()
	b, err := generateExtents(sectors, max)
	if err != nil {
		t.Fatalf("generateExtents: %v", err)
	}
	return b
}

func assertExtents(t *testing.T, got, want [][2]uint64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d extents %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("extent %d is (lba=%d, len=%d), want (lba=%d, len=%d)",
				i, got[i][0], got[i][1], want[i][0], want[i][1])
		}
	}
}

// synthLdlinux builds a stand-in for ldlinux.sys with a patch area laid out
// the way a real one is, so the patcher can be tested without shipping a
// GPL binary in the repository.
type synthOffsets struct {
	patchArea, epa, secPtr, advPtr, dir int
	secPtrCount, dirLen                 int
	sect1Ptr0, sect1Ptr1                int
}

func synthLdlinux(t *testing.T) ([]byte, synthOffsets) {
	t.Helper()
	off := synthOffsets{
		patchArea:   24,
		secPtr:      512,
		secPtrCount: 192,
		epa:         5190,
		advPtr:      5848,
		dir:         5212,
		dirLen:      256,
		sect1Ptr0:   282,
		sect1Ptr1:   288,
	}
	img := make([]byte, 8192)

	// Fill with something non-zero so the checksum is not trivially right.
	for i := range img {
		img[i] = byte(i * 7)
	}

	binary.LittleEndian.PutUint32(img[off.patchArea:], ldlinuxMagic)
	binary.LittleEndian.PutUint16(img[off.patchArea+22:], uint16(off.epa))

	binary.LittleEndian.PutUint16(img[off.epa+0:], uint16(off.advPtr))
	binary.LittleEndian.PutUint16(img[off.epa+2:], uint16(off.dir))
	binary.LittleEndian.PutUint16(img[off.epa+4:], uint16(off.dirLen))
	binary.LittleEndian.PutUint16(img[off.epa+10:], uint16(off.secPtr))
	binary.LittleEndian.PutUint16(img[off.epa+12:], uint16(off.secPtrCount))
	binary.LittleEndian.PutUint16(img[off.epa+14:], uint16(off.sect1Ptr0))
	binary.LittleEndian.PutUint16(img[off.epa+16:], uint16(off.sect1Ptr1))
	return img, off
}

func TestFindPatchArea(t *testing.T) {
	img, off := synthLdlinux(t)
	pa, err := findPatchArea(img)
	if err != nil {
		t.Fatalf("findPatchArea: %v", err)
	}
	for _, c := range []struct {
		name      string
		got, want int
	}{
		{"offset", pa.offset, off.patchArea},
		{"epa", pa.epa, off.epa},
		{"advPtrOffset", pa.advPtrOffset, off.advPtr},
		{"dirOffset", pa.dirOffset, off.dir},
		{"dirLen", pa.dirLen, off.dirLen},
		{"secPtrOffset", pa.secPtrOffset, off.secPtr},
		{"secPtrCount", pa.secPtrCount, off.secPtrCount},
		{"sect1Ptr0", pa.sect1Ptr0, off.sect1Ptr0},
		{"sect1Ptr1", pa.sect1Ptr1, off.sect1Ptr1},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}

	t.Run("no magic", func(t *testing.T) {
		if _, err := findPatchArea(make([]byte, 4096)); err == nil {
			t.Fatal("expected an error for an image with no patch area")
		}
	})
}

// TestPatchLdlinux checks the two things ldlinux.sys verifies about itself at
// boot: that the boot sector was told where its first sector is, and that its
// dwords sum to the magic. A wrong checksum stops the boot dead, and there is
// no way to notice before then.
func TestPatchLdlinux(t *testing.T) {
	img, off := synthLdlinux(t)
	ldlinuxLen := len(img) - 2*advSize
	padded := make([]byte, len(img))
	copy(padded, img)

	nsect := ldlinuxSectors(ldlinuxLen)
	sectors := make([]uint64, nsect)
	for i := range sectors {
		sectors[i] = uint64(1000 + i)
	}

	bootSect := make([]byte, BlockSize)
	if err := patchLdlinux(padded, bootSect, ldlinuxLen, sectors); err != nil {
		t.Fatalf("patchLdlinux: %v", err)
	}

	// The boot sector loads the first sector itself.
	if got := binary.LittleEndian.Uint32(bootSect[off.sect1Ptr0:]); got != uint32(sectors[0]) {
		t.Errorf("boot sector holds first sector %d, want %d", got, sectors[0])
	}
	if got := binary.LittleEndian.Uint32(bootSect[off.sect1Ptr1:]); got != 0 {
		t.Errorf("high half of the first sector pointer is %d, want 0", got)
	}

	// The last two sectors are the ADVs.
	if got := binary.LittleEndian.Uint64(padded[off.advPtr:]); got != sectors[nsect-2] {
		t.Errorf("first ADV pointer is %d, want %d", got, sectors[nsect-2])
	}
	if got := binary.LittleEndian.Uint64(padded[off.advPtr+8:]); got != sectors[nsect-1] {
		t.Errorf("second ADV pointer is %d, want %d", got, sectors[nsect-1])
	}

	// Totals, excluding the ADVs.
	if got := binary.LittleEndian.Uint16(padded[off.patchArea+8:]); int(got) != nsect-2 {
		t.Errorf("data_sectors is %d, want %d", got, nsect-2)
	}
	if got := binary.LittleEndian.Uint16(padded[off.patchArea+10:]); got != 2 {
		t.Errorf("adv_sectors is %d, want 2", got)
	}

	// The self-check ldlinux.sys runs: summing its dwords yields the magic.
	var csum uint32
	for i := range ldlinuxLen >> 2 {
		csum += binary.LittleEndian.Uint32(padded[i*4 : i*4+4])
	}
	if csum != ldlinuxMagic {
		t.Errorf("dwords sum to %#x, want %#x", csum, uint32(ldlinuxMagic))
	}

	t.Run("too few sectors", func(t *testing.T) {
		img, _ := synthLdlinux(t)
		if err := patchLdlinux(img, make([]byte, BlockSize), ldlinuxLen, sectors[:2]); err == nil {
			t.Fatal("expected an error when the file is shorter than its image")
		}
	})
}

// TestClusterCount checks the FAT16/FAT32 boundary arithmetic, which is what
// makes a too-small partition unbootable.
func TestClusterCount(t *testing.T) {
	l := fatLayout{
		sectorsPerCluster: 1,
		reservedSectors:   32,
		fatCount:          2,
		sectorsPerFat:     511,
		totalSectors:      65536,
	}
	// A 32MB partition: the size that reads as FAT16 despite the BPB.
	if got := l.clusterCount(); got != 64482 {
		t.Errorf("clusterCount() = %d, want 64482", got)
	}
	if l.clusterCount() > fat32MinClusters {
		t.Error("a 32MB partition should not qualify as FAT32")
	}

	l.totalSectors = 2 * 65536 // 64MB
	if l.clusterCount() <= fat32MinClusters {
		t.Error("a 64MB partition should qualify as FAT32")
	}
}

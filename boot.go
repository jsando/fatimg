// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2023-2026 Jason Sando
//
// The SYSLINUX installation in this file is derived from syslinux-6.03,
// specifically syslinux_patch() and generate_extents() in
// libinstaller/syslxmod.c and cleanup_adv() in libinstaller/setadv.c:
//
//	Copyright 1998-2008 H. Peter Anvin - All Rights Reserved
//	Copyright 2009-2014 Intel Corporation; author H. Peter Anvin
//
// Those files are licensed under the GNU General Public License, version 2 or
// (at your option) any later version. fatimg takes the later option and is
// distributed under version 3; this is why the project is GPL rather than
// permissively licensed.

package main

// Legacy (BIOS) boot support, via SYSLINUX.
//
// Terminology, since this file is thick with it:
//
//   - LBA, Logical Block Address: a sector's number counting from the start
//     of something, rather than the old cylinder/head/sector coordinates.
//     "Absolute" LBAs count from the start of the disk; the sector numbers
//     SYSLINUX works in count from the start of the partition, and the two
//     differ by the partition's own starting LBA.
//   - MBR, Master Boot Record: sector 0 of the disk. Holds up to 446 bytes
//     of boot code, then the four-entry partition table, then 0x55AA.
//   - VBR, Volume Boot Record, a.k.a. the boot sector: the first sector of a
//     partition. On FAT it holds boot code wrapped around the BPB.
//   - BPB, BIOS Parameter Block: the block of geometry fields inside the VBR
//     that describes the filesystem -- sector size, cluster size, how many
//     FATs, where the root directory starts, and so on. Bytes 11 to 89 of
//     the sector on FAT32.
//   - FAT, File Allocation Table: the array that chains clusters together.
//     Entry N holds the number of the cluster that follows cluster N, or an
//     end-of-chain marker. Walking it is how you find a file's data.
//   - CHS, Cylinder/Head/Sector: the pre-LBA addressing scheme. Its geometry
//     fields survive in the BPB and some boot code still falls back to them.
//   - ADV, Auxiliary Data Vector: a 512-byte scratch area SYSLINUX keeps for
//     itself at the end of ldlinux.sys, for things like "boot this once on
//     the next reboot". Two copies, so an interrupted write cannot lose it.
//     We write empty ones; nothing here uses the feature.
//
// Making a FAT32 image bootable by a PC BIOS takes four things beyond the
// filesystem itself:
//
//  1. mbr.bin in the first 440 bytes of sector 0, to chainload the partition
//     marked active;
//  2. a BPB whose hidden-sector count is the partition's starting LBA -- the
//     SYSLINUX boot sector adds it to every partition-relative sector number
//     it reads, so a zero there sends it to the wrong end of the disk;
//  3. the SYSLINUX boot sector (ldlinux.bss) merged into the partition's
//     first sector, leaving the BPB in place;
//  4. ldlinux.sys on the filesystem, patched with the list of disk sectors it
//     occupies. 512 bytes of boot sector cannot walk a FAT chain, so the
//     sector map is stamped in at install time.
//
// Item 4 is the whole of the work, and this file follows syslinux_patch() in
// syslinux-6.03 libinstaller/syslxmod.c. The structure offsets below are a
// property of the ldlinux.sys build being installed, not of the format, which
// is why they are read out of the image rather than hardcoded, and why the
// files must come from one syslinux release rather than a mix.

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// ldlinuxMagic marks the patch area inside ldlinux.sys.
	ldlinuxMagic = 0x3eb202fe

	// ldlinuxLoadAddr is where the boot sector loads ldlinux.sys. Extents
	// are split so that no single one straddles a 64K boundary from here.
	ldlinuxLoadAddr = 0x8000

	// extentSize is sizeof(struct syslinux_extent): a 64-bit starting LBA
	// and a 16-bit count of how many sectors follow it, packed.
	extentSize = 10

	// advSize is the size of one ADV (Auxiliary Data Vector), SYSLINUX's
	// own scratch area. Two of them are appended to ldlinux.sys on disk.
	// The three magic numbers let SYSLINUX tell a good copy from a torn
	// one: two signatures and a value the contents must sum to.
	advSize   = 512
	advMagic1 = 0x5a2d2fa5 // head signature
	advMagic2 = 0xa3041767 // what the body must sum to
	advMagic3 = 0xdd28bf64 // tail signature

	// ldlinuxName is the 8.3 short name of ldlinux.sys as it appears in the
	// root directory: eight bytes of name and three of extension, space
	// padded, with no dot between them.
	ldlinuxName = "LDLINUX SYS"

	// mbrBootstrapSize is the space in the MBR before the disk signature
	// and the partition table, which is all the boot code gets.
	mbrBootstrapSize = 440

	// bpbHiddenSectorsOffset is the offset within the boot sector of the
	// BPB's 32-bit "hidden sectors" count -- the number of sectors before
	// this partition, i.e. its own starting LBA.
	bpbHiddenSectorsOffset = 0x1c
	// bpbGeometryOffset is the offset of the BPB's 16-bit CHS
	// sectors-per-track field, immediately followed by the head count.
	bpbGeometryOffset = 0x18
	// fatBackupBootSector is the sector where FAT32 keeps a copy of the
	// boot sector, and where go-diskfs writes one.
	fatBackupBootSector = 6

	// The two regions of a FAT32 boot sector (VBR) that belong to SYSLINUX
	// rather than to the filesystem: the jump instruction and OEM name at
	// the front, and the boot code after the BPB. The BPB between them
	// (bytes 11 to 89) describes the filesystem and must survive, as must
	// the 0x55AA signature in the last two bytes.
	// From FAT_bsHead/FAT_bsCode in libinstaller/syslxint.h.
	bsHeadStart, bsHeadEnd = 0, 11
	bsCodeStart, bsCodeEnd = 90, 510
)

// syslinuxFiles holds the pieces of a SYSLINUX release needed for a BIOS boot.
type syslinuxFiles struct {
	mbr      []byte // mbr.bin, the 440-byte MBR bootstrap
	bootSect []byte // ldlinux.bss, the 512-byte FAT boot sector template
	ldlinux  []byte // ldlinux.sys, the core loaded by the boot sector
	c32      []byte // ldlinux.c32, the module ldlinux.sys loads in turn
}

// syslinuxSearchDirs are the subdirectories of --syslinux-dir searched for
// each file. The list covers an unpacked syslinux tarball as well as the
// layouts the common distribution packages use.
var syslinuxSearchDirs = []string{
	".",
	"bios",
	"core",
	"mbr",
	"bios/core",
	"bios/mbr",
	"modules/bios",
	"bios/com32/elflink/ldlinux",
	"com32/elflink/ldlinux",
}

func findSyslinuxFile(dir, name string) ([]byte, error) {
	for _, sub := range syslinuxSearchDirs {
		b, err := os.ReadFile(filepath.Join(dir, sub, name))
		if err == nil {
			return b, nil
		}
		if !os.IsNotExist(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("%s not found under %s (searched %s)",
		name, dir, strings.Join(syslinuxSearchDirs, ", "))
}

// loadSyslinuxFiles reads the SYSLINUX files out of dir, which should be a
// syslinux release directory. They are read at run time rather than embedded
// so that fatimg ships as its own work: embedding them would put someone
// else's binaries inside ours, with their own version skew and redistribution
// obligations, for no gain over pointing at a release.
func loadSyslinuxFiles(dir string) (*syslinuxFiles, error) {
	s := &syslinuxFiles{}
	for _, f := range []struct {
		name string
		dest *[]byte
	}{
		{"mbr.bin", &s.mbr},
		{"ldlinux.bss", &s.bootSect},
		{"ldlinux.sys", &s.ldlinux},
		{"ldlinux.c32", &s.c32},
	} {
		b, err := findSyslinuxFile(dir, f.name)
		if err != nil {
			return nil, err
		}
		*f.dest = b
	}
	// mbr.bin has to stop short of the partition table at offset 446, and
	// the boot sector template has to be exactly one sector. Anything else
	// means the wrong file was picked up.
	if len(s.mbr) > mbrBootstrapSize {
		return nil, fmt.Errorf("mbr.bin is %d bytes, expected at most %d", len(s.mbr), mbrBootstrapSize)
	}
	if len(s.bootSect) != BlockSize {
		return nil, fmt.Errorf("ldlinux.bss is %d bytes, expected %d", len(s.bootSect), BlockSize)
	}
	if len(s.ldlinux) < BlockSize {
		return nil, fmt.Errorf("ldlinux.sys is %d bytes, which is too small to be valid", len(s.ldlinux))
	}
	return s, nil
}

// ldlinuxSectors is the number of sectors ldlinux.sys occupies on disk,
// including the two ADV sectors appended to it.
func ldlinuxSectors(ldlinuxLen int) int {
	return (ldlinuxLen+BlockSize-1)/BlockSize + 2
}

// ldlinuxPayload is the byte stream written to the filesystem as
// /ldlinux.sys: the image followed by two ADV sectors, padded out to a whole
// number of sectors so that the patched copy can be written back in place.
func ldlinuxPayload(ldlinux []byte) []byte {
	buf := make([]byte, ldlinuxSectors(len(ldlinux))*BlockSize)
	copy(buf, ldlinux)
	copy(buf[len(ldlinux):], makeADV())
	return buf
}

// makeADV builds the pair of empty ADVs (Auxiliary Data Vectors, SYSLINUX's
// own scratch area) that live at the end of ldlinux.sys, following
// syslinux_reset_adv() and cleanup_adv() in libinstaller/setadv.c.
//
// The layout is a head signature, a checksum chosen so the rest of the sector
// sums to a second magic, the data, and a tail signature. Empty is all we
// need: nothing fatimg does uses the features that store anything here. The
// two copies are identical, which is what SYSLINUX expects to find.
func makeADV() []byte {
	adv := make([]byte, 2*advSize)
	binary.LittleEndian.PutUint32(adv[0:4], advMagic1)
	csum := uint32(advMagic2)
	for i := 8; i < advSize-4; i += 4 {
		csum -= binary.LittleEndian.Uint32(adv[i : i+4])
	}
	binary.LittleEndian.PutUint32(adv[4:8], csum)
	binary.LittleEndian.PutUint32(adv[advSize-4:advSize], advMagic3)
	copy(adv[advSize:], adv[:advSize])
	return adv
}

// patchArea locates the patch area inside ldlinux.sys and reads the offsets
// of the extended patch area (EPA) out of it. The patch area is the struct
// SYSLINUX leaves in its own image for an installer to fill in; the EPA is a
// second struct it points at, holding the offsets of everything else that
// needs writing. All offsets are relative to the start of the image except
// sect1ptr0 and sect1ptr1, which are offsets into the boot sector.
type patchArea struct {
	offset int // of the patch area itself

	epa          int
	advPtrOffset int
	dirOffset    int
	dirLen       int
	secPtrOffset int
	secPtrCount  int
	sect1Ptr0    int
	sect1Ptr1    int
}

func findPatchArea(img []byte) (*patchArea, error) {
	p := -1
	for i := 0; i+4 <= len(img); i += 4 {
		if binary.LittleEndian.Uint32(img[i:i+4]) == ldlinuxMagic {
			p = i
			break
		}
	}
	if p < 0 {
		return nil, fmt.Errorf("no patch area found in ldlinux.sys (is it a SYSLINUX BIOS build?)")
	}
	// The patch area is 24 bytes; epaoffset is its last field.
	if p+24 > len(img) {
		return nil, fmt.Errorf("patch area at offset %d is truncated", p)
	}
	u16 := func(off int) int { return int(binary.LittleEndian.Uint16(img[off : off+2])) }

	epa := u16(p + 22)
	if epa+20 > len(img) {
		return nil, fmt.Errorf("extended patch area at offset %d is out of range", epa)
	}
	pa := &patchArea{
		offset:       p,
		epa:          epa,
		advPtrOffset: u16(epa + 0),
		dirOffset:    u16(epa + 2),
		dirLen:       u16(epa + 4),
		secPtrOffset: u16(epa + 10),
		secPtrCount:  u16(epa + 12),
		sect1Ptr0:    u16(epa + 14),
		sect1Ptr1:    u16(epa + 16),
	}

	// The offsets come from the file rather than from us, so check that
	// every region they describe is inside it before writing through them.
	// ldlinux.sys is whatever the user pointed --syslinux-dir at.
	for _, r := range []struct {
		name       string
		start, end int
	}{
		{"ADV pointers", pa.advPtrOffset, pa.advPtrOffset + 16},
		{"install directory", pa.dirOffset, pa.dirOffset + pa.dirLen},
		{"sector extents", pa.secPtrOffset, pa.secPtrOffset + pa.secPtrCount*extentSize},
	} {
		if r.start < 0 || r.end > len(img) {
			return nil, fmt.Errorf("%s at offset %d..%d lie outside ldlinux.sys (%d bytes)",
				r.name, r.start, r.end, len(img))
		}
	}
	// These index the boot sector, not the image.
	for _, r := range []struct {
		name   string
		offset int
	}{
		{"first sector pointer", pa.sect1Ptr0},
		{"first sector pointer (high)", pa.sect1Ptr1},
	} {
		if r.offset < 0 || r.offset+4 > BlockSize {
			return nil, fmt.Errorf("%s at offset %d lies outside the boot sector", r.name, r.offset)
		}
	}
	return pa, nil
}

// patchLdlinux stamps the sector map into ldlinux.sys and into the boot sector
// template, following syslinux_patch() in libinstaller/syslxmod.c. Both img
// and bootSect are modified in place. img is the padded payload from
// ldlinuxPayload; ldlinuxLen is the unpadded length of ldlinux.sys, which is
// what the totals and the checksum are computed over. sectors are
// filesystem-relative sector numbers, in file order.
func patchLdlinux(img, bootSect []byte, ldlinuxLen int, sectors []uint64) error {
	nsect := ldlinuxSectors(ldlinuxLen)
	if len(sectors) < nsect {
		return fmt.Errorf("ldlinux.sys occupies %d sectors, need %d", len(sectors), nsect)
	}
	pa, err := findPatchArea(img)
	if err != nil {
		return err
	}

	// The boot sector loads the first sector of ldlinux.sys itself, and
	// finds the rest through the extents below.
	binary.LittleEndian.PutUint32(bootSect[pa.sect1Ptr0:], uint32(sectors[0]))
	binary.LittleEndian.PutUint32(bootSect[pa.sect1Ptr1:], uint32(sectors[0]>>32))

	dwords := ldlinuxLen >> 2 // complete dwords, excluding the ADVs
	binary.LittleEndian.PutUint16(img[pa.offset+8:], uint16(nsect-2))
	binary.LittleEndian.PutUint16(img[pa.offset+10:], 2)
	binary.LittleEndian.PutUint32(img[pa.offset+12:], uint32(dwords))

	// Everything between the first sector and the two ADVs is described by
	// the extent list.
	extents, err := generateExtents(sectors[1:nsect-2], pa.secPtrCount)
	if err != nil {
		return err
	}
	clear(img[pa.secPtrOffset : pa.secPtrOffset+pa.secPtrCount*extentSize])
	copy(img[pa.secPtrOffset:], extents)

	binary.LittleEndian.PutUint64(img[pa.advPtrOffset:], sectors[nsect-2])
	binary.LittleEndian.PutUint64(img[pa.advPtrOffset+8:], sectors[nsect-1])

	// Install into the root directory. dirlen includes the NUL.
	if pa.dirLen < 2 {
		return fmt.Errorf("no room for the install directory in ldlinux.sys")
	}
	img[pa.dirOffset] = '/'
	img[pa.dirOffset+1] = 0

	// The checksum is negative: ldlinux.sys sums its own dwords at boot and
	// expects the magic back.
	binary.LittleEndian.PutUint32(img[pa.offset+16:], 0)
	csum := uint32(ldlinuxMagic)
	for i := range dwords {
		csum -= binary.LittleEndian.Uint32(img[i*4 : i*4+4])
	}
	binary.LittleEndian.PutUint32(img[pa.offset+16:], csum)
	return nil
}

// generateExtents packs a list of sector numbers into extents -- (starting
// LBA, how many sectors follow) pairs -- merging runs that are contiguous on
// disk, so that a file in one piece costs a couple of entries instead of one
// per sector. A run is broken when it would reach 64K, the most a single BIOS
// disk read can move, or when the address it loads to would cross a 64K
// boundary in real-mode segmented memory. Follows generate_extents() in
// syslxmod.c.
func generateExtents(sectors []uint64, maxExtents int) ([]byte, error) {
	out := make([]byte, 0, len(sectors)*extentSize)
	addr := uint32(ldlinuxLoadAddr)
	base := addr
	var lba uint64
	var length uint32

	emit := func() {
		var e [extentSize]byte
		binary.LittleEndian.PutUint64(e[0:8], lba)
		binary.LittleEndian.PutUint16(e[8:10], uint16(length))
		out = append(out, e[:]...)
	}

	for _, sect := range sectors {
		if length != 0 {
			xbytes := (length + 1) * BlockSize
			if sect == lba+uint64(length) && xbytes < 65536 &&
				(addr^(base+xbytes-1))&0xffff0000 == 0 {
				length++
				addr += BlockSize
				continue
			}
			emit()
		}
		base = addr
		lba = sect
		length = 1
		addr += BlockSize
	}
	if length != 0 {
		emit()
	}

	if n := len(out) / extentSize; n > maxExtents {
		return nil, fmt.Errorf("ldlinux.sys is too fragmented: %d extents, ldlinux.sys has room for %d", n, maxExtents)
	}
	return out, nil
}

// fatLayout is the part of the BPB needed to turn a cluster number into a
// sector number relative to the start of the partition.
//
// A FAT32 volume is laid out as: reserved sectors (the boot sector and its
// backup among them), then two copies of the FAT itself, then the data area
// carved into fixed-size clusters. Cluster numbering starts at 2, which is
// why converting one to a sector subtracts 2.
type fatLayout struct {
	sectorsPerCluster uint32
	reservedSectors   uint32
	fatCount          uint32
	sectorsPerFat     uint32
	rootCluster       uint32
	totalSectors      uint32
}

func (l fatLayout) firstDataSector() uint32 {
	return l.reservedSectors + l.fatCount*l.sectorsPerFat
}

func (l fatLayout) clusterSector(c uint32) uint32 {
	return l.firstDataSector() + (c-2)*l.sectorsPerCluster
}

// clusterCount is the number of data clusters in the filesystem, which is what
// decides whether a reader treats it as FAT16 or FAT32.
func (l fatLayout) clusterCount() uint32 {
	return (l.totalSectors - l.firstDataSector()) / l.sectorsPerCluster
}

// fat32MinClusters is the largest cluster count that still reads as FAT16.
// A filesystem at or below it is not really FAT32 no matter what its BPB says;
// see clusters <= 0xfff4 in syslinux core/fs/fat/fat.c.
const fat32MinClusters = 0xfff4

func readFATLayout(img *os.File, partOffset int64) (fatLayout, error) {
	bs := make([]byte, BlockSize)
	if _, err := img.ReadAt(bs, partOffset); err != nil {
		return fatLayout{}, fmt.Errorf("error reading FAT boot sector: %w", err)
	}
	if bps := binary.LittleEndian.Uint16(bs[11:13]); bps != BlockSize {
		return fatLayout{}, fmt.Errorf("unsupported FAT sector size %d", bps)
	}
	l := fatLayout{
		sectorsPerCluster: uint32(bs[13]),
		reservedSectors:   uint32(binary.LittleEndian.Uint16(bs[14:16])),
		fatCount:          uint32(bs[16]),
		sectorsPerFat:     binary.LittleEndian.Uint32(bs[36:40]),
		rootCluster:       binary.LittleEndian.Uint32(bs[44:48]),
		totalSectors:      binary.LittleEndian.Uint32(bs[32:36]),
	}
	if l.sectorsPerCluster == 0 || l.fatCount == 0 || l.sectorsPerFat == 0 || l.rootCluster < 2 {
		return fatLayout{}, fmt.Errorf("first partition does not hold a FAT32 filesystem")
	}
	return l, nil
}

// fatEOC (end of chain) is the first FAT entry value that marks the last
// cluster of a file rather than pointing at another one. Only the low 28 bits
// of a FAT32 entry are the cluster number; the top four are reserved.
const fatEOC = 0x0ffffff8

// maxChainClusters bounds the walk so that a corrupt FAT cannot loop forever.
const maxChainClusters = 1 << 20

// chain follows a FAT32 cluster chain from start.
func (l fatLayout) chain(img *os.File, partOffset int64, start uint32) ([]uint32, error) {
	fatOffset := partOffset + int64(l.reservedSectors)*BlockSize
	var out []uint32
	var buf [4]byte
	for c := start; c >= 2 && c < fatEOC; {
		out = append(out, c)
		if len(out) > maxChainClusters {
			return nil, fmt.Errorf("cluster chain from %d does not terminate", start)
		}
		if _, err := img.ReadAt(buf[:], fatOffset+int64(c)*4); err != nil {
			return nil, fmt.Errorf("error reading FAT: %w", err)
		}
		c = binary.LittleEndian.Uint32(buf[:]) & 0x0fffffff
	}
	return out, nil
}

// findRootEntry returns the first cluster of a file in the root directory,
// looked up by its 11-byte 8.3 short name.
//
// A directory is a list of 32-byte entries. Names too long or too mixed-case
// for 8.3 get extra "long file name" entries in front of the real one, which
// are skipped here: every file still has a short name, and that is what we
// match on.
func (l fatLayout) findRootEntry(img *os.File, partOffset int64, shortName string) (uint32, error) {
	clusters, err := l.chain(img, partOffset, l.rootCluster)
	if err != nil {
		return 0, err
	}
	buf := make([]byte, l.sectorsPerCluster*BlockSize)
	for _, c := range clusters {
		off := partOffset + int64(l.clusterSector(c))*BlockSize
		if _, err := img.ReadAt(buf, off); err != nil {
			return 0, fmt.Errorf("error reading root directory: %w", err)
		}
		for i := 0; i+32 <= len(buf); i += 32 {
			e := buf[i : i+32]
			switch {
			case e[0] == 0x00: // end of directory
				return 0, fmt.Errorf("%s not found in the root directory", shortName)
			case e[0] == 0xe5: // deleted
				continue
			case e[11] == 0x0f: // long file name fragment
				continue
			}
			if string(e[0:11]) == shortName {
				hi := uint32(binary.LittleEndian.Uint16(e[20:22]))
				lo := uint32(binary.LittleEndian.Uint16(e[26:28]))
				return hi<<16 | lo, nil
			}
		}
	}
	return 0, fmt.Errorf("%s not found in the root directory", shortName)
}

// ldlinuxSectorMap returns the first n filesystem-relative sectors of the
// already-written /ldlinux.sys.
func ldlinuxSectorMap(img *os.File, partOffset int64, l fatLayout, n int) ([]uint64, error) {
	start, err := l.findRootEntry(img, partOffset, ldlinuxName)
	if err != nil {
		return nil, err
	}
	clusters, err := l.chain(img, partOffset, start)
	if err != nil {
		return nil, err
	}
	sectors := make([]uint64, 0, n)
	for _, c := range clusters {
		first := l.clusterSector(c)
		for j := uint32(0); j < l.sectorsPerCluster && len(sectors) < n; j++ {
			sectors = append(sectors, uint64(first+j))
		}
		if len(sectors) == n {
			break
		}
	}
	if len(sectors) < n {
		return nil, fmt.Errorf("ldlinux.sys is %d sectors on disk, expected %d", len(sectors), n)
	}
	return sectors, nil
}

// fixBPBGeometry corrects the two BPB (BIOS Parameter Block) fields go-diskfs
// fills in with placeholders: the hidden-sector count, which it always writes
// as zero, and the CHS geometry, which it writes as 1/1 on the grounds that
// everything addresses by LBA these days.
//
// Both are wrong for a filesystem that lives inside a partition. Hidden
// sectors is meant to say how many sectors precede the partition, and boot
// code adds it to the partition-relative sector numbers it works in to get an
// address it can hand to the BIOS -- so a zero there means every read lands
// 1MB early, at the front of the disk.
//
// The backup boot sector is corrected too, so that it stays a copy.
func fixBPBGeometry(img *os.File, partStart int64) error {
	for _, sector := range []int64{0, fatBackupBootSector} {
		base := (partStart + sector) * BlockSize

		var hidden [4]byte
		binary.LittleEndian.PutUint32(hidden[:], uint32(partStart))
		if _, err := img.WriteAt(hidden[:], base+bpbHiddenSectorsOffset); err != nil {
			return fmt.Errorf("error writing hidden sector count: %w", err)
		}

		// 63 sectors per track, 255 heads: the conventional fiction, and
		// what a CHS fallback in a boot sector expects to see.
		var geom [4]byte
		binary.LittleEndian.PutUint16(geom[0:2], 63)
		binary.LittleEndian.PutUint16(geom[2:4], 255)
		if _, err := img.WriteAt(geom[:], base+bpbGeometryOffset); err != nil {
			return fmt.Errorf("error writing disk geometry: %w", err)
		}
	}
	return nil
}

// installSyslinux writes the SYSLINUX boot code into an image whose first
// partition is a FAT32 filesystem already holding /ldlinux.sys. It must run
// after the filesystem is closed and nothing else will move the file.
func installSyslinux(path string, partStart int64, s *syslinuxFiles) (err error) {
	img, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := img.Close(); err == nil {
			err = cerr
		}
	}()

	partOffset := partStart * BlockSize

	// The MBR bootstrap. mbr.Table.Write only touches bytes 446 onwards, so
	// writing here cannot disturb the partition table.
	if _, err := img.WriteAt(s.mbr, 0); err != nil {
		return fmt.Errorf("error writing MBR boot code: %w", err)
	}

	layout, err := readFATLayout(img, partOffset)
	if err != nil {
		return err
	}

	// go-diskfs will happily build a FAT32 with too few clusters to be one.
	// SYSLINUX applies the standard rule and reads such a filesystem as
	// FAT16, so the image gets as far as printing its banner and then
	// cannot find ldlinux.c32. Catch it here rather than at boot.
	if n := layout.clusterCount(); n <= fat32MinClusters {
		return fmt.Errorf("partition has %d clusters, which reads as FAT16; "+
			"FAT32 needs more than %d, so use --size 33 or larger", n, fat32MinClusters)
	}

	nsect := ldlinuxSectors(len(s.ldlinux))
	sectors, err := ldlinuxSectorMap(img, partOffset, layout, nsect)
	if err != nil {
		return err
	}

	// Patch a private copy of the boot sector template, then merge it into
	// the live boot sector: patchLdlinux writes the location of the first
	// sector of ldlinux.sys into the template.
	bootSect := make([]byte, BlockSize)
	copy(bootSect, s.bootSect)

	payload := ldlinuxPayload(s.ldlinux)
	if err := patchLdlinux(payload, bootSect, len(s.ldlinux), sectors); err != nil {
		return err
	}

	// Write the patched image back over the copy the filesystem holds.
	for i, sector := range sectors {
		off := partOffset + int64(sector)*BlockSize
		if _, err := img.WriteAt(payload[i*BlockSize:(i+1)*BlockSize], off); err != nil {
			return fmt.Errorf("error writing patched ldlinux.sys: %w", err)
		}
	}

	// Merge the boot code into the partition's boot sector, keeping the BPB
	// that describes the filesystem. The backup gets the same treatment so
	// that it remains identical to the primary; the SYSLINUX installer
	// leaves the backup stale, but there is no reason to copy that.
	live := make([]byte, BlockSize)
	if _, err := img.ReadAt(live, partOffset); err != nil {
		return fmt.Errorf("error reading FAT boot sector: %w", err)
	}
	copy(live[bsHeadStart:bsHeadEnd], bootSect[bsHeadStart:bsHeadEnd])
	copy(live[bsCodeStart:bsCodeEnd], bootSect[bsCodeStart:bsCodeEnd])

	for _, sector := range []int64{0, fatBackupBootSector} {
		if _, err := img.WriteAt(live, partOffset+sector*BlockSize); err != nil {
			return fmt.Errorf("error writing FAT boot sector: %w", err)
		}
	}
	return nil
}

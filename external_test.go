package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The round-trip tests prove fatimg agrees with itself, which would still pass
// if we wrote a corrupt-but-self-consistent filesystem. These tests validate
// the image with mtools and fsck.fat instead, independent FAT implementations.
//
// They skip when those tools are not installed, so they are advisory locally
// and enforced in CI where the packages are installed.

// partitionOffset is the byte offset of the FAT32 partition within the image,
// which external tools need in order to address it.
const partitionOffset = PartitionStart * BlockSize

// mtoolsImage formats an image path for mtools' "image@@offset" syntax.
func mtoolsImage(path string) string {
	return fmt.Sprintf("%s@@%d", path, partitionOffset)
}

// runTool runs an external tool, skipping the test if it is not installed.
func runTool(t *testing.T, name string, args ...string) (string, error) {
	t.Helper()
	bin, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s not installed, skipping external validation", name)
	}
	cmd := exec.Command(bin, args...)
	// mtools otherwise rejects our geometry with "Bad configuration".
	cmd.Env = append(os.Environ(), "MTOOLS_SKIP_CHECK=1")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// buildValidationImage creates an image of the given size with a known layout,
// including a nested directory so that '..' entries are exercised.
func buildValidationImage(t *testing.T, sizeMB int) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	writeTree(t, src, []fileSpec{
		{"EFI/BOOT/bootx64.efi", 8192},
		{"EFI/BOOT/startup.nsh", 42},
		{"EFI/BOOT/nested/grubx64.efi", 4096},
	})

	image := filepath.Join(dir, "disk.img")
	if err := runCmd(t, "create", "--output", image, "--size", strconv.Itoa(sizeMB),
		"--label", "TESTVOL", filepath.Join(src, "EFI")); err != nil {
		t.Fatalf("create: %v", err)
	}
	return image
}

// TestMtoolsCanListImage checks that an independent FAT implementation sees the
// same files we wrote.
func TestMtoolsCanListImage(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping external validation in short mode")
	}
	image := buildValidationImage(t, 64)

	out, err := runTool(t, "mdir", "-i", mtoolsImage(image), "-/", "::/")
	if err != nil {
		t.Fatalf("mdir failed: %v\n%s", err, out)
	}
	for _, want := range []string{"bootx64.efi", "startup.nsh", "grubx64.efi", "EFI", "BOOT"} {
		if !strings.Contains(out, want) {
			t.Errorf("mdir output missing %q:\n%s", want, out)
		}
	}
}

// TestMtoolsCanReadFileContents checks that file data, not just directory
// entries, is readable by an external tool.
func TestMtoolsCanReadFileContents(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping external validation in short mode")
	}
	image := buildValidationImage(t, 64)

	dest := filepath.Join(t.TempDir(), "extracted.efi")
	out, err := runTool(t, "mcopy", "-i", mtoolsImage(image),
		"::/EFI/BOOT/bootx64.efi", dest)
	if err != nil {
		t.Fatalf("mcopy failed: %v\n%s", err, out)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("reading extracted file: %v", err)
	}
	want := fileSpec{"EFI/BOOT/bootx64.efi", 8192}.content()
	if len(got) != len(want) {
		t.Fatalf("mcopy extracted %d bytes, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("extracted contents differ at byte %d", i)
		}
	}
}

// TestFsckAcceptsFilesystem runs a filesystem check over a range of partition
// sizes. go-diskfs v1.9.4 sized the FAT two entries short at 64MB and every
// 65MB after it, and wrote an invalid '..' entry in every subdirectory; both
// produced images that fsck.fat rejected. Those sizes are included here so a
// future dependency upgrade that reintroduces either defect fails loudly.
func TestFsckAcceptsFilesystem(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping external validation in short mode")
	}

	// 64, 129 and 194 are the sizes that triggered the FAT sizing defect.
	for _, sizeMB := range []int{33, 64, 129, 194} {
		t.Run(fmt.Sprintf("%dMB", sizeMB), func(t *testing.T) {
			image := buildValidationImage(t, sizeMB)

			// fsck.fat cannot address a partition inside an image, so extract
			// the partition to a standalone file first.
			partition := filepath.Join(t.TempDir(), "part.img")
			if err := extractPartition(image, partition); err != nil {
				t.Fatalf("extracting partition: %v", err)
			}

			// -n opens read-only and reports problems without repairing them.
			// It exits non-zero for anything it would have changed.
			out, err := runTool(t, "fsck.fat", "-n", partition)
			if err != nil {
				t.Fatalf("fsck.fat rejected a %dMB image: %v\n%s", sizeMB, err, out)
			}

			// Named guards for the two known regressions, in case fsck ever
			// downgrades either to a non-fatal notice.
			for _, bad := range []string{"clusters but only space for", "Invalid '..' entry"} {
				if strings.Contains(out, bad) {
					t.Errorf("fsck.fat reported %q at %dMB:\n%s", bad, sizeMB, out)
				}
			}
		})
	}
}

// extractPartition copies the FAT32 partition out of a disk image.
func extractPartition(image, dest string) error {
	in, err := os.Open(image)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}
	if info.Size() <= partitionOffset {
		return fmt.Errorf("image is %d bytes, smaller than the partition offset %d",
			info.Size(), partitionOffset)
	}

	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()

	buf := make([]byte, info.Size()-partitionOffset)
	if _, err := in.ReadAt(buf, partitionOffset); err != nil {
		return err
	}
	_, err = out.Write(buf)
	return err
}

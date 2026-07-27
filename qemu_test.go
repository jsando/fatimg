package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The fourth validation tier: boot the image on an emulated PC.
//
// Nothing below this tier can catch a broken bootloader install. An image with
// no boot code in it at all passes every round-trip test in this suite and is
// pronounced healthy by fsck.fat, because a boot sector is not part of the
// filesystem as far as a filesystem checker is concerned. The only way to know
// that the sector map stamped into ldlinux.sys points where the boot sector
// will look is to let a BIOS follow it.

// bootMarker is the label named by DEFAULT in the test's syslinux.cfg. It is
// not a real kernel, so SYSLINUX prints it and fails to load it -- which is
// all we need, since reaching that point means the whole chain worked.
const bootMarker = "fatimg-boot-marker"

// bootConfig is the syslinux.cfg written into the test image. SERIAL redirects
// SYSLINUX to the serial port, which is how the test observes it; CONSOLE 0
// stops it also writing to a VGA console nobody is reading.
const bootConfig = `SERIAL 0 115200
CONSOLE 0
PROMPT 0
TIMEOUT 50
DEFAULT ` + bootMarker + `
`

// syslinuxDir returns the SYSLINUX release to install from. Like the mtools
// and dosfstools requirements, a missing one fails rather than skips: a
// validation tier that quietly does not run looks exactly like a passing one.
func syslinuxDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("FATIMG_SYSLINUX_DIR")
	if dir == "" {
		t.Fatal("FATIMG_SYSLINUX_DIR is not set.\n" +
			"The boot test installs SYSLINUX from an unpacked release, which is not\n" +
			"bundled with fatimg because it is GPL-licensed. Run `make boot`, which\n" +
			"downloads one and sets this for you, or point it at your own copy.")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("FATIMG_SYSLINUX_DIR is set to %s, which is not readable: %v", dir, err)
	}
	return dir
}

func qemuBinary(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("qemu-system-x86_64")
	if err != nil {
		t.Fatal("qemu-system-x86_64 not found on PATH.\n" +
			"The boot test runs the image on an emulated PC; install QEMU:\n" +
			"  macOS:  brew install qemu\n" +
			"  Debian: sudo apt-get install qemu-system-x86\n" +
			"Or run `make unit` for the tests that do not need it.")
	}
	return bin
}

// TestBIOSBootInQEMU builds a bootable image and checks that SYSLINUX got far
// enough to read the syslinux.cfg we put in it. That covers the whole chain:
// the MBR bootstrap, the patched FAT boot sector, ldlinux.sys located through
// its sector map and passing its own checksum, and ldlinux.c32 found on the
// filesystem.
func TestBIOSBootInQEMU(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping boot test in short mode")
	}
	qemu := qemuBinary(t)
	syslinux := syslinuxDir(t)

	// Both partition types should boot: a BIOS goes by the active flag, not
	// the type byte. fat32 is the more conventional choice for a BIOS-only
	// image, efi is what fatimg has always written.
	for _, partType := range []string{partTypeFAT32, partTypeEFI} {
		t.Run(partType, func(t *testing.T) {
			dir := t.TempDir()

			src := filepath.Join(dir, "src")
			if err := os.MkdirAll(src, 0o755); err != nil {
				t.Fatalf("creating source tree: %v", err)
			}
			if err := os.WriteFile(filepath.Join(src, "syslinux.cfg"), []byte(bootConfig), 0o644); err != nil {
				t.Fatalf("writing syslinux.cfg: %v", err)
			}

			// The trailing slash copies the contents rather than the
			// directory, putting syslinux.cfg in the root where
			// SYSLINUX looks for it.
			image := filepath.Join(dir, "boot.img")
			if err := runCmd(t, "create", "--output", image, "--size", "64",
				"--syslinux", "--syslinux-dir", syslinux,
				"--part-type", partType, src+"/"); err != nil {
				t.Fatalf("create: %v", err)
			}

			out := bootImage(t, qemu, image, 90*time.Second)
			if !strings.Contains(out, "SYSLINUX") {
				t.Errorf("SYSLINUX never reached the serial console.\nSerial output:\n%s", out)
			}
			if !strings.Contains(out, bootMarker) {
				t.Errorf("SYSLINUX did not read our syslinux.cfg (no %q in output).\nSerial output:\n%s",
					bootMarker, out)
			}
		})
	}
}

// bootImage runs the image under QEMU until the marker appears on the serial
// port or the deadline passes, and returns whatever the serial port produced.
func bootImage(t *testing.T, qemu, image string, timeout time.Duration) string {
	t.Helper()

	serial := filepath.Join(t.TempDir(), "serial.txt")

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, qemu,
		"-display", "none", // no window; this runs in CI
		"-no-reboot", // stop rather than loop on boot failure
		"-m", "256",
		"-drive", "file="+image+",format=raw,if=ide",
		"-serial", "file:"+serial,
	)
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting qemu: %v", err)
	}

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	defer func() {
		cancel()
		<-exited
	}()

	readSerial := func() string {
		b, err := os.ReadFile(serial)
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("reading serial log: %v", err)
		}
		return string(b)
	}

	// QEMU has no reason to exit on its own -- SYSLINUX sits at its boot
	// prompt -- so poll the serial log and stop as soon as it says what we
	// are waiting for. If QEMU exits first it failed to start, and its
	// stderr says why; without this the test would just wait out the
	// timeout and report an empty serial log.
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-exited:
			exited <- err
			if out := readSerial(); strings.Contains(out, bootMarker) {
				return out
			}
			t.Fatalf("qemu exited before the image booted: %v\nqemu stderr:\n%s\nserial output:\n%s",
				err, stderr.String(), readSerial())
		case <-ctx.Done():
			return readSerial()
		case <-tick.C:
			if out := readSerial(); strings.Contains(out, bootMarker) {
				return out
			}
		}
	}
}

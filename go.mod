module github.com/jsando/fatimg

go 1.25.0

// Go 1.25.4 carries the fix for golang/go#69255: under qemu-user on an arm64
// host the amd64 runtime is handed addresses above 47 bits and dies with
// "taggedPointerPack invalid packing" (older Go: "lfstack.push"). Building
// with anything earlier ships a binary that cannot run in an emulated amd64
// container. See fatimg#9. Do not lower this.
toolchain go1.25.4

require (
	github.com/diskfs/go-diskfs v1.6.0
	github.com/dustin/go-humanize v1.0.1
	github.com/klauspost/pgzip v1.2.6
)

require (
	github.com/djherbis/times v1.6.0 // indirect
	github.com/elliotwutingfeng/asciiset v0.0.0-20260129054604-cfde2086bc57 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/klauspost/compress v1.18.5 // indirect
	github.com/pierrec/lz4/v4 v4.1.26 // indirect
	github.com/pkg/xattr v0.4.12 // indirect
	github.com/sirupsen/logrus v1.9.4 // indirect
	github.com/ulikunitz/xz v0.5.15 // indirect
	golang.org/x/sys v0.43.0 // indirect
)

BIN        := fatimg
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE ?= $(shell date -u +"%Y-%m-%d %H:%M:%S UTC")
LDFLAGS    := -X 'main.version=$(VERSION)' -X 'main.commit=$(COMMIT)' -X 'main.buildDate=$(BUILD_DATE)'

# External FAT implementations the tests validate our images against, so that
# a self-consistently wrong filesystem cannot pass, plus the emulator the boot
# test runs images on.
REQUIRED_TOOLS := mdir mcopy fsck.fat qemu-system-x86_64

# The SYSLINUX release the boot test installs into an image. It is downloaded
# rather than vendored so that we do not redistribute someone else's binaries.
SYSLINUX_VERSION := 6.03
SYSLINUX_CACHE   := .syslinux
SYSLINUX_DIR     := $(SYSLINUX_CACHE)/syslinux-$(SYSLINUX_VERSION)
SYSLINUX_URL     := https://mirrors.edge.kernel.org/pub/linux/utils/boot/syslinux/syslinux-$(SYSLINUX_VERSION).tar.xz

export FATIMG_SYSLINUX_DIR = $(abspath $(SYSLINUX_DIR))

.PHONY: help all build test unit integration boot syslinux tools-check vet fmt fmt-check check clean

help: ## Show this help
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | awk -F':.*?## ' '{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

all: check build ## Verify and build

build: ## Build the binary with version info
	go build -ldflags "$(LDFLAGS)" -o $(BIN) .

# -count=1 disables the test cache. The external validation tests decide at
# run time whether mtools and fsck.fat are installed, and Go's cache key does
# not change when a tool appears in a directory already on PATH -- so a cached
# run happily replays "skipping, not installed" after you have installed them.
test: tools-check syslinux ## Run all tests, including image round-trips and the boot test
	go test -count=1 ./...

unit: ## Run only fast tests (no external tools needed)
	go test -short ./...

integration: tools-check syslinux ## Run image round-trip and external validation tests verbosely
	go test -count=1 -v -run 'TestRoundTrip|TestList|TestCreate|TestGzip|TestMtools|TestFsck' ./...

boot: tools-check syslinux ## Boot a --syslinux image under QEMU
	go test -count=1 -v -run TestBIOSBoot ./...

syslinux: $(SYSLINUX_DIR) ## Download the SYSLINUX release the boot test installs

# SYSLINUX is not vendored: shipping someone else's binaries inside ours would
# mean carrying their version skew and redistribution obligations, and the same
# reasoning keeps it out of the binary. The tarball ships the prebuilt
# mbr.bin, ldlinux.bss, ldlinux.sys and ldlinux.c32 that an install needs;
# most distribution packages do not, because their installer embeds them.
$(SYSLINUX_DIR):
	@echo "Downloading syslinux $(SYSLINUX_VERSION)..."
	@mkdir -p $(SYSLINUX_CACHE)
	@curl -sSfL $(SYSLINUX_URL) -o $(SYSLINUX_CACHE)/syslinux.tar.xz
	@tar xf $(SYSLINUX_CACHE)/syslinux.tar.xz -C $(SYSLINUX_CACHE)
	@rm -f $(SYSLINUX_CACHE)/syslinux.tar.xz

tools-check: ## Verify the external validation tools are installed
	@missing=""; \
	for tool in $(REQUIRED_TOOLS); do \
		command -v $$tool >/dev/null 2>&1 || missing="$$missing $$tool"; \
	done; \
	if [ -n "$$missing" ]; then \
		echo "Missing required tool(s):$$missing"; \
		echo; \
		echo "  macOS:   brew install mtools dosfstools qemu"; \
		echo "  Debian:  sudo apt-get install mtools dosfstools qemu-system-x86"; \
		echo; \
		echo "mtools and dosfstools provide the independent FAT implementations"; \
		echo "the tests validate disk images against; QEMU runs the boot test."; \
		echo "See the Building and testing section of README.md."; \
		echo; \
		echo "To run only the tests that do not need them:  make unit"; \
		exit 1; \
	fi

vet: ## Run go vet
	go vet ./...

fmt: ## Format the source
	gofmt -l -w .

fmt-check: ## Fail if any file is not gofmt'd
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "These files are not gofmt'd:"; echo "$$unformatted"; exit 1; \
	fi

check: fmt-check vet test ## Everything CI runs

clean: ## Remove build artifacts
	rm -f $(BIN)
	rm -rf dist/
	rm -rf $(SYSLINUX_CACHE)

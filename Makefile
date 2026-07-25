BIN        := fatimg
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE ?= $(shell date -u +"%Y-%m-%d %H:%M:%S UTC")
LDFLAGS    := -X 'main.version=$(VERSION)' -X 'main.commit=$(COMMIT)' -X 'main.buildDate=$(BUILD_DATE)'

# External FAT implementations the tests validate our images against, so that
# a self-consistently wrong filesystem cannot pass.
REQUIRED_TOOLS := mdir mcopy fsck.fat

.PHONY: help all build test unit integration tools-check vet fmt fmt-check check clean

help: ## Show this help
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | awk -F':.*?## ' '{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

all: check build ## Verify and build

build: ## Build the binary with version info
	go build -ldflags "$(LDFLAGS)" -o $(BIN) .

# -count=1 disables the test cache. The external validation tests decide at
# run time whether mtools and fsck.fat are installed, and Go's cache key does
# not change when a tool appears in a directory already on PATH -- so a cached
# run happily replays "skipping, not installed" after you have installed them.
test: tools-check ## Run all tests, including image round-trips
	go test -count=1 ./...

unit: ## Run only fast tests (no external tools needed)
	go test -short ./...

integration: tools-check ## Run image round-trip and external validation tests verbosely
	go test -count=1 -v -run 'TestRoundTrip|TestList|TestCreate|TestGzip|TestMtools|TestFsck' ./...

tools-check: ## Verify the external FAT validation tools are installed
	@missing=""; \
	for tool in $(REQUIRED_TOOLS); do \
		command -v $$tool >/dev/null 2>&1 || missing="$$missing $$tool"; \
	done; \
	if [ -n "$$missing" ]; then \
		echo "Missing required tool(s):$$missing"; \
		echo; \
		echo "  macOS:   brew install mtools dosfstools"; \
		echo "  Debian:  sudo apt-get install mtools dosfstools"; \
		echo; \
		echo "These provide the independent FAT implementations the tests"; \
		echo "validate disk images against. See the Building and testing"; \
		echo "section of README.md."; \
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

BIN        := fatimg
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE ?= $(shell date -u +"%Y-%m-%d %H:%M:%S UTC")
LDFLAGS    := -X 'main.version=$(VERSION)' -X 'main.commit=$(COMMIT)' -X 'main.buildDate=$(BUILD_DATE)'

.PHONY: help all build test unit integration vet fmt fmt-check check clean

help: ## Show this help
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | awk -F':.*?## ' '{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

all: check build ## Verify and build

build: ## Build the binary with version info
	go build -ldflags "$(LDFLAGS)" -o $(BIN) .

test: ## Run all tests, including image round-trips
	go test ./...

unit: ## Run only fast tests (skips image round-trips)
	go test -short ./...

integration: ## Run image round-trip and external validation tests verbosely
	go test -v -run 'TestRoundTrip|TestList|TestCreate|TestGzip|TestMtools|TestFsck' ./...

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

# metakill build tasks. Everything builds with CGO_ENABLED=0 (pure Go).

BINARY    := metakill
PKG       := ./cmd/metakill
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -s -w -X main.version=$(VERSION)
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64
DIST      := dist

export CGO_ENABLED := 0

.PHONY: all build test test-race lint fixtures golden selftest release clean

all: lint test build

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) $(PKG)

test:
	go test ./...

# The race detector needs cgo.
test-race:
	CGO_ENABLED=1 go test -race ./...

lint:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	go vet ./...
	@if command -v golangci-lint >/dev/null 2>&1; then golangci-lint run ./...; else echo "golangci-lint not installed; skipped"; fi
	go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...

# Regenerate testdata/fixtures from internal/fixtures.
fixtures:
	go generate ./internal/fixtures

# Rewrite the golden dry-run outputs in testdata/golden.
golden: fixtures
	go test $(PKG) -update

selftest: build
	./bin/$(BINARY) --selftest

release:
	@rm -rf $(DIST) && mkdir -p $(DIST)
	@set -e; for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=""; [ "$$os" = windows ] && ext=".exe"; \
		out="$(DIST)/$(BINARY)_$(VERSION)_$${os}_$${arch}$$ext"; \
		echo "building $$out"; \
		GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o "$$out" $(PKG); \
	done
	@cd $(DIST) && (command -v sha256sum >/dev/null && sha256sum * || shasum -a 256 *) > SHA256SUMS
	@echo "artifacts in $(DIST)/"

clean:
	rm -rf bin $(DIST)

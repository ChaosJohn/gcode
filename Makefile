# gcode build orchestration. Every target enforces CGO_ENABLED=0
# (docs/port/DECISIONS.md D5).

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
PKG     := github.com/ChaosJohn/gcode/internal/version
LDFLAGS := -s -w \
	-X $(PKG).Version=$(VERSION) \
	-X $(PKG).Commit=$(COMMIT) \
	-X $(PKG).Date=$(DATE)

export CGO_ENABLED := 0

.PHONY: build test vet fmt check cross dist clean

build:
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/ ./cmd/...

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

# Full local gate used before every milestone push.
check: vet build test
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:" && gofmt -l . && exit 1)

# Native build of cmd/gcode for the host platform, stamped with metadata.
dist:
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/gcode ./cmd/gcode

# Cross-compile the release matrix (mirrors CI, CI uses scripts/cross-build.sh).
cross:
	bash scripts/cross-build.sh

clean:
	rm -rf bin dist

# coxswain core (v2). v1 lives in bin/ and is frozen (see docs/decisions/0006).
BIN     := dist/cox
PKG     := github.com/nphattai/coxswain
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X $(PKG)/internal/version.Version=$(VERSION)

.PHONY: build test install lint release-dry test-port vet-port

build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/cox

test:
	go test ./...

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/cox

# test-port and vet-port are kept as aliases: the firstmate port suites (epic cox-supervision-port) lost their `port`
# build tag once every case turned green, so `go test ./...` and `go vet ./...` run the whole corpus.
test-port: test

vet-port:
	go vet ./...

lint:
	@test -z "$$(gofmt -l .)" || { echo "gofmt needed on:"; gofmt -l .; exit 1; }
	go vet ./...

# release-dry builds the full release locally without publishing: four binaries plus archives with hooks/templates/skills.
release-dry:
	goreleaser release --snapshot --clean

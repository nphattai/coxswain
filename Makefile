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
	@$(MAKE) --no-print-directory test-port

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/cox

# test-port runs the firstmate port suites (build tag `port`, epic cox-supervision-port) and prints the red (failing
# leaf case) count per package; the red cases are the gap list. It never fails the build: the ratchet (a package's red
# count may only fall) is checked at audit from the before/after counts each story's PR carries.
test-port:
	@go test -tags port -json ./... 2>/dev/null | grep '"Action":"fail"' \
		| sed -E 's/.*"Package":"([^"]*)"(.*"Test":"([^"]*)")?.*/\1 \3/' \
		| awk 'NF == 1 { pkg[$$1] = 1; next } { f[$$0] = 1 } END { for (k in f) { split(k, a, " "); leaf = 1; for (o in f) if (o != k && index(o, k "/") == 1) leaf = 0; if (leaf) { n[a[1]]++; t++ } } for (p in n) printf "%5d red  %s\n", n[p], p; for (p in pkg) if (!(p in n)) printf "BUILD FAILED  %s (its port cases did not run)\n", p; printf "%5d red  total (go test -tags port ./...)\n", t }' \
		| sort -k3

vet-port:
	go vet -tags port ./...

lint:
	@test -z "$$(gofmt -l .)" || { echo "gofmt needed on:"; gofmt -l .; exit 1; }
	go vet ./...

# release-dry builds the full release locally without publishing: four binaries plus archives with hooks/templates/skills.
release-dry:
	goreleaser release --snapshot --clean

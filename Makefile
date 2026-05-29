GO_PACKAGES := ./...
GOIMPORTS ?= go run golang.org/x/tools/cmd/goimports@v0.31.0
GOLANGCI_LINT ?= go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.4.0
GORELEASER ?= go run github.com/goreleaser/goreleaser/v2@v2.16.0
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LOCAL_BIN := dist/am
TUI_DIST := dist/tui
TUI_GOOS = $(shell go env GOOS)
TUI_GOARCH = $(shell go env GOARCH)
TUI_BUN_ARCH_amd64 = x64
TUI_BUN_ARCH_arm64 = arm64
TUI_BUN_ARCH = $(TUI_BUN_ARCH_$(TUI_GOARCH))
TUI_BIN = $(TUI_DIST)/agent-machine-tui_$(TUI_GOOS)_$(TUI_GOARCH)
BUN_TMPDIR ?= /tmp/agent-machine-bun
BUN_INSTALL ?= /tmp/agent-machine-bun-install

.PHONY: fmt fmt-check vet lint test ci build install-local tui-deps tui-check tui-build release-check release-snapshot

build:
	mkdir -p $(dir $(LOCAL_BIN))
	CGO_ENABLED=0 go build -ldflags "-X github.com/weskor/agent-machine/internal/cli.Version=$(VERSION)" -o $(LOCAL_BIN) .

install-local: build
	mkdir -p $(HOME)/.local/bin
	cp $(LOCAL_BIN) $(HOME)/.local/bin/am

fmt:
	find . -name '*.go' -not -path './.git/*' -not -path './.am/*' -not -path './.clawpatch/*' -print0 | xargs -0 gofmt -w --
	find . -name '*.go' -not -path './.git/*' -not -path './.am/*' -not -path './.clawpatch/*' -print0 | xargs -0 $(GOIMPORTS) -w --

fmt-check:
	@test -z "$$(find . -name '*.go' -not -path './.git/*' -not -path './.am/*' -not -path './.clawpatch/*' -print0 | xargs -0 gofmt -l --)"
	@test -z "$$(find . -name '*.go' -not -path './.git/*' -not -path './.am/*' -not -path './.clawpatch/*' -print0 | xargs -0 $(GOIMPORTS) -l --)"

vet:
	go vet $(GO_PACKAGES)

lint:
	$(GOLANGCI_LINT) run $(GO_PACKAGES)

test:
	go test $(GO_PACKAGES)

# ci is the local all-in validation target: Go gates plus the TUI build check.
ci: fmt-check vet lint test tui-check

tui-deps:
	mkdir -p $(BUN_TMPDIR) $(BUN_INSTALL)
	cd tui && BUN_TMPDIR=$(BUN_TMPDIR) BUN_INSTALL=$(BUN_INSTALL) bun install --frozen-lockfile

tui-check: tui-deps
	cd tui && BUN_TMPDIR=$(BUN_TMPDIR) BUN_INSTALL=$(BUN_INSTALL) bun build src/index.ts --target=bun --outdir /tmp/agent-machine-tui-check

tui-build:
	mkdir -p $(TUI_DIST)
	@test -n "$(TUI_BUN_ARCH)" || (echo "unsupported TUI_GOARCH=$(TUI_GOARCH)" && exit 1)
	cd tui && bun build --compile --target=bun-$(TUI_GOOS)-$(TUI_BUN_ARCH) --outfile ../$(TUI_BIN) src/index.ts

release-check:
	$(GORELEASER) check

release-snapshot:
	$(GORELEASER) release --snapshot --clean

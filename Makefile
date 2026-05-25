GO_PACKAGES := ./...
GOIMPORTS ?= go run golang.org/x/tools/cmd/goimports@v0.31.0
GOLANGCI_LINT ?= go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.4.0
GORELEASER ?= go run github.com/goreleaser/goreleaser/v2@v2.16.0

.PHONY: fmt fmt-check vet lint test ci release-check release-snapshot

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './.git/*' -not -path './.am/*')
	$(GOIMPORTS) -w $$(find . -name '*.go' -not -path './.git/*' -not -path './.am/*')

fmt-check:
	@test -z "$$(gofmt -l $$(find . -name '*.go' -not -path './.git/*' -not -path './.am/*'))"
	@test -z "$$($(GOIMPORTS) -l $$(find . -name '*.go' -not -path './.git/*' -not -path './.am/*'))"

vet:
	go vet $(GO_PACKAGES)

lint:
	$(GOLANGCI_LINT) run $(GO_PACKAGES)

test:
	go test $(GO_PACKAGES)

ci: fmt-check vet lint test

release-check:
	$(GORELEASER) check

release-snapshot:
	$(GORELEASER) release --snapshot --clean

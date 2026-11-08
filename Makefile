.PHONY: build build-compile test race lint fmt tidy check example fixtures help

# Version named in the co-author trailer of the commits okdp-gitops writes,
# without the leading "v" of the tag; empty outside a checkout, it is then
# the main module's version or dev.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')
LDFLAGS := -X github.com/okdp/okdp-gitops/internal/buildinfo.Version=$(VERSION)

# golangci-lint at the version tools/go.mod pins: a module of its own, its
# dependencies stay out of the library's go.mod.
GOLANGCI_LINT := go tool -modfile=tools/go.mod golangci-lint

# The Go files, but those of dot directories (.claude/worktrees...).
GOFILES = $(shell find . -name '*.go' -not -path './.*')

build: ## Compile the CLI to bin/okdp-gitops
	go build -ldflags "$(LDFLAGS)" -o bin/okdp-gitops ./cmd/okdp-gitops

build-compile: ## Compile the okdp-compile CLI to bin/okdp-compile
	go build -ldflags "-X main.version=$(VERSION)" -o bin/okdp-compile ./cmd/okdp-compile

test: ## Run all tests
	go test ./...

race: ## Run all tests with the race detector
	go test -race ./...

lint: ## Run golangci-lint, the version tools/go.mod pins
	$(GOLANGCI_LINT) run ./...

fmt: ## Format the Go files (gofmt -w), listing those changed
	@gofmt -l -w $(GOFILES)

tidy: ## Tidy go.mod and tools/go.mod
	go mod tidy
	go -C tools mod tidy

check: ## Run what CI runs: gofmt, vet, lint, tidy, build and race tests
	@out=$$(gofmt -l $(GOFILES)); test -z "$$out" || { echo "not formatted, run make fmt:" >&2; echo "$$out" >&2; exit 1; }
	go vet ./...
	$(GOLANGCI_LINT) run ./...
	go mod tidy -diff
	go -C tools mod tidy -diff
	go build ./...
	go test -race ./...

example: ## Rewrite the outputs of examples/ from the current compiler
	go test . -run '^TestExample$$' -update

fixtures: ## Regenerate testdata/helm (helm, python3, UMBRELLA_HELMS)
	go test ./internal/helmvalues -run '^TestHelmValues$$' -update
	go test ./internal/target/helm -run '^TestHelmUmbrella$$' -update
	UMBRELLA_HELMS="$(UMBRELLA_HELMS)" testdata/helm/generate.sh

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

.DEFAULT_GOAL := help

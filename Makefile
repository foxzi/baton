BINARY := baton
PKG    := github.com/foxzi/baton
CMD    := ./cmd/baton

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X $(PKG)/internal/version.Version=$(VERSION) \
	-X $(PKG)/internal/version.Commit=$(COMMIT) \
	-X $(PKG)/internal/version.Date=$(DATE)

.PHONY: all build test race vet fmt lint tidy clean docs docs-check validate-apis validate-examples

all: fmt vet test build

build:
	go build -ldflags '$(LDFLAGS)' -o $(BINARY) $(CMD)

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

lint:
	golangci-lint run

tidy:
	go mod tidy

# validate-apis replays the recorded examples of every pack through its
# envelope and transforms, the way the contract tests in CI do.
validate-apis: build
	./$(BINARY) apis validate apis/*

# validate-examples checks every shipped scenario, so an example cannot go
# stale against the schema, its packs or its own step references.
validate-examples: build
	@set -e; for scenario in examples/*.yaml; do ./$(BINARY) validate $$scenario; done

# docs regenerates the schema reference; edit the schema, never these files.
docs:
	go run $(CMD) schema --markdown en > docs/en/schema.md
	go run $(CMD) schema --markdown ru > docs/ru/schema.md

# docs-check fails when the committed reference no longer matches the schema.
docs-check:
	@go run $(CMD) schema --markdown en | diff -u docs/en/schema.md - \
		|| { echo 'docs/en/schema.md is stale, run make docs'; exit 1; }
	@go run $(CMD) schema --markdown ru | diff -u docs/ru/schema.md - \
		|| { echo 'docs/ru/schema.md is stale, run make docs'; exit 1; }

clean:
	rm -f $(BINARY)
	rm -rf dist

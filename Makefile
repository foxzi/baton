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

.PHONY: all build test race vet fmt lint tidy clean

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

clean:
	rm -f $(BINARY)
	rm -rf dist

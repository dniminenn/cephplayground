BIN     := bin/cephplayground
PKG     := ./cmd/cephplayground
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/dniminenn/cephplayground/internal/adm.Version=$(VERSION)
GOFLAGS := -trimpath -ldflags='$(LDFLAGS)'

.PHONY: all build test vet clean install

all: build

build:
	@mkdir -p bin
	CGO_ENABLED=0 go build $(GOFLAGS) -o $(BIN) $(PKG)

test:
	go test ./...

vet:
	go vet ./...

install:
	CGO_ENABLED=0 go install $(GOFLAGS) $(PKG)

clean:
	rm -rf bin dist

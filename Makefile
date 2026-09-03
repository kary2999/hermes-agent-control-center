GO       ?= go
BIN_DIR  ?= bin

.PHONY: all fmt vet test test-race build build-connector build-relay clean

all: fmt vet test build

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

build: build-connector build-relay

build-connector:
	GOOS=darwin GOARCH=arm64 $(GO) build -o $(BIN_DIR)/hermes-connector ./cmd/connector

build-relay:
	GOOS=linux GOARCH=amd64 $(GO) build -o $(BIN_DIR)/hermes-relay ./cmd/relay

clean:
	rm -rf $(BIN_DIR)

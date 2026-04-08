.PHONY: build build-mcp run run-mcp test test-integration lint clean

BINARY=haas
BINARY_MCP=haas-mcp

build:
	go build -o bin/$(BINARY) ./cmd/haas

build-mcp:
	go build -o bin/$(BINARY_MCP) ./cmd/haas-mcp

run:
	go run ./cmd/haas

run-mcp:
	go run ./cmd/haas-mcp

test:
	go test ./internal/...

test-integration:
	go test -tags=integration -v ./test/integration/...

lint:
	golangci-lint run ./...

clean:
	rm -rf bin/

deps:
	go mod tidy

.DEFAULT_GOAL := build

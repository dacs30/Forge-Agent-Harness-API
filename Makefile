.PHONY: build run test test-integration lint clean

BINARY=haas

build:
	go build -o bin/$(BINARY) ./cmd/haas

run:
	go run ./cmd/haas

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

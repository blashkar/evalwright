BIN := bin/evalwright
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build test lint fmt example clean

build:
	go build -ldflags "-X main.version=$(VERSION)" -o $(BIN) ./cmd/evalwright

test:
	go test -race ./...

lint:
	golangci-lint run

fmt:
	golangci-lint fmt

example: build
	$(BIN) run cmd/evalwright/example.yaml

clean:
	rm -rf bin

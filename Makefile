BIN := bin/evalwright
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build test test-ollama lint fmt example demo docker clean

build:
	go build -ldflags "-X main.version=$(VERSION)" -o $(BIN) ./cmd/evalwright

test:
	go test -race ./...

# Integration tests against a local Ollama server (not run in CI).
test-ollama:
	go test -race -tags ollama -run Integration -v ./internal/provider/

lint:
	golangci-lint run

fmt:
	golangci-lint fmt

# Runs offline with the mock provider.
example: build
	$(BIN) run examples/offline.yaml

# Runs against Ollama, downloading llama3.2:3b if needed.
demo: build
	$(BIN) run examples/quickstart.yaml --pull

# Builds the container image that docker-compose.yml runs.
docker:
	docker build --build-arg VERSION=$(VERSION) -t evalwright:local .

clean:
	rm -rf bin

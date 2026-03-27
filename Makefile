.PHONY: build run dev clean test fmt vet help

# Build as amp-proxy-v2 to never overwrite the running binary
BINARY := bin/amp-proxy-v2
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")

build:
	go build -ldflags "-X main.version=$(VERSION)" -o $(BINARY) .

run: build
	$(BINARY)

dev:
	go run -ldflags "-X main.version=$(VERSION)" .

clean:
	rm -rf bin/ dist/

test:
	go test -v ./...

fmt:
	go fmt ./...

vet:
	go vet ./...

help:
	@echo "amp-proxy v2 - Smart proxy for Amp CLI with built-in provider auth"
	@echo ""
	@echo "Available commands:"
	@echo "  make build   - Build the binary (bin/amp-proxy-v2)"
	@echo "  make run     - Build and run"
	@echo "  make dev     - Run in development mode"
	@echo "  make clean   - Clean build artifacts"
	@echo "  make test    - Run tests"
	@echo "  make fmt     - Format code"
	@echo "  make vet     - Run go vet"

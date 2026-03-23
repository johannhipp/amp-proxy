.PHONY: build run dev clean test

build:
	go build -o bin/amp-proxy .

run: build
	./bin/amp-proxy

dev:
	go run .

clean:
	rm -rf bin/ dist/

test:
	go test -v ./...

fmt:
	go fmt ./...

vet:
	go vet ./...

help:
	@echo "amp-proxy - Conditional HTTP Proxy Server"
	@echo ""
	@echo "Available commands:"
	@echo "  make build   - Build the binary"
	@echo "  make run     - Build and run"
	@echo "  make dev     - Run in development mode"
	@echo "  make clean   - Clean build artifacts"
	@echo "  make test    - Run tests"
	@echo "  make fmt     - Format code"
	@echo "  make vet     - Run go vet"

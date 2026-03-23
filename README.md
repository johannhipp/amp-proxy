# amp-proxy

A lightweight conditional HTTP proxy server written in Go. Routes incoming requests to a target server, with the ability to block or conditionally forward requests based on custom matching rules.

## Features

- Fast HTTP request proxying
- Rule-based request matching (priority-ordered)
- Block or reject specific requests
- Conditional routing based on request properties
- Environment variable configuration
- Zero-dependency core proxy logic

## Architecture

- **config.go** - Configuration and rule definitions
- **proxy.go** - Reverse proxy handler and request routing
- **main.go** - Server startup and CLI
- **go.mod** - Go module definition

## Configuration

### Default Rules

1. **block-admin** (Priority 100)
   - Blocks requests to `/admin` and `/admin/*`
   - Target: empty (rejected)

2. **auth-required** (Priority 90)
   - Routes `/private` and `/private/*` to default target
   - Can be extended to check Authorization header

### Custom Configuration

Edit `config.go` to add custom rules:

```go
{
    Name: "api-v2",
    Match: func(r *http.Request) bool {
        return r.Header.Get("X-API-Version") == "2"
    },
    Target: "http://api-v2.example.com",
    Priority: 80,
}
```

## Usage

### Start with defaults
```bash
make run
```

### Start with custom options
```bash
go run . -port 8080 -addr 127.0.0.1 -target http://localhost:9000
```

### Environment variables
```bash
LISTEN_PORT=8080 LISTEN_ADDR=127.0.0.1 DEFAULT_TARGET=http://localhost:9000 go run .
```

### Test the proxy
```bash
# Request that gets proxied to default target
curl -i http://localhost:3000/api/users

# Request that gets blocked
curl -i http://localhost:3000/admin

# Request that requires auth (currently always proxies, can add auth check)
curl -i http://localhost:3000/private
```

## Development

```bash
make dev       # Run with hot-reload (requires CompileDaemon)
make build     # Build binary
make test      # Run tests
make fmt       # Format code
make vet       # Run linter
```

## How It Works

1. Request arrives at proxy
2. Rules evaluated in priority order (highest first)
3. First matching rule determines target
4. If target is empty string, request is blocked (403)
5. Otherwise, request is forwarded via reverse proxy
6. Response returned to client

## Extending

To add new matching logic, modify the `Rule` definitions in `config.go`:

```go
Rule{
    Name: "custom-rule",
    Match: func(r *http.Request) bool {
        // Your matching logic
        return r.Header.Get("Custom-Header") == "value"
    },
    Target: "http://target.example.com",
    Priority: 50,
}
```

Target URLs can be:
- Full HTTP/HTTPS URLs: `http://localhost:3001`
- Empty string (block request): `""`

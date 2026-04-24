# amp-proxy

Use [Amp](https://ampcode.com) with your existing Claude Max, ChatGPT Plus, or Gemini subscriptions — no API billing.

## Quick Start

```bash
git clone https://github.com/johannhipp/amp-proxy && cd amp-proxy
./bin/install.sh
amp-proxy login claude
amp-proxy
```

Then point Amp at the proxy:

```bash
echo '{"amp.url": "http://localhost:18317"}' > ~/.config/amp/settings.json
```

## What It Does

amp-proxy sits between the Amp CLI and the world. It routes LLM calls through your consumer subscriptions (via [CLIProxyAPIPlus](https://github.com/router-for-me/CLIProxyAPIPlus)) while keeping everything else — OAuth, threads, settings, GitHub — on ampcode.com.

```
Amp CLI
  │
  ▼
amp-proxy (:18317)
  │
  ├── /api/provider/*  ──►  built-in provider gateway   LLM calls (Claude, GPT, Gemini, ...)
  ├── /v1/*, /api/v1/* ──►  built-in provider gateway   LLM streaming
  ├── /auth/*          ──►  ampcode.com                  OAuth (302 redirect)
  ├── /api/internal    ──►  Exa API / stubs              web_search, read_web_page
  └── everything else  ──►  ampcode.com                  threads, settings, GitHub
```

Single binary. Single port. No vibeproxy needed.

## Features

- **Built-in provider auth** — `amp-proxy login claude` authenticates with your subscription, no separate app needed
- **Multi-provider support** — Claude, OpenAI, Gemini, GitHub Copilot, Qwen — with round-robin and failover
- **Model remapping** — unsupported models (Gemini) get translated to ones you have (Claude, GPT), with full request/response protocol translation across Google GenAI, Anthropic Messages, and OpenAI Chat Completions formats, including streaming
- **Configurable** — model mappings, providers, API keys, routing strategy — all via optional YAML config
- **Web search via Exa** — intercepts Amp's `web_search` and `read_web_page` tools and routes them through the [Exa API](https://exa.ai)
- **Credit gate bypass** — fakes `getUserFreeTierStatus` so server-side tools aren't blocked
- **Stable tool translation** — fixes repeated same-name tool calls with one-to-one ID tracking
- **Zero config for common use** — works out of the box after `login`, config file only needed for advanced use

## Install

Requires [Go 1.21+](https://go.dev/dl/). Works on macOS and Linux (amd64/arm64).

```bash
git clone https://github.com/johannhipp/amp-proxy
cd amp-proxy
./bin/install.sh
```

This builds amp-proxy and downloads `cli-proxy-api-plus` (needed for auth flows) into `~/.local/bin`.

Override the install directory:

```bash
AMP_PROXY_INSTALL_DIR=/usr/local/bin ./bin/install.sh
```

## Auth

Authenticate with one or more providers:

```bash
amp-proxy login claude       # Claude Max / Pro
amp-proxy login openai       # ChatGPT Plus / Pro
amp-proxy login gemini       # Gemini
amp-proxy login copilot      # GitHub Copilot
amp-proxy login qwen         # Qwen
```

Check status:

```bash
amp-proxy status
```

```
Provider     Status        Account              Expires
────────     ──────        ───────              ───────
claude       ✓ active      user@gmail.com       2025-04-15
openai       ✓ active      user@gmail.com       2025-04-12
gemini       ✗ not authed  -                    -
```

Tokens are stored in `~/.cli-proxy-api/` — compatible with vibeproxy, so existing tokens carry over.

If amp-proxy was already running when you logged in, restart it — tokens are loaded at startup, so new ones added mid-run aren't picked up until restart.

## Usage

```bash
# Start with defaults (127.0.0.1:18317)
amp-proxy

# Custom port
amp-proxy serve --port 9000

# With Exa web search
EXA_API_KEY=your-key amp-proxy

# Debug logging
amp-proxy serve --debug
```

## Configuration

Config is **optional**. amp-proxy works with zero config for most users. An optional YAML file unlocks advanced features.

Auto-detected at `~/.config/amp-proxy/config.yaml` (macOS: `~/Library/Application Support/amp-proxy/config.yaml`), or set `AMP_PROXY_CONFIG`.

```yaml
# Only include what you want to change. Everything has defaults.

# Exa API for web_search tool
exa_api_key: ${EXA_API_KEY}

# Provider routing: "round-robin" (default) or "fill-first"
routing: fill-first

# Custom model remapping
model_remaps:
  - from: gemini-3-flash-preview
    to: claude-sonnet-4-6
    provider: anthropic

  - from: gemini-3-pro
    to: gpt-5.4
    provider: openai

# Direct API keys (skip OAuth)
anthropic_api_keys:
  - key: ${ANTHROPIC_API_KEY}

# Enable/disable providers
providers:
  claude: true
  openai: true
  gemini: false
```

See [`config.example.yaml`](config.example.yaml) for all options.

## Prompt Caching

By default, amp-proxy strips `cache_control` fields from request bodies before forwarding to providers. This prevents 400 errors on some OAuth routes, but **disables Anthropic prompt caching** — which can significantly increase token usage in long sessions.

To enable prompt caching, set `strip_cache_control: false` in your config:

```yaml
strip_cache_control: false
```

If you see 400 errors from Anthropic after disabling this, re-enable it.

## Model Remapping

When Amp requests a model you don't have (e.g., Gemini), amp-proxy translates the request to a provider you do have. This includes full protocol translation — request body, response body, and streaming — across Google GenAI, Anthropic Messages, and OpenAI Chat Completions formats.

Default mappings (configurable via YAML):

| Amp requests | Served by | Provider |
|---|---|---|
| gemini-3-flash-preview | claude-sonnet-4-6 | Anthropic |
| gemini-3-flash | claude-sonnet-4-6 | Anthropic |
| gemini-3-pro | gpt-5.4 | OpenAI |
| gemini-3-pro-image | gpt-image-1 | OpenAI |
| anything else unsupported | claude-sonnet-4-6 | Anthropic |

## Web Search

Amp's `web_search` and `read_web_page` are server-side tools on ampcode.com, gated by credits. amp-proxy intercepts them and routes through the [Exa API](https://exa.ai) instead.

Set `EXA_API_KEY` to enable. Without it, tools return a stub message.

## Endpoints

| Path | Method | Description |
|------|--------|-------------|
| `/healthz` | GET | Health check → `{"status":"ok"}` |
| `/metrics` | GET | Request counters (JSON) |

## CLI Reference

```
amp-proxy [command] [flags]

Commands:
  serve     Start the proxy server (default)
  login     Authenticate with a provider
  logout    Remove saved auth for a provider
  status    Show auth status for all providers
  version   Print version info

Flags (serve):
  --port <port>      Listen port (default: 18317, env: LISTEN_PORT)
  --addr <addr>      Listen address (default: 127.0.0.1, env: LISTEN_ADDR)
  --config <path>    Config file path (env: AMP_PROXY_CONFIG)
  --ampcode <url>    Ampcode URL (default: https://ampcode.com)
  --debug            Enable debug logging (env: AMP_PROXY_DEBUG)
```

## Development

```bash
make test    # run tests
make dev     # run without building
make fmt     # format code
make vet     # run go vet
```

When running from source (`make dev` / `go run .`), `amp-proxy login` shells out to `cli-proxy-api-plus`, which the install script normally provides. To use `login` in dev mode, install the binary yourself:

```bash
go install github.com/router-for-me/CLIProxyAPI/v6/cmd/server@latest
ln -sf ~/go/bin/server ~/go/bin/cli-proxy-api-plus
```

Make sure `~/go/bin` is on your `PATH`. Or just run `./bin/install.sh` once to get a released `cli-proxy-api-plus` into `~/.local/bin`.

## Migrating from vibeproxy

If you're already using vibeproxy + the old amp-proxy:

1. Install the new amp-proxy: `./bin/install.sh`
2. `amp-proxy status` — your existing tokens in `~/.cli-proxy-api/` are picked up automatically
3. `amp-proxy` — start the proxy, vibeproxy is no longer needed
4. Stop vibeproxy

## Releases

Automated via [`.github/workflows/release.yml`](.github/workflows/release.yml) and [`.goreleaser.yml`](.goreleaser.yml).

```bash
git tag v0.2.0
git push origin v0.2.0
```

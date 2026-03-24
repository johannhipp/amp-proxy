# amp-proxy

Use your existing Claude Max, ChatGPT Plus, or Gemini subscriptions with [Amp](https://ampcode.com) through [vibeproxy](https://github.com/automazeio/vibeproxy). No API keys, no credits.

The problem: when you point Amp at vibeproxy (`amp.url`), *all* traffic goes there -- auth, GitHub, threads, settings, everything. Most of that breaks. amp-proxy sits in the middle and sends each request where it actually needs to go.

```
Amp CLI
  │
  ▼
amp-proxy (:18317)
  │
  ├── /api/provider/*  ──►  vibeproxy (:8317)   LLM calls
  ├── /v1/*, /api/v1/* ──►  vibeproxy (:8317)   LLM streaming
  ├── /auth/*          ──►  ampcode.com          OAuth (302 redirect)
  └── everything else  ──►  ampcode.com          threads, settings, GitHub, etc.
```

## Features

- **Request routing** -- LLM calls go to vibeproxy, everything else to ampcode.com
- **OAuth redirect** -- auth paths get 302'd to ampcode.com so cookies land on the right domain
- **Model remapping** -- swap unsupported models (Gemini) for ones you have (Claude, GPT), with full request/response translation across Google GenAI, Anthropic Messages, and OpenAI Chat Completions formats, including streaming
- **Stable subagent tool translation** -- fixes repeated same-name tool calls (like Finder's multiple `shell_command` steps) by tracking tool call/result IDs one-to-one, preventing duplicate `tool_result` protocol errors
- **Web search via Exa** -- intercepts Amp's `web_search` and `read_web_page` server-side tools and routes them through the [Exa API](https://exa.ai) instead of ampcode.com's credit-gated backend
- **Credit gate bypass** -- fakes `getUserFreeTierStatus` so the CLI doesn't block server-side tool dispatch
- **Structured logging** -- every request logged with slog (request ID, headers redacted, JSON body previews, route decisions, response timing)
- **Graceful shutdown** -- SIGINT/SIGTERM trigger a clean drain of in-flight requests (10s timeout)
- **Health check** -- `/healthz` endpoint for Docker, k8s, or monitoring
- **Request metrics** -- `/metrics` endpoint with JSON counters per route type

## Setup

```bash
# build
make build

# run (defaults: listen on :18317, vibeproxy on :8317)
./bin/amp-proxy

# with Exa web search enabled
EXA_API_KEY=your-key ./bin/amp-proxy

# with custom targets
./bin/amp-proxy --port 18317 --vibeproxy http://localhost:8317 --ampcode https://ampcode.com
```

Point Amp at the proxy:

```bash
echo '{"amp.url": "http://localhost:18317"}' > ~/.config/amp/settings.json
```

You'll also need the Amp API key registered for this URL. Copy your existing key:

```bash
# check your existing keys
cat ~/.local/share/amp/secrets.json

# add an entry for the proxy URL (same key, different URL)
```

See `.env.example` for all configuration options.

## Model remapping

If you don't have a subscription for every provider Amp uses, amp-proxy can swap unsupported models for ones you do have access to. When Amp requests a Gemini model (and you don't have Google OAuth in vibeproxy), the proxy translates the request to a supported provider automatically.

Default mappings in `remap.go`:

| Amp requests | Gets served by | Provider |
|---|---|---|
| gemini-3-flash-preview | claude-sonnet-4-6 | Anthropic |
| gemini-3-flash | claude-sonnet-4-6 | Anthropic |
| gemini-3-pro | gpt-5.4 | OpenAI |
| gemini-3-pro-image | gpt-image-1 | OpenAI |
| anything else unsupported | claude-sonnet-4-6 | Anthropic |

The translation handles request/response format conversion between Google GenAI, Anthropic Messages, and OpenAI Chat Completions APIs. Streaming works too.

To change the mappings, edit the `modelMappings` slice in `remap.go`. Unmapped models fall back to Sonnet 4.6 with a warning in the logs so you know what to add.

## Server-side tool interception

Amp executes `web_search` and `read_web_page` server-side on ampcode.com, gated by a credit check. If your account has no credits, both tools fail. amp-proxy intercepts the `/api/internal` RPC calls and routes them to the Exa API instead.

Three intercepts, in order:

1. **`getUserFreeTierStatus` fake** -- The CLI polls this every 30s. ampcode.com returns `canUseAmpFree: false` when credits are exhausted, blocking tool dispatch. The proxy returns `canUseAmpFree: true` to unblock the client-side gate.

2. **`webSearch2` → Exa `/search`** -- `web_search` tool calls become `POST /api/internal?webSearch2` with `{"method":"webSearch2","params":{"objective":"...","maxResults":N}}`. The proxy translates this to an Exa search request and returns results in the schema the CLI expects (`result.results[]` with `title`, `url`, `text`).

3. **`extractWebPageContent` → Exa `/contents`** -- `read_web_page` calls become `POST /api/internal?extractWebPageContent` with `{"method":"extractWebPageContent","params":{"url":"...","objective":"..."}}`. The proxy fetches the page via Exa and returns content as `result.excerpts[]`.

Set `EXA_API_KEY` to enable. Without it, both tools return a stub message instead of failing.

## Logging

Structured logs via `slog`. Every request includes request ID, method, path, route decision, and response timing.

```
INFO request reqID=14 method=POST path=/api/provider/anthropic/v1/messages
INFO route   reqID=14 label=VIBEPROXY method=POST path=/api/provider/anthropic/v1/messages target=http://localhost:8317 rule=provider-to-vibeproxy
INFO response reqID=14 label=VIBEPROXY status=200 statusText=OK bytes=8450 elapsed=15622ms
```

## Endpoints

| Path | Method | Description |
|------|--------|-------------|
| `/healthz` | GET | Health check, returns `{"status":"ok"}` |
| `/metrics` | GET | Request counters (total, vibeproxy, ampcode, remap, exa, errors) |

## Development

```bash
make test    # run tests
make dev     # run without building
make fmt     # format code
make vet     # run go vet
```

## Release

Tag a version and push to trigger a GitHub Actions release with cross-platform binaries:

```bash
git tag v0.1.0
git push origin v0.1.0
```

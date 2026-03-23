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

## Setup

```bash
# build
make build

# run (defaults: listen on :18317, vibeproxy on :8317)
./bin/amp-proxy

# or with custom targets
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

## Logging

Every request gets logged with a request ID, headers (auth redacted), JSON body previews, route decisions, and response timing. Useful for figuring out what Amp is actually doing.

```
[0014] REQUEST  POST /api/provider/google/.../gemini-3-flash-preview:generateContent
[0014] REMAP    google/gemini-3-flash-preview -> anthropic/claude-sonnet-4-6
[0014] RESPONSE REMAP     200 OK (928ms)
```

## Development

```bash
make test    # run tests
make dev     # run without building
make fmt     # format code
```

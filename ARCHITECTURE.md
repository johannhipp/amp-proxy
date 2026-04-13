# amp-proxy v2: Eliminating vibeproxy — Revised Architecture

## Flaws Found in v1 Plan & Corrections

### Flaw 1: Over-engineered config for a local dev tool
The original YAML schema was ~60 lines with sections for thinking, gateway, cliproxy subtrees, OAuth aliases, exclusions, etc. For a tool whose killer feature is "clone, build, run," this is a UX regression. **90% of users need zero config.**

**Fix:** Config file is optional. Sensible defaults for everything. Flags/env override the few things that vary. Config file only needed for advanced use (model remaps, API keys, multi-account).

### Flaw 2: Embedding CLIProxyAPIPlus via Builder/Service drags in gin
The `cliproxy.Builder` and `cliproxy.Service` types import `internal/api` which imports gin — even if you never call `Run()`. But `coreauth.Manager` and the executor layer have **zero gin dependency** and can be used standalone.

**Fix:** Embed CLIProxyAPIPlus via `cliproxy.Builder`/`Service` on a localhost ephemeral port. While this pulls in gin as a transitive dependency, it reuses the full battle-tested auth pipeline (token refresh, retry, credential selection) without reimplementing it. The gin dependency is acceptable since it only adds ~10MB and is never exposed externally.

### Flaw 3: Default bind address `0.0.0.0` is too open
A local dev tool with auth tokens should not default to all interfaces.

**Fix:** Default to `127.0.0.1`.

### Flaw 4: ThinkingProxy features may not be needed
The `-thinking-N` model suffix is a vibeproxy convention, not an Amp CLI feature. We shouldn't implement middleware for conventions we haven't verified in real traffic.

**Fix:** Cut thinking middleware from scope. Add logging to capture real model names. Implement only if verified.

### Flaw 5: Vercel gateway bypass is vibeproxy-specific
Niche, high-coupling, hard to explain in UX.

**Fix:** Cut from scope entirely.

### Flaw 6: Cold-start problem
If nothing works until you create a config AND complete OAuth, the first-run experience is terrible.

**Fix:** `amp-proxy` starts and works immediately for non-provider routes (ampcode.com, auth, tools). Provider requests without auth return a clear actionable error: *"Not authenticated. Run `amp-proxy login claude` first."*

### Flaw 7: Auth from request handlers is dangerous
Opening browsers or printing device codes from inside an HTTP handler causes races (multiple concurrent requests → multiple auth attempts).

**Fix:** Auth is always an explicit CLI action, never triggered from request serving.

### Flaw 8: Hot-reload complexity for no benefit
Config hot-reload via fsnotify adds complexity. Restarting a local tool takes <1 second.

**Fix:** No hot-reload. Read config once on startup.

---

## Revised Architecture

### Design Principles

1. **Zero config for common use** — clone, build, `amp-proxy login claude`, `amp-proxy serve`, done
2. **Embedded auth server** — `cliproxy.Builder`/`Service` on localhost ephemeral port; gin is a transitive dep but never exposed externally
3. **Fail gracefully** — provider down? auth expired? Non-provider routes keep working, provider routes return actionable errors
4. **Reuse existing tokens** — `~/.cli-proxy-api/` works as-is for users coming from vibeproxy
5. **One binary, one port, one process**

### Current Flow (before)

```
Amp CLI → amp-proxy (:18317) → vibeproxy (:8317) → CLIProxyAPIPlus (:8318) → providers
                ↘ ampcode.com
```

### New Flow (after)

```
Amp CLI → amp-proxy (:18317)
              ├─ /auth/*              → 302 redirect to ampcode.com
              ├─ /api/internal        → tool stubs / Exa
              ├─ /api/provider/*      → provider pipeline → embedded CLIProxyAPIPlus (127.0.0.1:ephemeral) → providers
              ├─ /v1/*, /api/v1/*     → provider pipeline → embedded CLIProxyAPIPlus (127.0.0.1:ephemeral) → providers
              └─ everything else      → ampcode.com reverse proxy
```

**Embedded CLIProxyAPIPlus on localhost.** A `cliproxy.Service` runs on an ephemeral `127.0.0.1` port inside the process. Provider requests are reverse-proxied to it, reusing the full auth pipeline.

---

## How CLIProxyAPIPlus Is Embedded

Using `cliproxy.Builder`/`Service` to run the full auth server in-process:

```go
import (
    "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy"
    "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

// Load config, override host/port to localhost ephemeral
cfg, _ := config.LoadConfig(configPath)
cfg.Host = "127.0.0.1"
cfg.Port = ephemeralPort

// Build the service
service, _ := cliproxy.NewBuilder().
    WithConfig(cfg).
    WithConfigPath(configPath).
    Build()

// Run in background goroutine
go service.Run(ctx)

// Provider requests are reverse-proxied to this address
proxyURL := fmt.Sprintf("http://127.0.0.1:%d", ephemeralPort)
```

### What we get for free from the embedded CLIProxyAPIPlus
- OAuth token refresh (background loop)
- Round-robin / fill-first credential selection
- Multi-account failover with cooldown
- Quota tracking per model per credential
- Retry across credentials on 429/401

### What we still own in amp-proxy
- HTTP server (`net/http`)
- Request routing (ampcode vs. provider)
- Google ↔ Anthropic/OpenAI protocol translation
- Exa/tool stub interception
- Request body mutations (strip cache_control, strip openai fields)
- Graceful shutdown, logging, metrics

---

## Config: Optional, Minimal, Progressive

### Zero config (most users)

```bash
amp-proxy login claude      # OAuth flow, tokens saved to ~/.cli-proxy-api/
amp-proxy serve             # starts on 127.0.0.1:18317, auto-detects tokens
```

That's it. Model remaps use built-in defaults. Single provider works immediately.

### Flags/env for common overrides

```bash
amp-proxy serve --port 9000 --addr 0.0.0.0
# or
LISTEN_PORT=9000 amp-proxy serve
```

### Config file for advanced use (optional)

Only needed for: custom model remaps, API keys, multi-account, provider toggles.

**Location:** auto-detected via `os.UserConfigDir()` → `<config_dir>/amp-proxy/config.yaml`
Override: `AMP_PROXY_CONFIG=/path/to/config.yaml` or `--config` flag.

```yaml
# Only include what you want to change. Everything has defaults.

# Optional: override defaults
server:
  addr: 127.0.0.1
  port: 18317

# Optional: Exa API for web_search tool
exa_api_key: ${EXA_API_KEY}

# Optional: auth token directory (default: ~/.cli-proxy-api)
auth_dir: ~/.cli-proxy-api

# Optional: provider routing strategy
routing: round-robin  # or "fill-first"

# Optional: direct API keys (skip OAuth entirely)
api_keys:
  anthropic:
    - key: ${ANTHROPIC_API_KEY}
  openai:
    - key: ${OPENAI_API_KEY}
      base_url: https://api.openai.com

# Optional: enable/disable providers
providers:
  claude: true     # default: true
  openai: true     # default: true
  gemini: false
  copilot: false

# Optional: custom model remapping (overrides built-in defaults)
model_remaps:
  - from: gemini-3-flash-preview
    to: claude-sonnet-4-6
    provider: anthropic

  - from: gemini-3-flash
    to: claude-sonnet-4-6
    provider: anthropic

  - from: gemini-3-pro
    to: gpt-5.4
    provider: openai

# Optional: fallback for unmapped models
fallback_model:
  name: claude-sonnet-4-6
  provider: anthropic
```

**Key difference from v1 plan:** this config is ~30 lines max, everything is optional, and most users never create it.

---

## CLI Commands

```
amp-proxy serve              # start the proxy (default if no subcommand)
amp-proxy login <provider>   # claude | openai | gemini | copilot | qwen
amp-proxy logout <provider>  # remove saved tokens
amp-proxy status             # show auth status for all providers
amp-proxy config init        # generate commented config.yaml
amp-proxy config validate    # validate config file
amp-proxy version            # print version
```

### Login flow
`amp-proxy login claude` spawns the CLIProxyAPIPlus auth flow for the specified provider. This opens a browser for OAuth or prints a device code. Tokens are saved to `auth_dir`. The running proxy (if any) picks them up on next request (no hot-reload needed — just re-scan the token dir on demand or at a short interval).

### Status output
```
$ amp-proxy status
Provider        Status      Account              Expires
─────────────────────────────────────────────────────────
claude          ✓ active    user@gmail.com        2025-04-15
openai          ✓ active    user@gmail.com        2025-04-12
gemini          ✗ not authed
copilot         ✗ not authed
```

---

## Provider Request Pipeline

```
incoming request
    │
    ├─ [1] is this a provider request? (/api/provider/*, /v1/*, /api/v1/*)
    │       no → ampcode proxy / tool stubs / etc.
    │
    ├─ [2] unsupported provider? (not anthropic/openai)
    │       yes → protocol translate (Google → Anthropic/OpenAI)
    │       no  → pass through
    │
    ├─ [3] request mutations:
    │       ├─ strip cache_control from body (prevents 400 via OAuth)
    │       └─ strip unsupported OpenAI fields (stream_options)
    │
    ├─ [4] reverse-proxy to embedded CLIProxyAPIPlus
    │       ├─ selects credential (round-robin/fill-first)
    │       ├─ injects auth headers
    │       └─ sends to upstream provider
    │
    ├─ [5] response handling:
    │       ├─ success → reverse translate if remapped, forward to client
    │       ├─ 401/429 → retry with different credential
    │       └─ error → clear error message
    │
    └─ [6] if no auth available:
            → HTTP 503: {"error": "not_authenticated", "message": "Run `amp-proxy login claude` to authenticate"}
```

---

## Package Structure (Simplified)

```
amp-proxy/
├── main.go                          # CLI entry, subcommands (serve/login/logout/status)
├── config.go                        # YAML loader (optional file), env expansion, defaults
├── proxy.go                         # HTTP handler, routing
├── gateway.go                       # embedded CLIProxyAPIPlus (Builder/Service/writeConfig)
├── auth.go                          # login/logout/status CLI, token scanning
├── remap.go                         # model mapping, Google protocol translation
├── translate_anthropic.go           # Google ↔ Anthropic
├── translate_openai.go              # Google ↔ OpenAI
├── tool_call_tracker.go             # tool result ID deduplication
├── config.example.yaml
├── go.mod
└── go.sum
```

**Deliberately flat.** No `internal/gateway/`, no `internal/middleware/`, no deep package tree. This is a local dev tool, not a framework.

---

## What's In vs. Out of Scope

### In scope (v1)
- Embed CLIProxyAPIPlus via `cliproxy.Builder`/`Service` for credential management
- `login` / `logout` / `status` CLI subcommands
- Optional YAML config for model remaps, API keys, provider toggles
- `cache_control` stripping (needed to prevent 400s via OAuth route)
- `stream_options` stripping (already exists)
- Reuse `~/.cli-proxy-api/` token dir
- Keep all existing functionality (ampcode proxy, auth redirect, Exa tools, Google remap)

### Out of scope (cut)
- ~~ThinkingProxy middleware~~ — verify if needed first
- ~~Vercel gateway bypass~~ — vibeproxy-specific
- ~~Config hot-reload~~ — restart is fast enough
- ~~Management dashboard~~ — not needed for local tool
- ~~Standalone gin HTTP server~~ — gin is present as a transitive dep of CLIProxyAPIPlus but only listens on localhost

### Future (if proven needed)
- Thinking param injection (only if Amp CLI actually sends `-thinking-N`)
- Config hot-reload (only if users demand it)
- TUI for interactive provider management

---

## Migration Path

### From vibeproxy + amp-proxy (current setup)

1. Build new amp-proxy
2. `amp-proxy status` — sees existing tokens in `~/.cli-proxy-api/` automatically
3. `amp-proxy serve` — works immediately, no config needed
4. Stop vibeproxy — no longer needed

**Zero config migration.** Existing tokens are reused in-place. No copying, no format changes.

### From scratch (new user)

1. `go install github.com/johannhipp/amp-proxy@latest`
2. `amp-proxy login claude` — opens browser, OAuth completes
3. `amp-proxy serve` — ready
4. Point Amp CLI: `amp.url = http://127.0.0.1:18317`

---

## Risks & Mitigations (Revised)

| Risk | Mitigation |
|------|-----------|
| gin pulled in as transitive dep via `cliproxy.Builder` | Acceptable: adds ~10MB, only listens on 127.0.0.1 ephemeral port, never exposed externally. |
| Token refresh races during serving | CLIProxyAPIPlus handles this internally with singleflight |
| Provider down takes out whole proxy | Non-provider routes always work. Provider errors return 503 with clear message. |
| CLIProxyAPIPlus API changes (v6→v7) | Pin to specific version. Adapter layer is thin enough to update. |
| Binary size increase | Acceptable tradeoff for auth management. Measure before/after. |
| Users confused by subcommands | `amp-proxy` without args = `amp-proxy serve` (backwards compatible) |

---

## Build Safety (Development)

- **Worktree:** all dev in `../amp-proxy-v2/` (branch `feat/embed-cliproxy`)
- **Binary:** output as `amp-proxy-v2` (Makefile target), never overwrites `amp-proxy`
- **Ports:** dev/test use `:28317`, never `:18317` or `:8317`
- **Running tmux session:** never touched

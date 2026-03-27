package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

// ProviderGateway manages the embedded CLIProxyAPIPlus service
type ProviderGateway struct {
	service    *cliproxy.Service
	proxy      *httputil.ReverseProxy
	targetURL  string
	port       int
	ready      bool
	mu         sync.RWMutex
	configPath string // generated config file, cleaned up on shutdown
}

// NewProviderGateway creates and configures the embedded CLIProxyAPIPlus service
func NewProviderGateway(appCfg *AppConfig) (*ProviderGateway, error) {
	gw := &ProviderGateway{}

	// Find a free port for the internal CLIProxyAPIPlus server
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("find free port: %w", err)
	}
	gw.port = listener.Addr().(*net.TCPAddr).Port
	listener.Close()

	gw.targetURL = fmt.Sprintf("http://127.0.0.1:%d", gw.port)

	// Write a CLIProxyAPIPlus config file
	configPath, err := gw.writeConfig(appCfg)
	if err != nil {
		return nil, fmt.Errorf("write cliproxy config: %w", err)
	}
	gw.configPath = configPath

	// Load the config through CLIProxyAPIPlus's loader
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("load cliproxy config: %w", err)
	}

	// Override port to our ephemeral port
	cfg.Port = gw.port
	cfg.Host = "127.0.0.1"

	// Build the service
	svc, err := cliproxy.NewBuilder().
		WithConfig(cfg).
		WithConfigPath(configPath).
		Build()
	if err != nil {
		return nil, fmt.Errorf("build cliproxy service: %w", err)
	}
	gw.service = svc

	// Set up reverse proxy to the internal server
	target, _ := url.Parse(gw.targetURL)
	gw.proxy = httputil.NewSingleHostReverseProxy(target)
	gw.proxy.Transport = &http.Transport{
		ResponseHeaderTimeout: 10 * time.Minute,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConnsPerHost:   20,
	}
	gw.proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		slog.Error("provider gateway error", "path", r.URL.Path, "error", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, `{"error":"provider_unavailable","message":"Provider backend error: %s"}`, err.Error())
	}

	return gw, nil
}

// Start starts the embedded CLIProxyAPIPlus service in a background goroutine
func (gw *ProviderGateway) Start(ctx context.Context) error {
	errCh := make(chan error, 1)

	go func() {
		slog.Info("provider gateway starting", "port", gw.port)
		if err := gw.service.Run(ctx); err != nil && ctx.Err() == nil {
			errCh <- err
		}
		close(errCh)
	}()

	// Wait for the service to be ready
	ready := gw.waitForReady(ctx, 15*time.Second)

	// Check for early errors
	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("provider gateway failed to start: %w", err)
		}
	default:
	}

	if !ready {
		return fmt.Errorf("provider gateway did not become ready within timeout")
	}

	gw.mu.Lock()
	gw.ready = true
	gw.mu.Unlock()

	slog.Info("provider gateway ready", "url", gw.targetURL)
	return nil
}

// waitForReady polls the internal server until it responds
func (gw *ProviderGateway) waitForReady(ctx context.Context, timeout time.Duration) bool {
	deadline := time.After(timeout)
	client := &http.Client{Timeout: 500 * time.Millisecond}

	for {
		select {
		case <-ctx.Done():
			return false
		case <-deadline:
			return false
		default:
			resp, err := client.Get(gw.targetURL + "/v1/models")
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode < 500 {
					return true
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
}

// ServeHTTP forwards the request to the embedded CLIProxyAPIPlus
func (gw *ProviderGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	gw.mu.RLock()
	ready := gw.ready
	gw.mu.RUnlock()

	if !ready {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"error":"provider_not_ready","message":"Provider gateway is starting up. Please retry."}`)
		return
	}

	gw.proxy.ServeHTTP(w, r)
}

// Shutdown gracefully stops the embedded service and cleans up
func (gw *ProviderGateway) Shutdown(ctx context.Context) error {
	if gw.configPath != "" {
		os.Remove(gw.configPath)
	}
	if gw.service != nil {
		return gw.service.Shutdown(ctx)
	}
	return nil
}

// writeConfig generates a CLIProxyAPIPlus YAML config file from AppConfig
func (gw *ProviderGateway) writeConfig(appCfg *AppConfig) (string, error) {
	authDir := ExpandHome(appCfg.AuthDir)
	if err := os.MkdirAll(authDir, 0700); err != nil {
		return "", fmt.Errorf("create auth dir: %w", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "port: %d\n", gw.port)
	fmt.Fprintf(&b, "host: 127.0.0.1\n")
	fmt.Fprintf(&b, "auth-dir: %q\n", authDir)
	fmt.Fprintf(&b, "debug: false\n")
	fmt.Fprintf(&b, "usage-statistics-enabled: false\n")
	fmt.Fprintf(&b, "request-retry: %d\n", appCfg.RequestRetry)
	if appCfg.RequestTimeoutMins > 0 {
		fmt.Fprintf(&b, "request-timeout: %q\n", fmt.Sprintf("%dm", appCfg.RequestTimeoutMins))
	}
	if appCfg.MaxRetryCredentials > 0 {
		fmt.Fprintf(&b, "max-retry-credentials: %d\n", appCfg.MaxRetryCredentials)
	}

	// Routing strategy
	if appCfg.Routing != "" {
		fmt.Fprintf(&b, "routing:\n  strategy: %q\n", appCfg.Routing)
	}

	// Disable management panel (not needed for local use)
	fmt.Fprintf(&b, "remote-management:\n  allow-remote: false\n  disable-control-panel: true\n")

	// Amp upstream config — leave empty so CLIProxyAPIPlus doesn't try Amp routing
	// We handle Amp routing ourselves
	fmt.Fprintf(&b, "ampcode:\n  upstream-url: \"\"\n  restrict-management-to-localhost: true\n")

	// Quota exceeded behavior
	fmt.Fprintf(&b, "quota-exceeded:\n  switch-project: true\n  switch-preview-model: true\n")

	// Direct API keys
	if len(appCfg.AnthropicAPIKeys) > 0 {
		fmt.Fprintf(&b, "claude-api-key:\n")
		for _, k := range appCfg.AnthropicAPIKeys {
			fmt.Fprintf(&b, "  - api-key: %q\n", k.Key)
			if k.Priority > 0 {
				fmt.Fprintf(&b, "    priority: %d\n", k.Priority)
			}
		}
	}

	if len(appCfg.OpenAIAPIKeys) > 0 {
		fmt.Fprintf(&b, "codex-api-key:\n")
		for _, k := range appCfg.OpenAIAPIKeys {
			fmt.Fprintf(&b, "  - api-key: %q\n", k.Key)
			if k.BaseURL != "" {
				fmt.Fprintf(&b, "    base-url: %q\n", k.BaseURL)
			}
			if k.Priority > 0 {
				fmt.Fprintf(&b, "    priority: %d\n", k.Priority)
			}
		}
	}

	if len(appCfg.GeminiAPIKeys) > 0 {
		fmt.Fprintf(&b, "gemini-api-key:\n")
		for _, k := range appCfg.GeminiAPIKeys {
			fmt.Fprintf(&b, "  - api-key: %q\n", k.Key)
			if k.Priority > 0 {
				fmt.Fprintf(&b, "    priority: %d\n", k.Priority)
			}
		}
	}

	// OpenAI-compatible providers
	if len(appCfg.OpenAICompatible) > 0 {
		fmt.Fprintf(&b, "openai-compatibility:\n")
		for _, compat := range appCfg.OpenAICompatible {
			fmt.Fprintf(&b, "  - name: %q\n", compat.Name)
			fmt.Fprintf(&b, "    base-url: %q\n", compat.BaseURL)
			if len(compat.Keys) > 0 {
				fmt.Fprintf(&b, "    api-key:\n")
				for _, k := range compat.Keys {
					fmt.Fprintf(&b, "      - api-key: %q\n", k.Key)
				}
			}
		}
	}

	// Provider exclusions (disabled providers)
	var excluded []string
	for provider, enabled := range appCfg.Providers {
		if !enabled {
			tokenType, ok := providerTokenTypes[provider]
			if ok {
				excluded = append(excluded, tokenType)
			}
		}
	}
	if len(excluded) > 0 {
		fmt.Fprintf(&b, "oauth-excluded-models:\n")
		for _, prov := range excluded {
			fmt.Fprintf(&b, "  %s:\n    - \"*\"\n", prov)
		}
	}

	// Write to config dir (survives /tmp cleanup, cleaned up on shutdown)
	configDir, err := os.UserConfigDir()
	if err != nil {
		configDir = authDir
	} else {
		configDir = filepath.Join(configDir, "amp-proxy")
	}
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return "", fmt.Errorf("create config dir: %w", err)
	}
	configPath := filepath.Join(configDir, "cliproxy-internal.yaml")
	if err := os.WriteFile(configPath, []byte(b.String()), 0600); err != nil {
		return "", err
	}

	slog.Debug("wrote cliproxy config", "path", configPath)
	return configPath, nil
}

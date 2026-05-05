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
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	"gopkg.in/yaml.v3"
)

// ProviderGateway manages the embedded CLIProxyAPIPlus service
type ProviderGateway struct {
	service    *cliproxy.Service
	proxy      *httputil.ReverseProxy
	client     *http.Client // for direct requests (remap path); no total timeout for streaming
	targetURL  string
	port       int
	ready      bool
	mu         sync.RWMutex
	configPath string // generated config file, cleaned up on shutdown
}

// NewProviderGateway creates and configures the embedded CLIProxyAPIPlus service
func NewProviderGateway(appCfg *AppConfig) (*ProviderGateway, error) {
	gw := &ProviderGateway{}

	// Find a free port for the internal CLIProxyAPIPlus server.
	// NOTE: There is a TOCTOU race between closing this listener and
	// CLIProxyAPIPlus re-binding to the same port — another process could
	// claim it in between. The CLIProxyAPIPlus SDK does not support
	// accepting a pre-opened listener, so we accept this minor risk.
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

	// Set up shared transport for both the reverse proxy and direct client
	transport := &http.Transport{
		ResponseHeaderTimeout: 10 * time.Minute,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConnsPerHost:   20,
	}

	// Reverse proxy for normal provider requests
	target, _ := url.Parse(gw.targetURL)
	gw.proxy = httputil.NewSingleHostReverseProxy(target)
	gw.proxy.Transport = transport
	gw.proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		slog.Error("provider gateway error", "path", r.URL.Path, "error", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, `{"error":"provider_unavailable","message":"Provider backend error: %s"}`, err.Error())
	}

	// Direct client for remap path — no total timeout so SSE streams aren't killed
	gw.client = &http.Client{Transport: transport}

	return gw, nil
}

// Start starts the embedded CLIProxyAPIPlus service in a background goroutine
func (gw *ProviderGateway) Start(ctx context.Context) error {
	errCh := make(chan error, 1)
	readyCh := make(chan bool, 1)

	go func() {
		slog.Info("provider gateway starting", "port", gw.port)
		if err := gw.service.Run(ctx); err != nil && ctx.Err() == nil {
			errCh <- err
		}
		close(errCh)
	}()

	go func() {
		readyCh <- gw.waitForReady(ctx, 15*time.Second)
	}()

	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("provider gateway failed to start: %w", err)
		}
		return fmt.Errorf("provider gateway exited unexpectedly")
	case ready := <-readyCh:
		if !ready {
			return fmt.Errorf("provider gateway did not become ready within timeout")
		}
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

// Do sends a request through the gateway's shared transport (for remap path).
// Unlike ph.httpClient, this has no total timeout so SSE streams work correctly.
func (gw *ProviderGateway) Do(req *http.Request) (*http.Response, error) {
	return gw.client.Do(req)
}

// IsReady returns whether the gateway is ready to accept requests
func (gw *ProviderGateway) IsReady() bool {
	gw.mu.RLock()
	defer gw.mu.RUnlock()
	return gw.ready
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
		if err := os.Remove(gw.configPath); err != nil && !os.IsNotExist(err) {
			slog.Warn("failed to remove temp cliproxy config", "path", gw.configPath, "error", err)
		}
	}
	if gw.service != nil {
		return gw.service.Shutdown(ctx)
	}
	return nil
}

type cliproxyConfig struct {
	Port                   int                    `yaml:"port"`
	Host                   string                 `yaml:"host"`
	AuthDir                string                 `yaml:"auth-dir"`
	Debug                  bool                   `yaml:"debug"`
	UsageStatisticsEnabled bool                   `yaml:"usage-statistics-enabled"`
	RequestRetry           int                    `yaml:"request-retry"`
	RequestTimeout         string                 `yaml:"request-timeout,omitempty"`
	MaxRetryCredentials    int                    `yaml:"max-retry-credentials,omitempty"`
	Routing                *cliproxyRouting       `yaml:"routing,omitempty"`
	RemoteManagement       cliproxyRemoteMgmt     `yaml:"remote-management"`
	Ampcode                cliproxyAmpcode        `yaml:"ampcode"`
	QuotaExceeded          cliproxyQuotaExceeded  `yaml:"quota-exceeded"`
	ClaudeAPIKey           []cliproxyAPIKey       `yaml:"claude-api-key,omitempty"`
	CodexAPIKey            []cliproxyAPIKey       `yaml:"codex-api-key,omitempty"`
	GeminiAPIKey           []cliproxyAPIKey       `yaml:"gemini-api-key,omitempty"`
	OpenAICompatibility    []cliproxyOpenAICompat `yaml:"openai-compatibility,omitempty"`
	OAuthExcludedModels    map[string][]string    `yaml:"oauth-excluded-models,omitempty"`
}

type cliproxyRouting struct {
	Strategy string `yaml:"strategy"`
}

type cliproxyRemoteMgmt struct {
	AllowRemote         bool `yaml:"allow-remote"`
	DisableControlPanel bool `yaml:"disable-control-panel"`
}

type cliproxyAmpcode struct {
	UpstreamURL                   string                    `yaml:"upstream-url"`
	RestrictManagementToLocalhost bool                      `yaml:"restrict-management-to-localhost"`
	ModelMappings                 []cliproxyAmpModelMapping `yaml:"model-mappings,omitempty"`
	ForceModelMappings            bool                      `yaml:"force-model-mappings,omitempty"`
}

type cliproxyAmpModelMapping struct {
	From  string `yaml:"from"`
	To    string `yaml:"to"`
	Regex bool   `yaml:"regex,omitempty"`
}

type cliproxyQuotaExceeded struct {
	SwitchProject      bool `yaml:"switch-project"`
	SwitchPreviewModel bool `yaml:"switch-preview-model"`
}

type cliproxyAPIKey struct {
	APIKey   string `yaml:"api-key"`
	BaseURL  string `yaml:"base-url,omitempty"`
	Priority int    `yaml:"priority,omitempty"`
}

type cliproxyOpenAICompat struct {
	Name          string           `yaml:"name"`
	BaseURL       string           `yaml:"base-url"`
	APIKeyEntries []cliproxyAPIKey `yaml:"api-key-entries,omitempty"`
}

// writeConfig generates a CLIProxyAPIPlus YAML config file from AppConfig
func (gw *ProviderGateway) writeConfig(appCfg *AppConfig) (string, error) {
	authDir := ExpandHome(appCfg.AuthDir)
	if err := os.MkdirAll(authDir, 0700); err != nil {
		return "", fmt.Errorf("create auth dir: %w", err)
	}

	cfg := cliproxyConfig{
		Port:         gw.port,
		Host:         "127.0.0.1",
		AuthDir:      authDir,
		RequestRetry: appCfg.RequestRetry,
		RemoteManagement: cliproxyRemoteMgmt{
			DisableControlPanel: true,
		},
		Ampcode: cliproxyAmpcode{
			UpstreamURL:                   appCfg.AmpcodeURL,
			RestrictManagementToLocalhost: true,
			ForceModelMappings:            appCfg.AmpcodeForceModelMappings,
		},
		QuotaExceeded: cliproxyQuotaExceeded{
			SwitchProject:      true,
			SwitchPreviewModel: true,
		},
	}

	for _, m := range appCfg.AmpcodeModelMappings {
		cfg.Ampcode.ModelMappings = append(cfg.Ampcode.ModelMappings, cliproxyAmpModelMapping{
			From:  m.From,
			To:    m.To,
			Regex: m.Regex,
		})
	}

	if appCfg.RequestTimeoutMins > 0 {
		cfg.RequestTimeout = fmt.Sprintf("%dm", appCfg.RequestTimeoutMins)
	}
	if appCfg.MaxRetryCredentials > 0 {
		cfg.MaxRetryCredentials = appCfg.MaxRetryCredentials
	}
	if appCfg.Routing != "" {
		cfg.Routing = &cliproxyRouting{Strategy: appCfg.Routing}
	}

	for _, k := range appCfg.AnthropicAPIKeys {
		cfg.ClaudeAPIKey = append(cfg.ClaudeAPIKey, cliproxyAPIKey{
			APIKey: k.Key, Priority: k.Priority,
		})
	}
	for _, k := range appCfg.OpenAIAPIKeys {
		cfg.CodexAPIKey = append(cfg.CodexAPIKey, cliproxyAPIKey{
			APIKey: k.Key, BaseURL: k.BaseURL, Priority: k.Priority,
		})
	}
	for _, k := range appCfg.GeminiAPIKeys {
		cfg.GeminiAPIKey = append(cfg.GeminiAPIKey, cliproxyAPIKey{
			APIKey: k.Key, Priority: k.Priority,
		})
	}

	for _, compat := range appCfg.OpenAICompatible {
		c := cliproxyOpenAICompat{Name: compat.Name, BaseURL: compat.BaseURL}
		for _, k := range compat.Keys {
			c.APIKeyEntries = append(c.APIKeyEntries, cliproxyAPIKey{APIKey: k.Key})
		}
		cfg.OpenAICompatibility = append(cfg.OpenAICompatibility, c)
	}

	excluded := make(map[string][]string)
	for provider, enabled := range appCfg.Providers {
		if !enabled {
			if tokenType, ok := providerTokenTypes[provider]; ok {
				excluded[tokenType] = []string{"*"}
			}
		}
	}
	if len(excluded) > 0 {
		cfg.OAuthExcludedModels = excluded
	}

	data, err := yaml.Marshal(&cfg)
	if err != nil {
		return "", fmt.Errorf("marshal cliproxy config: %w", err)
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
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		return "", err
	}

	slog.Debug("wrote cliproxy config", "path", configPath)
	return configPath, nil
}

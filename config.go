package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// AppConfig holds all amp-proxy configuration
type AppConfig struct {
	// Server settings
	ListenPort int    `yaml:"port"`
	ListenAddr string `yaml:"addr"`
	AmpcodeURL string `yaml:"ampcode_url"`

	// Exa API for web_search and read_web_page
	ExaAPIKey string `yaml:"exa_api_key"`

	// Auth token directory
	AuthDir string `yaml:"auth_dir"`

	// Provider routing
	Routing string `yaml:"routing"` // "round-robin" or "fill-first"

	// Provider enable/disable
	Providers map[string]bool `yaml:"providers"`

	// Model remapping (Google GenAI → supported providers)
	ModelRemaps []ModelRemapConfig `yaml:"model_remaps"`

	// Fallback for unmapped models
	Fallback *ModelRemapConfig `yaml:"fallback"`

	// CLIProxyAPIPlus-specific config overrides
	RequestRetry        int `yaml:"request_retry"`
	RequestTimeoutMins  int `yaml:"request_timeout_mins"`
	MaxRetryCredentials int `yaml:"max_retry_credentials"`

	// Direct API keys (skip OAuth)
	AnthropicAPIKeys []APIKeyConfig `yaml:"anthropic_api_keys"`
	OpenAIAPIKeys    []APIKeyConfig `yaml:"openai_api_keys"`
	GeminiAPIKeys    []APIKeyConfig `yaml:"gemini_api_keys"`

	// OpenAI-compatible providers
	OpenAICompatible []OpenAICompatConfig `yaml:"openai_compatible"`
}

// ModelRemapConfig defines how to remap an unsupported model
type ModelRemapConfig struct {
	From     string `yaml:"from"`
	To       string `yaml:"to"`
	Provider string `yaml:"provider"` // "anthropic" or "openai"
}

// APIKeyConfig holds a direct API key configuration
type APIKeyConfig struct {
	Key      string `yaml:"key"`
	BaseURL  string `yaml:"base_url"`
	Priority int    `yaml:"priority"`
}

// OpenAICompatConfig holds an OpenAI-compatible provider config
type OpenAICompatConfig struct {
	Name    string         `yaml:"name"`
	BaseURL string         `yaml:"base_url"`
	Keys    []APIKeyConfig `yaml:"keys"`
}

// DefaultConfig returns configuration with sensible defaults
func DefaultConfig() *AppConfig {
	return &AppConfig{
		ListenPort: 18317,
		ListenAddr: "127.0.0.1",
		AmpcodeURL: "https://ampcode.com",
		AuthDir:    "~/.cli-proxy-api",
		Routing:    "round-robin",
		Providers: map[string]bool{
			"claude":  true,
			"openai":  true,
			"gemini":  true,
			"copilot": true,
		},
		ModelRemaps: []ModelRemapConfig{
			{From: "gemini-3-flash-preview", To: "claude-sonnet-4-6", Provider: "anthropic"},
			{From: "gemini-3-flash", To: "claude-sonnet-4-6", Provider: "anthropic"},
			{From: "gemini-3-pro", To: "gpt-5.4", Provider: "openai"},
			{From: "gemini-3-pro-image", To: "gpt-image-1", Provider: "openai"},
		},
		Fallback: &ModelRemapConfig{
			To:       "claude-sonnet-4-6",
			Provider: "anthropic",
		},
		RequestRetry:       3,
		RequestTimeoutMins: 10,
	}
}

// LoadConfig loads configuration from the given path, auto-detects, or returns defaults
func LoadConfig(path string) (*AppConfig, error) {
	cfg := DefaultConfig()

	// Determine config path
	if path == "" {
		path = findConfigFile()
	}

	if path == "" {
		// No config file — use defaults + env vars
		applyEnvOverrides(cfg)
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			applyEnvOverrides(cfg)
			return cfg, nil
		}
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	// Expand ${ENV_VAR} references
	expanded := expandEnvVars(string(data))

	if err := yaml.Unmarshal([]byte(expanded), cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	applyEnvOverrides(cfg)
	return cfg, nil
}

// findConfigFile looks for config in standard locations
func findConfigFile() string {
	// Check env var
	if v := os.Getenv("AMP_PROXY_CONFIG"); v != "" {
		if _, err := os.Stat(v); err == nil {
			return v
		}
	}

	// Check os.UserConfigDir()/amp-proxy/config.yaml
	if dir, err := os.UserConfigDir(); err == nil {
		p := filepath.Join(dir, "amp-proxy", "config.yaml")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	// Check ./config.yaml (current directory)
	if _, err := os.Stat("config.yaml"); err == nil {
		return "config.yaml"
	}

	return ""
}

// applyEnvOverrides applies environment variable overrides to config
func applyEnvOverrides(cfg *AppConfig) {
	if v := os.Getenv("EXA_API_KEY"); v != "" {
		cfg.ExaAPIKey = v
	}
}

var envVarRegex = regexp.MustCompile(`\$\{([^}]+)\}`)

// expandEnvVars replaces ${VAR} with the environment variable value
func expandEnvVars(s string) string {
	return envVarRegex.ReplaceAllStringFunc(s, func(match string) string {
		varName := match[2 : len(match)-1]
		if val, ok := os.LookupEnv(varName); ok {
			return val
		}
		return match
	})
}

// ExpandHome expands ~ to the user's home directory
func ExpandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// TargetPathForProvider returns the CLIProxyAPIPlus API path for a provider
func TargetPathForProvider(provider string) string {
	switch provider {
	case "anthropic":
		return "/api/provider/anthropic/v1/messages"
	case "openai":
		return "/api/provider/openai/v1/chat/completions"
	default:
		return "/api/provider/" + provider + "/v1/messages"
	}
}

// FindModelRemap returns the remap config for a given model, or the fallback
func (cfg *AppConfig) FindModelRemap(model string) (ModelRemapConfig, bool) {
	for _, m := range cfg.ModelRemaps {
		if m.From == model {
			return m, true
		}
	}
	if cfg.Fallback != nil {
		return *cfg.Fallback, false
	}
	return ModelRemapConfig{To: "claude-sonnet-4-6", Provider: "anthropic"}, false
}

package main

import (
	"net/http"
	"strings"
)

// Rule represents a proxy rule with matching criteria
type Rule struct {
	Name     string
	Match    func(*http.Request) bool // Matcher function
	Target   string                   // Target URL, empty = reject
	Priority int                      // Higher priority rules evaluated first
}

// Config holds proxy server configuration
type Config struct {
	ListenPort      int
	ListenAddr      string
	DefaultTarget   string // Where non-matched requests go (ampcode.com)
	VibeProxyTarget string // Where LLM provider requests go (vibeproxy)
	ExaAPIKey       string // Exa API key for web search/page reading (optional)
	Rules           []Rule
}

// hasPathPrefix checks if the request path matches a prefix.
func hasPathPrefix(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

// NewDefaultConfig returns default configuration.
// Only LLM provider/streaming paths go to vibeproxy.
// Everything else (auth, threads, settings, GitHub, etc.)
// goes directly to ampcode.com.
func NewDefaultConfig() *Config {
	const (
		ampcode   = "https://ampcode.com"
		vibeproxy = "http://localhost:8317"
	)

	return &Config{
		ListenPort:      18317,
		ListenAddr:      "0.0.0.0",
		DefaultTarget:   ampcode,
		VibeProxyTarget: vibeproxy,
		Rules: []Rule{
			// LLM provider requests -> vibeproxy
			{
				Name: "provider-to-vibeproxy",
				Match: func(r *http.Request) bool {
					return hasPathPrefix(r.URL.Path, "/api/provider") ||
						hasPathPrefix(r.URL.Path, "/v1") ||
						hasPathPrefix(r.URL.Path, "/api/v1")
				},
				Target:   vibeproxy,
				Priority: 100,
			},
		},
	}
}

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthRequestsRedirectToAmpcode(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AmpcodeURL = "https://ampcode.com"
	handler := &ProxyHandler{
		config: cfg,
		httpClient: &http.Client{},
	}

	tests := []struct {
		path             string
		expectedLocation string
	}{
		{"/auth/cli-login", "https://ampcode.com/auth/cli-login"},
		{"/auth/callback?code=abc&state=xyz", "https://ampcode.com/auth/callback?code=abc&state=xyz"},
		{"/api/auth/sign-in?returnTo=%2Fsettings", "https://ampcode.com/api/auth/sign-in?returnTo=%2Fsettings"},
	}

	for _, tt := range tests {
		req := httptest.NewRequest("GET", "http://localhost"+tt.path, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusFound {
			t.Errorf("Path %s: expected 302, got %d", tt.path, w.Code)
		}
		loc := w.Header().Get("Location")
		if loc != tt.expectedLocation {
			t.Errorf("Path %s: expected Location %s, got %s", tt.path, tt.expectedLocation, loc)
		}
	}
}

func TestHealthCheck(t *testing.T) {
	cfg := DefaultConfig()
	handler := &ProxyHandler{
		config: cfg,
		httpClient: &http.Client{},
	}

	req := httptest.NewRequest("GET", "http://localhost/healthz", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if w.Body.String() != `{"status":"ok"}` {
		t.Errorf("unexpected body: %s", w.Body.String())
	}
}

func TestGetUserFreeTierStatus(t *testing.T) {
	cfg := DefaultConfig()
	handler := &ProxyHandler{
		config: cfg,
		httpClient: &http.Client{},
	}

	req := httptest.NewRequest("GET", "http://localhost/api/internal?getUserFreeTierStatus", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if w.Body.String() != `{"ok":true,"result":{"canUseAmpFree":true,"isDailyGrantEnabled":true}}` {
		t.Errorf("unexpected body: %s", w.Body.String())
	}
}

func TestIsProviderRequest(t *testing.T) {
	tests := []struct {
		path   string
		expect bool
	}{
		{"/api/provider/anthropic", true},
		{"/api/provider/openai/v1", true},
		{"/api/provider/google", true},
		{"/v1/messages", true},
		{"/api/v1/chat/completions", true},
		{"/api/threads", false},
		{"/auth/cli-login", false},
		{"/api/internal", false},
		{"/settings", false},
	}

	for _, tt := range tests {
		req := httptest.NewRequest("GET", "http://localhost"+tt.path, nil)
		got := isProviderRequest(req)
		if got != tt.expect {
			t.Errorf("isProviderRequest(%s): expected %v, got %v", tt.path, tt.expect, got)
		}
	}
}

func TestStripCacheControl(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
		changed  bool
	}{
		{
			name:     "removes top-level cache_control",
			input:    `{"model":"test","cache_control":{"type":"ephemeral"},"max_tokens":100}`,
			expected: `{"max_tokens":100,"model":"test"}`,
			changed:  true,
		},
		{
			name:     "removes nested cache_control",
			input:    `{"messages":[{"role":"user","content":"hi","cache_control":{"type":"ephemeral"}}]}`,
			expected: `{"messages":[{"content":"hi","role":"user"}]}`,
			changed:  true,
		},
		{
			name:     "no change when no cache_control",
			input:    `{"model":"test","max_tokens":100}`,
			expected: `{"model":"test","max_tokens":100}`,
			changed:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m map[string]interface{}
			if err := json.Unmarshal([]byte(tt.input), &m); err != nil {
				t.Fatal(err)
			}
			changed := removeCacheControl(m)
			if changed != tt.changed {
				t.Errorf("removeCacheControl changed=%v, expected %v", changed, tt.changed)
			}
		})
	}
}

func TestConfigDefaults(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.ListenPort != 18317 {
		t.Errorf("expected default port 18317, got %d", cfg.ListenPort)
	}
	if cfg.ListenAddr != "127.0.0.1" {
		t.Errorf("expected default addr 127.0.0.1, got %s", cfg.ListenAddr)
	}
	if cfg.AmpcodeURL != "https://ampcode.com" {
		t.Errorf("expected default ampcode URL, got %s", cfg.AmpcodeURL)
	}
	if cfg.AuthDir != "~/.cli-proxy-api" {
		t.Errorf("expected default auth dir ~/.cli-proxy-api, got %s", cfg.AuthDir)
	}
	if len(cfg.ModelRemaps) != 4 {
		t.Errorf("expected 4 default model remaps, got %d", len(cfg.ModelRemaps))
	}
}

func TestFindModelRemap(t *testing.T) {
	cfg := DefaultConfig()

	tests := []struct {
		model      string
		wantTo     string
		wantProv   string
		wantExact  bool
	}{
		{"gemini-3-flash-preview", "claude-sonnet-4-6", "anthropic", true},
		{"gemini-3-pro", "gpt-5.4", "openai", true},
		{"unknown-model", "claude-sonnet-4-6", "anthropic", false},
	}

	for _, tt := range tests {
		remap, exact := cfg.FindModelRemap(tt.model)
		if remap.To != tt.wantTo {
			t.Errorf("FindModelRemap(%s): expected to=%s, got %s", tt.model, tt.wantTo, remap.To)
		}
		if remap.Provider != tt.wantProv {
			t.Errorf("FindModelRemap(%s): expected provider=%s, got %s", tt.model, tt.wantProv, remap.Provider)
		}
		if exact != tt.wantExact {
			t.Errorf("FindModelRemap(%s): expected exact=%v, got %v", tt.model, tt.wantExact, exact)
		}
	}
}

func TestExpandEnvVars(t *testing.T) {
	t.Setenv("TEST_VAR", "hello")
	result := expandEnvVars("key: ${TEST_VAR}")
	if result != "key: hello" {
		t.Errorf("expected 'key: hello', got %q", result)
	}

	// Unset var should remain as-is
	result = expandEnvVars("key: ${NONEXISTENT_VAR}")
	if result != "key: ${NONEXISTENT_VAR}" {
		t.Errorf("expected unchanged, got %q", result)
	}
}

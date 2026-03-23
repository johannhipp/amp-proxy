package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestConfig(vibeproxyURL, ampcodeURL string) *Config {
	config := NewDefaultConfig()
	config.DefaultTarget = ampcodeURL
	config.VibeProxyTarget = vibeproxyURL
	for i := range config.Rules {
		switch config.Rules[i].Name {
		case "provider-to-vibeproxy":
			config.Rules[i].Target = vibeproxyURL
		}
	}
	return config
}

func TestProviderRequestsGoToVibeProxy(t *testing.T) {
	vibeproxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("vibeproxy"))
	}))
	defer vibeproxy.Close()

	ampcode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ampcode"))
	}))
	defer ampcode.Close()

	config := newTestConfig(vibeproxy.URL, ampcode.URL)
	handler := NewProxyHandler(config)

	paths := []string{
		"/api/provider/anthropic",
		"/api/provider/openai/v1",
		"/api/provider/google",
		"/v1/messages",
		"/api/v1/chat/completions",
	}

	for _, path := range paths {
		req := httptest.NewRequest("POST", "http://localhost"+path, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Body.String() != "vibeproxy" {
			t.Errorf("Path %s: expected vibeproxy, got %s (status %d)", path, w.Body.String(), w.Code)
		}
	}
}

func TestAuthRequestsRedirectToAmpcode(t *testing.T) {
	config := NewDefaultConfig()
	config.DefaultTarget = "https://ampcode.com"
	handler := NewProxyHandler(config)

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

func TestNonAuthDefaultRequestsGoToAmpcode(t *testing.T) {
	vibeproxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("vibeproxy"))
	}))
	defer vibeproxy.Close()

	ampcode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ampcode"))
	}))
	defer ampcode.Close()

	config := newTestConfig(vibeproxy.URL, ampcode.URL)
	handler := NewProxyHandler(config)

	paths := []string{
		"/api/internal/github-auth-status",
		"/api/internal/github-proxy/repos",
	}

	for _, path := range paths {
		req := httptest.NewRequest("GET", "http://localhost"+path, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Body.String() != "ampcode" {
			t.Errorf("Path %s: expected ampcode, got %s (status %d)", path, w.Body.String(), w.Code)
		}
	}
}

func TestDefaultRequestsGoToAmpcode(t *testing.T) {
	vibeproxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("vibeproxy"))
	}))
	defer vibeproxy.Close()

	ampcode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ampcode"))
	}))
	defer ampcode.Close()

	config := newTestConfig(vibeproxy.URL, ampcode.URL)
	handler := NewProxyHandler(config)

	// Everything that's not an LLM provider path goes to ampcode
	paths := []string{
		"/api/threads",
		"/api/threads/sync",
		"/api/session",
		"/api/sessions",
		"/api/telemetry",
		"/api/events",
		"/api/attachments",
		"/api/durable-thread-workers",
		"/api/internal",
		"/threads/T-some-thread-id",
		"/settings",
	}

	for _, path := range paths {
		req := httptest.NewRequest("GET", "http://localhost"+path, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Body.String() != "ampcode" {
			t.Errorf("Path %s: expected ampcode, got %s (status %d)", path, w.Body.String(), w.Code)
		}
	}
}

func TestFindTarget(t *testing.T) {
	config := NewDefaultConfig()
	handler := NewProxyHandler(config)

	tests := []struct {
		path   string
		expect string
	}{
		// LLM paths -> vibeproxy
		{"/api/provider/anthropic", config.VibeProxyTarget},
		{"/api/provider/openai/v1", config.VibeProxyTarget},
		{"/v1/messages", config.VibeProxyTarget},
		{"/api/v1/chat/completions", config.VibeProxyTarget},
		// Everything else -> ampcode
		{"/auth/cli-login", config.DefaultTarget},
		{"/api/auth/sign-in", config.DefaultTarget},
		{"/api/internal/github-auth-status", config.DefaultTarget},
		{"/api/threads", config.DefaultTarget},
		{"/api/session", config.DefaultTarget},
		{"/threads/T-abc-123", config.DefaultTarget},
		{"/settings", config.DefaultTarget},
	}

	for _, tt := range tests {
		req := httptest.NewRequest("GET", "http://localhost"+tt.path, nil)
		target := handler.findTarget(req)

		if target != tt.expect {
			t.Errorf("Path %s: expected %s, got %s", tt.path, tt.expect, target)
		}
	}
}

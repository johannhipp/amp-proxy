package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// ProxyHandler handles HTTP request routing
type ProxyHandler struct {
	config     *AppConfig
	gateway    *ProviderGateway
	ampProxy   *httputil.ReverseProxy
	httpClient *http.Client
	requestID  atomic.Uint64
	metrics    struct {
		totalRequests atomic.Uint64
		providerReqs  atomic.Uint64
		ampcodeReqs   atomic.Uint64
		remapReqs     atomic.Uint64
		exaReqs       atomic.Uint64
		errors        atomic.Uint64
	}
}

// NewProxyHandler creates a new proxy handler
func NewProxyHandler(config *AppConfig) *ProxyHandler {
	handler := &ProxyHandler{
		config: config,
		httpClient: &http.Client{
			Timeout: 5 * time.Minute,
		},
	}

	// Set up ampcode reverse proxy
	ampcodeURL, err := url.Parse(config.AmpcodeURL)
	if err != nil {
		slog.Error("invalid ampcode URL", "url", config.AmpcodeURL, "error", err)
		ampcodeURL, _ = url.Parse("https://ampcode.com")
	}

	ampProxy := httputil.NewSingleHostReverseProxy(ampcodeURL)
	originalDirector := ampProxy.Director
	ampProxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = ampcodeURL.Host
	}
	ampProxy.Transport = &http.Transport{
		TLSClientConfig:       &tls.Config{},
		ResponseHeaderTimeout: 5 * time.Minute,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConnsPerHost:   10,
	}
	ampProxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		slog.Error("ampcode proxy error", "method", r.Method, "path", r.URL.Path, "error", err)
		handler.metrics.errors.Add(1)
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, `{"error":"proxy_error","message":"%s"}`, err.Error())
	}
	handler.ampProxy = ampProxy

	return handler
}

// responseRecorder wraps http.ResponseWriter to capture status code and size
type responseRecorder struct {
	http.ResponseWriter
	statusCode int
	bytes      int64
	errBody    []byte
}

func (rr *responseRecorder) WriteHeader(code int) {
	rr.statusCode = code
	rr.ResponseWriter.WriteHeader(code)
}

func (rr *responseRecorder) Write(b []byte) (int, error) {
	n, err := rr.ResponseWriter.Write(b)
	rr.bytes += int64(n)
	if rr.statusCode >= 300 {
		rr.errBody = append(rr.errBody, b...)
	}
	return n, err
}

func (rr *responseRecorder) Flush() {
	if f, ok := rr.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// ServeHTTP implements http.Handler interface
func (ph *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	reqID := ph.requestID.Add(1)
	start := time.Now()

	// Health check
	if r.URL.Path == "/healthz" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"status":"ok"}`)
		return
	}

	// Metrics
	if r.URL.Path == "/metrics" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]uint64{
			"total_requests":    ph.metrics.totalRequests.Load(),
			"provider_requests": ph.metrics.providerReqs.Load(),
			"ampcode_requests":  ph.metrics.ampcodeReqs.Load(),
			"remap_requests":    ph.metrics.remapReqs.Load(),
			"exa_requests":      ph.metrics.exaReqs.Load(),
			"errors":            ph.metrics.errors.Load(),
		})
		return
	}

	ph.metrics.totalRequests.Add(1)
	ph.logRequest(reqID, r)

	// Auth redirects — send browser directly to ampcode.com
	if hasPathPrefix(r.URL.Path, "/auth") || hasPathPrefix(r.URL.Path, "/api/auth") {
		redirectURL := ph.config.AmpcodeURL + r.URL.RequestURI()
		slog.Info("redirect", "reqID", reqID, "from", r.URL.RequestURI(), "to", redirectURL)
		ph.metrics.ampcodeReqs.Add(1)
		http.Redirect(w, r, redirectURL, http.StatusFound)
		return
	}

	// Fake free-tier status
	if r.URL.Path == "/api/internal" && r.URL.RawQuery == "getUserFreeTierStatus" {
		slog.Info("fake getUserFreeTierStatus", "reqID", reqID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"ok":true,"result":{"canUseAmpFree":true,"isDailyGrantEnabled":true}}`)
		return
	}

	// Tool stubs
	if r.URL.Path == "/api/internal" && (r.URL.RawQuery == "webSearch2" || r.URL.RawQuery == "extractWebPageContent") {
		ph.handleToolStub(w, r, reqID)
		return
	}

	// Model remapping: intercept unsupported provider requests and translate
	if provider, ok := isUnsupportedProviderRequest(r); ok {
		if model, streaming, isGoogle := parseGoogleProviderRequest(r); isGoogle {
			remap, isExplicit := ph.config.FindModelRemap(model)
			slog.Info("remap", "reqID", reqID, "from", provider+"/"+model, "to", remap.Provider+"/"+remap.To)
			ph.metrics.remapReqs.Add(1)
			ph.handleRemappedRequest(w, r, reqID, model, streaming, remap, isExplicit)
			return
		}
		slog.Warn("unsupported provider, forwarding to gateway", "reqID", reqID, "provider", provider)
	}

	// Provider requests → embedded CLIProxyAPIPlus
	if isProviderRequest(r) {
		if ph.gateway == nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"error":"provider_not_ready","message":"Provider gateway is starting up. Please retry."}`)
			return
		}
		ph.metrics.providerReqs.Add(1)

		// Strip unsupported OpenAI fields before forwarding
		if strings.Contains(r.URL.Path, "/openai/") {
			if err := stripOpenAIUnsupportedFields(r); err != nil {
				slog.Warn("failed to strip openai fields", "reqID", reqID, "error", err)
			}
		}

		// Strip cache_control from request bodies (prevents 400 via OAuth route)
		if r.Method == "POST" && r.Body != nil {
			stripCacheControl(r)
		}

		slog.Info("route", "reqID", reqID, "label", "PROVIDER", "method", r.Method, "path", r.URL.Path)

		rec := &responseRecorder{ResponseWriter: w, statusCode: 200}
		ph.gateway.ServeHTTP(rec, r)

		elapsed := time.Since(start)
		logAttrs := []any{"reqID", reqID, "label", "PROVIDER", "status", rec.statusCode, "statusText", http.StatusText(rec.statusCode), "bytes", rec.bytes, "elapsed", elapsed.Round(time.Millisecond)}
		if len(rec.errBody) > 0 {
			logAttrs = append(logAttrs, "body", string(rec.errBody))
		}
		slog.Info("response", logAttrs...)
		return
	}

	// Everything else → ampcode.com
	ph.metrics.ampcodeReqs.Add(1)
	slog.Info("route", "reqID", reqID, "label", "AMPCODE", "method", r.Method, "path", r.URL.Path)

	rec := &responseRecorder{ResponseWriter: w, statusCode: 200}
	ph.ampProxy.ServeHTTP(rec, r)

	elapsed := time.Since(start)
	logAttrs := []any{"reqID", reqID, "label", "AMPCODE", "status", rec.statusCode, "statusText", http.StatusText(rec.statusCode), "bytes", rec.bytes, "elapsed", elapsed.Round(time.Millisecond)}
	if len(rec.errBody) > 0 {
		logAttrs = append(logAttrs, "body", string(rec.errBody))
	}
	slog.Info("response", logAttrs...)
}

// isProviderRequest checks if this is a provider/LLM request
func isProviderRequest(r *http.Request) bool {
	return hasPathPrefix(r.URL.Path, "/api/provider") ||
		hasPathPrefix(r.URL.Path, "/v1") ||
		hasPathPrefix(r.URL.Path, "/api/v1")
}

// hasPathPrefix checks if the request path matches a prefix
func hasPathPrefix(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

// stripCacheControl removes cache_control keys from request JSON bodies
// This prevents 400 errors when requests go through the OAuth route
func stripCacheControl(r *http.Request) {
	if r.Body == nil {
		return
	}
	ct := r.Header.Get("Content-Type")
	if !strings.Contains(ct, "json") {
		return
	}

	body, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		r.Body = io.NopCloser(bytes.NewReader(body))
		return
	}

	var m map[string]interface{}
	if err := json.Unmarshal(body, &m); err != nil {
		r.Body = io.NopCloser(bytes.NewReader(body))
		return
	}

	changed := removeCacheControl(m)
	if !changed {
		r.Body = io.NopCloser(bytes.NewReader(body))
		return
	}

	modified, err := json.Marshal(m)
	if err != nil {
		r.Body = io.NopCloser(bytes.NewReader(body))
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(modified))
	r.ContentLength = int64(len(modified))
}

// removeCacheControl recursively removes cache_control keys from a JSON structure
func removeCacheControl(v interface{}) bool {
	changed := false
	switch val := v.(type) {
	case map[string]interface{}:
		if _, ok := val["cache_control"]; ok {
			delete(val, "cache_control")
			changed = true
		}
		for _, child := range val {
			if removeCacheControl(child) {
				changed = true
			}
		}
	case []interface{}:
		for _, item := range val {
			if removeCacheControl(item) {
				changed = true
			}
		}
	}
	return changed
}

// stripOpenAIUnsupportedFields removes unsupported fields from OpenAI request bodies
func stripOpenAIUnsupportedFields(r *http.Request) error {
	body, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		return err
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		r.Body = io.NopCloser(bytes.NewReader(body))
		return nil
	}
	changed := false
	for _, key := range []string{"stream_options"} {
		if _, ok := m[key]; ok {
			delete(m, key)
			changed = true
		}
	}
	if !changed {
		r.Body = io.NopCloser(bytes.NewReader(body))
		return nil
	}
	modified, err := json.Marshal(m)
	if err != nil {
		r.Body = io.NopCloser(bytes.NewReader(body))
		return err
	}
	r.Body = io.NopCloser(bytes.NewReader(modified))
	r.ContentLength = int64(len(modified))
	return nil
}

// handleToolStub intercepts server-side tool calls
func (ph *ProxyHandler) handleToolStub(w http.ResponseWriter, r *http.Request, reqID uint64) {
	method := r.URL.RawQuery

	body, err := io.ReadAll(r.Body)
	if err != nil {
		slog.Error("tool: failed to read body", "reqID", reqID, "method", method, "error", err)
		ph.metrics.errors.Add(1)
		ph.writeToolError(w, "proxy_error", "failed to read request body")
		return
	}

	var req struct {
		Params struct {
			Objective  string `json:"objective"`
			URL        string `json:"url"`
			MaxResults int    `json:"maxResults"`
		} `json:"params"`
	}
	json.Unmarshal(body, &req)

	if ph.config.ExaAPIKey == "" {
		slog.Info("tool: no EXA_API_KEY, returning stub", "reqID", reqID, "method", method)
		ph.writeToolResult(w, map[string]any{"excerpts": []string{"This tool is not available (EXA_API_KEY not set)"}})
		return
	}

	ph.metrics.exaReqs.Add(1)

	switch method {
	case "webSearch2":
		ph.handleExaSearch(w, reqID, req.Params.Objective, req.Params.MaxResults)
	case "extractWebPageContent":
		ph.handleExaContents(w, reqID, req.Params.URL, req.Params.Objective)
	}
}

func (ph *ProxyHandler) handleExaSearch(w http.ResponseWriter, reqID uint64, objective string, maxResults int) {
	if maxResults <= 0 {
		maxResults = 5
	}
	slog.Info("exa search", "reqID", reqID, "objective", objective, "maxResults", maxResults)

	exaReq := map[string]any{
		"query":       objective,
		"type":        "auto",
		"num_results": maxResults,
		"contents": map[string]any{
			"text": map[string]any{"max_characters": 10000},
		},
	}

	result, err := ph.callExa("/search", exaReq)
	if err != nil {
		slog.Error("exa search error", "reqID", reqID, "error", err)
		ph.metrics.errors.Add(1)
		ph.writeToolError(w, "exa_error", err.Error())
		return
	}

	md := ph.formatSearchResults(result)
	slog.Info("exa search returned", "reqID", reqID, "resultCount", len(md))
	ph.writeToolResult(w, map[string]any{
		"results":                 md,
		"showParallelAttribution": false,
	})
}

func (ph *ProxyHandler) handleExaContents(w http.ResponseWriter, reqID uint64, pageURL string, objective string) {
	slog.Info("exa contents", "reqID", reqID, "url", pageURL, "objective", objective)

	exaReq := map[string]any{
		"urls": []string{pageURL},
		"text": map[string]any{"max_characters": 20000},
	}

	result, err := ph.callExa("/contents", exaReq)
	if err != nil {
		slog.Error("exa contents error", "reqID", reqID, "error", err)
		ph.metrics.errors.Add(1)
		ph.writeToolError(w, "exa_error", err.Error())
		return
	}

	md := ph.formatContentsResults(result)
	slog.Info("exa contents returned", "reqID", reqID, "chars", len(md))
	ph.writeToolResult(w, map[string]any{"excerpts": []string{md}})
}

func (ph *ProxyHandler) callExa(endpoint string, payload map[string]any) (map[string]any, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", "https://api.exa.ai"+endpoint, strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", ph.config.ExaAPIKey)

	resp, err := ph.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("exa request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("exa returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result map[string]any
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return result, nil
}

func (ph *ProxyHandler) formatSearchResults(result map[string]any) []map[string]string {
	results, ok := result["results"].([]any)
	if !ok || len(results) == 0 {
		return nil
	}

	var out []map[string]string
	for _, r := range results {
		item, ok := r.(map[string]any)
		if !ok {
			continue
		}
		title, _ := item["title"].(string)
		url, _ := item["url"].(string)
		text, _ := item["text"].(string)
		if len(text) > 2000 {
			text = text[:2000] + "..."
		}
		out = append(out, map[string]string{"title": title, "url": url, "text": text})
	}
	return out
}

func (ph *ProxyHandler) formatContentsResults(result map[string]any) string {
	results, ok := result["results"].([]any)
	if !ok || len(results) == 0 {
		return "Could not extract content from the page."
	}

	item, ok := results[0].(map[string]any)
	if !ok {
		return "Could not extract content from the page."
	}

	title, _ := item["title"].(string)
	url, _ := item["url"].(string)
	text, _ := item["text"].(string)

	var sb strings.Builder
	if title != "" {
		fmt.Fprintf(&sb, "# %s\n", title)
	}
	if url != "" {
		fmt.Fprintf(&sb, "URL: %s\n\n", url)
	}
	if text != "" {
		sb.WriteString(text)
	} else {
		sb.WriteString("No text content extracted.")
	}
	return sb.String()
}

func (ph *ProxyHandler) writeToolResult(w http.ResponseWriter, result any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

func (ph *ProxyHandler) writeToolError(w http.ResponseWriter, code string, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": map[string]string{"code": code, "message": message}})
}

// logRequest logs detailed request information
func (ph *ProxyHandler) logRequest(reqID uint64, r *http.Request) {
	slog.Info("request", "reqID", reqID, "method", r.Method, "path", r.URL.Path+formatQuery(r.URL.RawQuery))

	for name, values := range r.Header {
		for _, v := range values {
			display := v
			if len(display) > 120 {
				display = display[:60] + "..." + display[len(display)-20:]
			}
			lower := strings.ToLower(name)
			if lower == "authorization" || lower == "cookie" {
				if idx := strings.Index(display, " "); idx > 0 {
					scheme := display[:idx]
					display = scheme + " [REDACTED len=" + fmt.Sprintf("%d", len(v)-idx-1) + "]"
				} else {
					display = "[REDACTED len=" + fmt.Sprintf("%d", len(v)) + "]"
				}
			}
			slog.Debug("header", "reqID", reqID, "name", name, "value", display)
		}
	}

	if r.ContentLength > 0 {
		slog.Debug("body", "reqID", reqID, "bytes", r.ContentLength, "contentType", r.Header.Get("Content-Type"))
	}

	if r.Body != nil && r.ContentLength > 0 && r.ContentLength <= 64*1024 &&
		strings.Contains(r.Header.Get("Content-Type"), "json") {
		body, err := io.ReadAll(r.Body)
		if err == nil {
			preview := string(body)
			if len(preview) > 500 {
				preview = preview[:500] + fmt.Sprintf("... (%d bytes total)", len(body))
			}
			slog.Debug("json body", "reqID", reqID, "preview", preview)
			r.Body = io.NopCloser(strings.NewReader(string(body)))
		}
	}
}

// Start starts the proxy server
func (ph *ProxyHandler) Start(ctx context.Context) error {
	// Start the embedded provider gateway
	gw, err := NewProviderGateway(ph.config)
	if err != nil {
		return fmt.Errorf("create provider gateway: %w", err)
	}
	ph.gateway = gw

	if err := gw.Start(ctx); err != nil {
		return fmt.Errorf("start provider gateway: %w", err)
	}

	addr := net.JoinHostPort(ph.config.ListenAddr, fmt.Sprintf("%d", ph.config.ListenPort))

	slog.Info("amp-proxy starting",
		"listen", addr,
		"ampcode", ph.config.AmpcodeURL,
		"provider_gateway", gw.targetURL,
		"auth_dir", ExpandHome(ph.config.AuthDir),
		"exa", ph.config.ExaAPIKey != "",
	)

	slog.Info("routing rules")
	slog.Info("rule", "priority", "100", "name", "provider", "match", "/api/provider/*, /v1/*, /api/v1/*", "target", "provider-gateway")
	slog.Info("rule", "priority", "90", "name", "auth", "match", "/auth/*, /api/auth/*", "target", "302→ampcode.com")
	slog.Info("rule", "priority", "80", "name", "tools", "match", "/api/internal?{web*,get*}", "target", "exa/stub")
	slog.Info("rule", "priority", "-", "name", "default", "target", "ampcode.com")

	// Print model remaps
	for _, m := range ph.config.ModelRemaps {
		slog.Info("model remap", "from", m.From, "to", m.Provider+"/"+m.To)
	}
	if ph.config.Fallback != nil {
		slog.Info("model remap", "from", "(fallback)", "to", ph.config.Fallback.Provider+"/"+ph.config.Fallback.To)
	}

	slog.Info("waiting for requests")

	srv := &http.Server{
		Addr:    addr,
		Handler: ph,
	}

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		slog.Info("shutting down...")

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		// Shutdown provider gateway
		if ph.gateway != nil {
			if err := ph.gateway.Shutdown(shutdownCtx); err != nil {
				slog.Error("provider gateway shutdown error", "error", err)
			}
		}

		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
		slog.Info("stopped")
		return nil
	}
}

func formatQuery(q string) string {
	if q == "" {
		return ""
	}
	return "?" + q
}

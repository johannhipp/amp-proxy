package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// ProxyHandler handles HTTP request routing
type ProxyHandler struct {
	config     *Config
	proxies    map[string]*httputil.ReverseProxy
	httpClient *http.Client
	requestID  atomic.Uint64
}

// NewProxyHandler creates a new proxy handler
func NewProxyHandler(config *Config) *ProxyHandler {
	handler := &ProxyHandler{
		config:  config,
		proxies: make(map[string]*httputil.ReverseProxy),
		httpClient: &http.Client{
			Timeout: 5 * time.Minute, // Long timeout for streaming LLM responses
		},
	}

	// Collect all unique targets
	targets := make(map[string]bool)
	targets[config.DefaultTarget] = true
	targets[config.VibeProxyTarget] = true
	for _, rule := range config.Rules {
		if rule.Target != "" {
			targets[rule.Target] = true
		}
	}

	for target := range targets {
		targetURL, err := url.Parse(target)
		if err != nil {
			log.Printf("Invalid target URL %s: %v\n", target, err)
			continue
		}
		proxy := httputil.NewSingleHostReverseProxy(targetURL)

		// For HTTPS targets, set the Host header to the target host
		// so TLS and virtual hosting work correctly.
		originalDirector := proxy.Director
		proxy.Director = func(req *http.Request) {
			originalDirector(req)
			req.Host = targetURL.Host
		}

		// Use a transport that supports HTTPS
		proxy.Transport = &http.Transport{
			TLSClientConfig:       &tls.Config{},
			ResponseHeaderTimeout: 5 * time.Minute, // Don't wait forever for upstream
			IdleConnTimeout:       90 * time.Second,
			MaxIdleConnsPerHost:   10,
		}

		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("PROXY ERROR for %s %s -> %s: %v\n", r.Method, r.URL.Path, target, err)
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, `{"error":"proxy_error","message":"%s"}`, err.Error())
		}

		handler.proxies[target] = proxy
	}

	return handler
}

// responseRecorder wraps http.ResponseWriter to capture status code and size
type responseRecorder struct {
	http.ResponseWriter
	statusCode int
	bytes      int64
}

func (rr *responseRecorder) WriteHeader(code int) {
	rr.statusCode = code
	rr.ResponseWriter.WriteHeader(code)
}

func (rr *responseRecorder) Write(b []byte) (int, error) {
	n, err := rr.ResponseWriter.Write(b)
	rr.bytes += int64(n)
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

	// Log full request details
	ph.logRequest(reqID, r)

	// Browser auth paths: redirect to ampcode.com instead of reverse-proxying.
	// OAuth flows set state/nonce cookies on the origin domain. Reverse-proxying
	// keeps the browser on localhost:18317 but ampcode's callback goes to
	// ampcode.com, causing a domain mismatch. A 302 redirect sends the browser
	// to ampcode.com directly so the entire OAuth round-trip stays on one domain.
	if hasPathPrefix(r.URL.Path, "/auth") || hasPathPrefix(r.URL.Path, "/api/auth") {
		redirectURL := ph.config.DefaultTarget + r.URL.RequestURI()
		log.Printf("[%04d] REDIRECT %s -> %s\n", reqID, r.URL.RequestURI(), redirectURL)
		http.Redirect(w, r, redirectURL, http.StatusFound)
		return
	}

	// Fake free-tier status so server-side tools (web_search, read_web_page) aren't blocked
	if r.URL.Path == "/api/internal" && r.URL.RawQuery == "getUserFreeTierStatus" {
		log.Printf("[%04d] FAKE     getUserFreeTierStatus -> canUseAmpFree:true\n", reqID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"ok":true,"result":{"canUseAmpFree":true,"isDailyGrantEnabled":true}}`)
		return
	}

	// Stub server-side tool calls (webSearch2, extractWebPageContent)
	if r.URL.Path == "/api/internal" && (r.URL.RawQuery == "webSearch2" || r.URL.RawQuery == "extractWebPageContent") {
		ph.handleToolStub(w, r, reqID)
		return
	}

	// Model remapping: intercept unsupported provider requests and translate them
	if provider, ok := isUnsupportedProviderRequest(r); ok {
		if model, streaming, isGoogle := parseGoogleProviderRequest(r); isGoogle {
			mapping, isExplicit := findMapping(model)
			log.Printf("[%04d] REMAP    %s/%s -> %s/%s\n", reqID, provider, model, mapping.TargetProvider, mapping.TargetModel)
			ph.handleRemappedRequest(w, r, reqID, model, streaming, mapping, isExplicit)
			return
		}
		// Non-Google unsupported provider — log warning, fall through to default
		log.Printf("[%04d] WARNING  unsupported provider %q, forwarding to vibeproxy as-is\n", reqID, provider)
	}

	target := ph.findTarget(r)

	if target == "" {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprintf(w, `{"error": "forbidden", "message": "request blocked by proxy rules"}`)
		log.Printf("[%04d] BLOCKED  %s %s (no matching target)\n", reqID, r.Method, r.URL.Path)
		return
	}

	proxy, ok := ph.proxies[target]
	if !ok {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, `{"error": "bad_gateway", "message": "target not available"}`)
		log.Printf("[%04d] ERROR    no proxy configured for target %s\n", reqID, target)
		return
	}

	label := "AMPCODE"
	if target == ph.config.VibeProxyTarget {
		label = "VIBEPROXY"
	}

	// Find which rule matched
	ruleName := ph.findMatchedRule(r)
	log.Printf("[%04d] ROUTE    %-9s %s %s -> %s (rule: %s)\n",
		reqID, label, r.Method, r.URL.Path, target, ruleName)

	// Wrap response writer to capture status/size
	rec := &responseRecorder{ResponseWriter: w, statusCode: 200}
	proxy.ServeHTTP(rec, r)

	elapsed := time.Since(start)
	log.Printf("[%04d] RESPONSE %-9s %d %s (%d bytes, %s)\n",
		reqID, label, rec.statusCode, http.StatusText(rec.statusCode), rec.bytes, elapsed.Round(time.Millisecond))
}

// handleToolStub intercepts server-side tool calls and routes them to Exa or returns stubs
func (ph *ProxyHandler) handleToolStub(w http.ResponseWriter, r *http.Request, reqID uint64) {
	method := r.URL.RawQuery

	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("[%04d] TOOL     %s: failed to read body: %v\n", reqID, method, err)
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
		log.Printf("[%04d] TOOL     %s: no EXA_API_KEY, returning stub\n", reqID, method)
		ph.writeToolResult(w, map[string]any{"excerpts": []string{"This tool is not available (EXA_API_KEY not set)"}})
		return
	}

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
	log.Printf("[%04d] EXA      webSearch2 objective=%q maxResults=%d\n", reqID, objective, maxResults)

	exaReq := map[string]any{
		"query":       objective,
		"type":        "auto",
		"num_results": maxResults,
		"contents": map[string]any{
			"text": map[string]any{
				"max_characters": 10000,
			},
		},
	}

	result, err := ph.callExa("/search", exaReq)
	if err != nil {
		log.Printf("[%04d] EXA      search error: %v\n", reqID, err)
		ph.writeToolError(w, "exa_error", err.Error())
		return
	}

	// Format results for the CLI's expected schema
	md := ph.formatSearchResults(result)
	log.Printf("[%04d] EXA      search returned %d results\n", reqID, len(md))
	ph.writeToolResult(w, map[string]any{
		"results":                 md,
		"showParallelAttribution": false,
	})
}

func (ph *ProxyHandler) handleExaContents(w http.ResponseWriter, reqID uint64, pageURL string, objective string) {
	log.Printf("[%04d] EXA      extractWebPageContent url=%q objective=%q\n", reqID, pageURL, objective)

	exaReq := map[string]any{
		"urls": []string{pageURL},
		"text": map[string]any{
			"max_characters": 20000,
		},
	}

	result, err := ph.callExa("/contents", exaReq)
	if err != nil {
		log.Printf("[%04d] EXA      contents error: %v\n", reqID, err)
		ph.writeToolError(w, "exa_error", err.Error())
		return
	}

	md := ph.formatContentsResults(result)
	log.Printf("[%04d] EXA      contents returned %d chars\n", reqID, len(md))
	ph.writeToolResult(w, map[string]any{
		"excerpts": []string{md},
	})
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

		out = append(out, map[string]string{
			"title": title,
			"url":   url,
			"text":  text,
		})
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
	json.NewEncoder(w).Encode(map[string]any{
		"ok":     true,
		"result": result,
	})
}

func (ph *ProxyHandler) writeToolError(w http.ResponseWriter, code string, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]any{
		"ok":    false,
		"error": map[string]string{"code": code, "message": message},
	})
}

// logRequest logs detailed request information
func (ph *ProxyHandler) logRequest(reqID uint64, r *http.Request) {
	log.Printf("[%04d] REQUEST  %s %s%s\n", reqID, r.Method, r.URL.Path, formatQuery(r.URL.RawQuery))

	// Log all headers
	for name, values := range r.Header {
		for _, v := range values {
			// Truncate long values (e.g. auth tokens) but show enough to identify them
			display := v
			if len(display) > 120 {
				display = display[:60] + "..." + display[len(display)-20:]
			}
			// Mask authorization tokens but show the scheme
			lower := strings.ToLower(name)
			if lower == "authorization" || lower == "cookie" {
				if idx := strings.Index(display, " "); idx > 0 {
					scheme := display[:idx]
					display = scheme + " [REDACTED len=" + fmt.Sprintf("%d", len(v)-idx-1) + "]"
				} else {
					display = "[REDACTED len=" + fmt.Sprintf("%d", len(v)) + "]"
				}
			}
			log.Printf("[%04d]   header  %s: %s\n", reqID, name, display)
		}
	}

	// Log content length / transfer encoding
	if r.ContentLength > 0 {
		log.Printf("[%04d]   body    %d bytes, content-type: %s\n", reqID, r.ContentLength, r.Header.Get("Content-Type"))
	} else if r.ContentLength == -1 && r.Body != nil {
		log.Printf("[%04d]   body    chunked/unknown, content-type: %s\n", reqID, r.Header.Get("Content-Type"))
	}

	// Log body preview for JSON requests (useful for seeing model names, etc.)
	if r.Body != nil && r.ContentLength > 0 && r.ContentLength <= 64*1024 &&
		strings.Contains(r.Header.Get("Content-Type"), "json") {
		body, err := io.ReadAll(r.Body)
		if err == nil {
			preview := string(body)
			if len(preview) > 500 {
				preview = preview[:500] + fmt.Sprintf("... (%d bytes total)", len(body))
			}
			log.Printf("[%04d]   json    %s\n", reqID, preview)
			// Replace the body so the proxy can still read it
			r.Body = io.NopCloser(strings.NewReader(string(body)))
		}
	}
}

// findMatchedRule returns the name of the rule that matched, or "default"
func (ph *ProxyHandler) findMatchedRule(r *http.Request) string {
	rules := make([]Rule, len(ph.config.Rules))
	copy(rules, ph.config.Rules)
	sort.Slice(rules, func(i, j int) bool {
		return rules[i].Priority > rules[j].Priority
	})
	for _, rule := range rules {
		if rule.Match(r) {
			return rule.Name
		}
	}
	return "default"
}

// findTarget determines the target URL for a request
func (ph *ProxyHandler) findTarget(r *http.Request) string {
	rules := make([]Rule, len(ph.config.Rules))
	copy(rules, ph.config.Rules)
	sort.Slice(rules, func(i, j int) bool {
		return rules[i].Priority > rules[j].Priority
	})

	for _, rule := range rules {
		if rule.Match(r) {
			return rule.Target
		}
	}

	return ph.config.DefaultTarget
}

// Start starts the proxy server
func (ph *ProxyHandler) Start() error {
	addr := net.JoinHostPort(ph.config.ListenAddr, fmt.Sprintf("%d", ph.config.ListenPort))

	log.Println("========================================")
	log.Println("  amp-proxy starting")
	log.Println("========================================")
	log.Printf("  listen:    %s\n", addr)
	log.Printf("  vibeproxy: %s (LLM provider requests)\n", ph.config.VibeProxyTarget)
	log.Printf("  ampcode:   %s (everything else)\n", ph.config.DefaultTarget)
	if ph.config.ExaAPIKey != "" {
		log.Printf("  exa:       enabled (web_search, read_web_page)\n")
	} else {
		log.Printf("  exa:       disabled (set EXA_API_KEY to enable)\n")
	}
	log.Println()
	log.Println("Routing rules (highest priority first):")
	rules := make([]Rule, len(ph.config.Rules))
	copy(rules, ph.config.Rules)
	sort.Slice(rules, func(i, j int) bool {
		return rules[i].Priority > rules[j].Priority
	})
	for _, rule := range rules {
		dest := rule.Target
		if dest == "" {
			dest = "(blocked)"
		}
		dest = strings.Replace(dest, "https://", "", 1)
		dest = strings.Replace(dest, "http://", "", 1)
		log.Printf("  [%3d] %-30s -> %s\n", rule.Priority, rule.Name, dest)
	}
	log.Printf("  [  -] %-30s -> %s\n", "(default)", strings.Replace(ph.config.DefaultTarget, "https://", "", 1))
	log.Println()
	log.Println("Waiting for requests...")
	log.Println("========================================")

	return http.ListenAndServe(addr, ph)
}

func formatQuery(q string) string {
	if q == "" {
		return ""
	}
	return "?" + q
}

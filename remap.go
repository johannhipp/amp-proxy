package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// ModelMapping defines how to remap an unsupported model to a supported one
type ModelMapping struct {
	SourcePattern  string // glob-style match on source model name
	TargetModel    string // target model identifier
	TargetProvider string // "anthropic" or "openai"
	TargetPath     string // API path to use on vibeproxy
}

var modelMappings = []ModelMapping{
	// Gemini flash variants -> Claude Haiku
	{SourcePattern: "gemini-3-flash-preview", TargetModel: "claude-haiku-4-5-20251001", TargetProvider: "anthropic", TargetPath: "/api/provider/anthropic/v1/messages"},
	{SourcePattern: "gemini-3-flash", TargetModel: "claude-haiku-4-5-20251001", TargetProvider: "anthropic", TargetPath: "/api/provider/anthropic/v1/messages"},
	// Gemini pro -> GPT 5.4
	{SourcePattern: "gemini-3-pro", TargetModel: "gpt-5.4", TargetProvider: "openai", TargetPath: "/api/provider/openai/v1/chat/completions"},
	// Gemini pro image -> GPT image
	{SourcePattern: "gemini-3-pro-image", TargetModel: "gpt-image-1", TargetProvider: "openai", TargetPath: "/api/provider/openai/v1/chat/completions"},
}

// Fallback for any model we don't explicitly map
var fallbackMapping = ModelMapping{
	TargetModel:    "claude-sonnet-4-6-20250929",
	TargetProvider: "anthropic",
	TargetPath:     "/api/provider/anthropic/v1/messages",
}

// googleModelRegex extracts the model name from Google provider paths
var googleModelRegex = regexp.MustCompile(`/api/provider/google/.+/models/([^/:]+):(generateContent|streamGenerateContent)`)

// parseGoogleProviderRequest checks if this is a Google provider request and extracts the model name
func parseGoogleProviderRequest(r *http.Request) (model string, streaming bool, ok bool) {
	matches := googleModelRegex.FindStringSubmatch(r.URL.Path)
	if matches == nil {
		return "", false, false
	}
	model = matches[1]
	streaming = matches[2] == "streamGenerateContent" || r.URL.Query().Get("alt") == "sse"
	return model, streaming, true
}

// isUnsupportedProviderRequest checks if this is a request to a provider we don't support
// (anything that's not anthropic or openai)
func isUnsupportedProviderRequest(r *http.Request) (provider string, ok bool) {
	if !strings.HasPrefix(r.URL.Path, "/api/provider/") {
		return "", false
	}
	// Extract provider name from /api/provider/{provider}/...
	rest := strings.TrimPrefix(r.URL.Path, "/api/provider/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) == 0 {
		return "", false
	}
	provider = parts[0]
	if provider == "anthropic" || provider == "openai" {
		return "", false
	}
	return provider, true
}

// findMapping returns the mapping for a given model, or the fallback
func findMapping(model string) (ModelMapping, bool) {
	for _, m := range modelMappings {
		if m.SourcePattern == model {
			return m, true
		}
	}
	return fallbackMapping, false
}

// handleRemappedRequest handles a request that needs model remapping
func (ph *ProxyHandler) handleRemappedRequest(w http.ResponseWriter, r *http.Request, reqID uint64, model string, streaming bool, mapping ModelMapping, isExplicit bool) {
	start := time.Now()

	if !isExplicit {
		log.Printf("[%04d] WARNING  unmapped model %q -> falling back to %s/%s. Add an explicit mapping to suppress this warning.\n",
			reqID, model, mapping.TargetProvider, mapping.TargetModel)
	}

	// Read the original request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, `{"error":"failed to read request body"}`, http.StatusBadRequest)
		log.Printf("[%04d] ERROR    failed to read body: %v\n", reqID, err)
		return
	}

	// Parse the Google GenAI request
	var googleReq map[string]interface{}
	if err := json.Unmarshal(body, &googleReq); err != nil {
		http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
		log.Printf("[%04d] ERROR    invalid JSON: %v\n", reqID, err)
		return
	}

	// Translate based on target provider
	var translatedBody []byte
	switch mapping.TargetProvider {
	case "anthropic":
		translatedBody, err = translateGoogleToAnthropic(googleReq, mapping.TargetModel, streaming)
	case "openai":
		translatedBody, err = translateGoogleToOpenAI(googleReq, mapping.TargetModel, streaming)
	default:
		http.Error(w, `{"error":"unsupported target provider"}`, http.StatusInternalServerError)
		return
	}
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"translation failed: %s"}`, err.Error()), http.StatusInternalServerError)
		log.Printf("[%04d] ERROR    translation failed: %v\n", reqID, err)
		return
	}

	// Build the outbound request to vibeproxy
	targetURL := ph.config.VibeProxyTarget + mapping.TargetPath
	outReq, err := http.NewRequestWithContext(r.Context(), "POST", targetURL, bytes.NewReader(translatedBody))
	if err != nil {
		http.Error(w, `{"error":"failed to create request"}`, http.StatusInternalServerError)
		return
	}
	outReq.Header.Set("Content-Type", "application/json")

	// Forward auth headers
	if auth := r.Header.Get("Authorization"); auth != "" {
		outReq.Header.Set("Authorization", auth)
	}
	for _, cookie := range r.Cookies() {
		outReq.AddCookie(cookie)
	}
	// Forward Amp-specific headers
	for name, values := range r.Header {
		if strings.HasPrefix(name, "X-Amp-") {
			for _, v := range values {
				outReq.Header.Set(name, v)
			}
		}
	}

	// Set Anthropic-specific headers
	if mapping.TargetProvider == "anthropic" {
		outReq.Header.Set("Anthropic-Version", "2023-06-01")
	}

	// Log the translated body for debugging
	if len(translatedBody) <= 2000 {
		log.Printf("[%04d] REMAP    translated body: %s\n", reqID, string(translatedBody))
	} else {
		log.Printf("[%04d] REMAP    translated body: %s... (%d bytes total)\n", reqID, string(translatedBody[:1000]), len(translatedBody))
	}

	log.Printf("[%04d] REMAP    %s -> %s (stream=%v)\n", reqID, targetURL, mapping.TargetModel, streaming)

	// Make the request
	resp, err := ph.httpClient.Do(outReq)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"upstream request failed: %s"}`, err.Error()), http.StatusBadGateway)
		log.Printf("[%04d] ERROR    upstream request failed: %v\n", reqID, err)
		return
	}
	defer resp.Body.Close()

	if !streaming {
		ph.handleNonStreamingResponse(w, resp, reqID, mapping, start)
	} else {
		ph.handleStreamingResponse(w, resp, reqID, mapping, start)
	}
}

func (ph *ProxyHandler) handleNonStreamingResponse(w http.ResponseWriter, resp *http.Response, reqID uint64, mapping ModelMapping, start time.Time) {
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, `{"error":"failed to read upstream response"}`, http.StatusBadGateway)
		return
	}

	// If upstream returned an error, log it and pass it through
	if resp.StatusCode >= 400 {
		log.Printf("[%04d] ERROR    upstream returned %d: %s\n", reqID, resp.StatusCode, string(respBody))
		for k, v := range resp.Header {
			for _, vv := range v {
				w.Header().Add(k, vv)
			}
		}
		w.WriteHeader(resp.StatusCode)
		w.Write(respBody)
		log.Printf("[%04d] RESPONSE REMAP     %d %s (%d bytes, %s)\n",
			reqID, resp.StatusCode, http.StatusText(resp.StatusCode), len(respBody), time.Since(start).Round(time.Millisecond))
		return
	}

	var upstreamResp map[string]interface{}
	if err := json.Unmarshal(respBody, &upstreamResp); err != nil {
		// Can't parse — just forward raw
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(respBody)
		return
	}

	var translated []byte
	switch mapping.TargetProvider {
	case "anthropic":
		translated, err = translateAnthropicToGoogle(upstreamResp)
	case "openai":
		translated, err = translateOpenAIToGoogle(upstreamResp)
	}
	if err != nil {
		// Fallback: forward raw response
		log.Printf("[%04d] WARNING  response translation failed: %v, forwarding raw\n", reqID, err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(respBody)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(translated)
	log.Printf("[%04d] RESPONSE REMAP     200 OK (%d bytes, %s)\n",
		reqID, len(translated), time.Since(start).Round(time.Millisecond))
}

func (ph *ProxyHandler) handleStreamingResponse(w http.ResponseWriter, resp *http.Response, reqID uint64, mapping ModelMapping, start time.Time) {
	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		w.WriteHeader(resp.StatusCode)
		w.Write(respBody)
		log.Printf("[%04d] RESPONSE REMAP     %d %s (error, %s)\n",
			reqID, resp.StatusCode, http.StatusText(resp.StatusCode), time.Since(start).Round(time.Millisecond))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	flusher, canFlush := w.(http.Flusher)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 256*1024), 256*1024)

	var totalBytes int64
	var eventType string

	for scanner.Scan() {
		line := scanner.Text()

		// Track event type for Anthropic SSE (event: xxx)
		if strings.HasPrefix(line, "event: ") {
			eventType = strings.TrimPrefix(line, "event: ")
			continue
		}

		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		// OpenAI stream terminator
		if data == "[DONE]" {
			break
		}

		var chunk map[string]interface{}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		var googleChunk []byte
		var err error
		switch mapping.TargetProvider {
		case "anthropic":
			googleChunk, err = translateAnthropicStreamChunk(chunk, eventType)
		case "openai":
			googleChunk, err = translateOpenAIStreamChunk(chunk)
		}
		if err != nil || googleChunk == nil {
			continue
		}

		sseFrame := fmt.Sprintf("data: %s\n\n", googleChunk)
		n, _ := w.Write([]byte(sseFrame))
		totalBytes += int64(n)
		if canFlush {
			flusher.Flush()
		}
	}

	log.Printf("[%04d] RESPONSE REMAP     200 OK (stream, %d bytes, %s)\n",
		reqID, totalBytes, time.Since(start).Round(time.Millisecond))
}

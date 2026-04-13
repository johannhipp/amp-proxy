package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"
)

type providerDef struct {
	name      string
	loginFlag string
	tokenType string
}

var providerDefs = []providerDef{
	{"claude", "-claude-login", "claude"},
	{"openai", "-codex-login", "codex"},
	{"gemini", "-login", "gemini-cli"},
	{"copilot", "-github-copilot-login", "github-copilot"},
	{"qwen", "-qwen-login", "qwen"},
	{"antigravity", "-antigravity-login", "antigravity"},
}

var providerLoginFlags = map[string]string{}
var providerTokenTypes = map[string]string{}

func init() {
	for _, p := range providerDefs {
		providerLoginFlags[p.name] = p.loginFlag
		providerTokenTypes[p.name] = p.tokenType
	}
	// Aliases
	providerLoginFlags["codex"] = providerLoginFlags["openai"]
	providerTokenTypes["codex"] = providerTokenTypes["openai"]
}

// tokenFile represents a parsed auth token file
type tokenFile struct {
	Type     string `json:"type"`
	Email    string `json:"email"`
	Disabled bool   `json:"disabled"`
	Expired  string `json:"expired"`
	Filename string `json:"-"`
}

// RunLogin runs the OAuth login flow for a provider
func RunLogin(provider, authDir string) error {
	flag, ok := providerLoginFlags[provider]
	if !ok {
		return fmt.Errorf("unknown provider %q\nSupported: claude, openai, gemini, copilot, qwen, antigravity", provider)
	}

	binary := findCLIProxyBinary()
	if binary == "" {
		return fmt.Errorf("cli-proxy-api-plus binary not found\n\nPlease install it:\n  go install github.com/router-for-me/CLIProxyAPI/v6@latest\n\nOr download from: https://github.com/router-for-me/CLIProxyAPIPlus/releases")
	}

	authDir = ExpandHome(authDir)
	if err := os.MkdirAll(authDir, 0700); err != nil {
		return fmt.Errorf("create auth dir: %w", err)
	}

	fmt.Printf("Logging in to %s...\n", provider)
	fmt.Printf("Using binary: %s\n", binary)

	// Build a minimal config for the login command
	configPath, err := writeMinimalConfig(authDir)
	if err != nil {
		return fmt.Errorf("write temp config: %w", err)
	}
	defer os.Remove(configPath)

	cmd := exec.Command(binary, flag, "-config", configPath)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("login command failed: %w", err)
	}

	fmt.Printf("\n✓ Login complete. Tokens saved to %s\n", authDir)
	return nil
}

// RunLogout removes auth tokens for a provider
func RunLogout(provider, authDir string) error {
	tokenType, ok := providerTokenTypes[provider]
	if !ok {
		return fmt.Errorf("unknown provider %q", provider)
	}

	authDir = ExpandHome(authDir)
	tokens, err := scanTokenFiles(authDir)
	if err != nil {
		return fmt.Errorf("scan tokens: %w", err)
	}

	removed := 0
	for _, t := range tokens {
		if t.Type == tokenType {
			path := filepath.Join(authDir, t.Filename)
			if err := os.Remove(path); err != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to remove %s: %v\n", t.Filename, err)
			} else {
				removed++
			}
		}
	}

	if removed == 0 {
		fmt.Printf("No tokens found for %s\n", provider)
	} else {
		fmt.Printf("✓ Removed %d token(s) for %s\n", removed, provider)
	}
	return nil
}

// RunStatus displays auth status for all providers
func RunStatus(authDir string) {
	authDir = ExpandHome(authDir)
	tokens, err := scanTokenFiles(authDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to scan auth directory %s: %v\n", authDir, err)
		fmt.Fprintf(os.Stderr, "Run 'amp-proxy login <provider>' to authenticate.\n")
		return
	}

	// Group by provider type
	byType := make(map[string][]tokenFile)
	for _, t := range tokens {
		byType[t.Type] = append(byType[t.Type], t)
	}

	fmt.Printf("Auth directory: %s\n\n", authDir)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "Provider\tStatus\tAccount\tExpires")
	fmt.Fprintln(w, "────────\t──────\t───────\t───────")

	for _, p := range providerDefs {
		tokens, ok := byType[p.tokenType]
		if !ok || len(tokens) == 0 {
			fmt.Fprintf(w, "%s\t✗ not authed\t-\t-\n", p.name)
			continue
		}
		for _, t := range tokens {
			status := "✓ active"
			if t.Disabled {
				status = "⊘ disabled"
			}
			if t.Expired != "" {
				if exp, err := time.Parse(time.RFC3339, t.Expired); err == nil {
					if exp.Before(time.Now()) {
						status = "✗ expired"
					}
				}
			}
			email := t.Email
			if email == "" {
				email = "-"
			}
			expires := "-"
			if t.Expired != "" {
				if exp, err := time.Parse(time.RFC3339, t.Expired); err == nil {
					expires = exp.Format("2006-01-02")
				}
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", p.name, status, email, expires)
		}
	}

	w.Flush()
}

// scanTokenFiles reads all JSON token files in the auth directory
func scanTokenFiles(authDir string) ([]tokenFile, error) {
	entries, err := os.ReadDir(authDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var tokens []tokenFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(authDir, e.Name()))
		if err != nil {
			continue
		}
		var t tokenFile
		if err := json.Unmarshal(data, &t); err != nil {
			continue
		}
		t.Filename = e.Name()
		if t.Type != "" {
			tokens = append(tokens, t)
		}
	}
	return tokens, nil
}

// findCLIProxyBinary searches for the cli-proxy-api-plus binary
func findCLIProxyBinary() string {
	// Check PATH
	if p, err := exec.LookPath("cli-proxy-api-plus"); err == nil {
		return p
	}

	// Check common locations
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(home, "go", "bin", "cli-proxy-api-plus"),
		filepath.Join(home, ".local", "bin", "cli-proxy-api-plus"),
		"/usr/local/bin/cli-proxy-api-plus",
	}

	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	return ""
}

// writeMinimalConfig creates a temporary config file for the login command
func writeMinimalConfig(authDir string) (string, error) {
	config := fmt.Sprintf("port: 0\nhost: 127.0.0.1\nauth-dir: %q\ndebug: false\n", authDir)
	f, err := os.CreateTemp("", "amp-proxy-auth-*.yaml")
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(config); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	f.Close()
	return f.Name(), nil
}

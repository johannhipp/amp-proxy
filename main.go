package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
)

var version = "dev"

func main() {
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		cmd = args[0]
		args = args[1:]
	}

	switch cmd {
	case "serve":
		cmdServe(args)
	case "login":
		cmdLogin(args)
	case "logout":
		cmdLogout(args)
	case "status":
		cmdStatus(args)
	case "version":
		fmt.Printf("amp-proxy %s\n", version)
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func loadConfigFromArgs(name string, args []string) (*AppConfig, error) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	configPath := fs.String("config", "", "Config file path")
	fs.Parse(args)
	if v := os.Getenv("AMP_PROXY_CONFIG"); v != "" && *configPath == "" {
		*configPath = v
	}
	return LoadConfig(*configPath)
}

func cmdServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.Int("port", 18317, "Port to listen on")
	addr := fs.String("addr", "127.0.0.1", "Address to listen on")
	configPath := fs.String("config", "", "Config file path (default: auto-detect)")
	ampcodeURL := fs.String("ampcode", "https://ampcode.com", "Ampcode URL")
	debug := fs.Bool("debug", false, "Enable debug logging")
	fs.Parse(args)

	// Env overrides
	if v := os.Getenv("LISTEN_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			*port = p
		}
	}
	if v := os.Getenv("LISTEN_ADDR"); v != "" {
		*addr = v
	}
	if v := os.Getenv("AMPCODE_URL"); v != "" {
		*ampcodeURL = v
	}
	if os.Getenv("AMP_PROXY_DEBUG") != "" {
		*debug = true
	}
	if v := os.Getenv("AMP_PROXY_CONFIG"); v != "" && *configPath == "" {
		*configPath = v
	}

	// Set up logging
	logLevel := &slog.LevelVar{}
	if *debug {
		logLevel.Set(slog.LevelDebug)
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})))

	// Load config
	cfg, err := LoadConfig(*configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	// CLI flags override config
	if isFlagSet(fs, "port") || os.Getenv("LISTEN_PORT") != "" {
		cfg.ListenPort = *port
	}
	if isFlagSet(fs, "addr") || os.Getenv("LISTEN_ADDR") != "" {
		cfg.ListenAddr = *addr
	}
	if isFlagSet(fs, "ampcode") || os.Getenv("AMPCODE_URL") != "" {
		cfg.AmpcodeURL = *ampcodeURL
	}
	if v := os.Getenv("EXA_API_KEY"); v != "" {
		cfg.ExaAPIKey = v
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	handler := NewProxyHandler(cfg)
	if err := handler.Start(ctx); err != nil {
		slog.Error("failed to start proxy", "error", err)
		os.Exit(1)
	}
}

func cmdLogin(args []string) {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "usage: amp-proxy login <provider>\n")
		fmt.Fprintf(os.Stderr, "providers: claude, openai, gemini, copilot, qwen, antigravity\n")
		os.Exit(1)
	}
	provider := args[0]

	cfg, err := loadConfigFromArgs("login", args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	if err := RunLogin(provider, cfg.AuthDir); err != nil {
		fmt.Fprintf(os.Stderr, "login failed: %v\n", err)
		os.Exit(1)
	}
}

func cmdLogout(args []string) {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "usage: amp-proxy logout <provider>\n")
		os.Exit(1)
	}
	provider := args[0]

	cfg, err := loadConfigFromArgs("logout", args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	if err := RunLogout(provider, cfg.AuthDir); err != nil {
		fmt.Fprintf(os.Stderr, "logout failed: %v\n", err)
		os.Exit(1)
	}
}

func cmdStatus(args []string) {
	cfg, err := loadConfigFromArgs("status", args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	RunStatus(cfg.AuthDir)
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `amp-proxy - Smart proxy for Amp CLI with built-in provider auth

Usage:
  amp-proxy [command] [flags]

Commands:
  serve     Start the proxy server (default)
  login     Authenticate with a provider (claude, openai, gemini, copilot, qwen)
  logout    Remove saved auth for a provider
  status    Show auth status for all providers
  version   Print version info
  help      Show this help

Serve Flags:
  --port <port>      Listen port (default: 18317, env: LISTEN_PORT)
  --addr <addr>      Listen address (default: 127.0.0.1, env: LISTEN_ADDR)
  --config <path>    Config file path (env: AMP_PROXY_CONFIG)
  --ampcode <url>    Ampcode URL (default: https://ampcode.com, env: AMPCODE_URL)
  --debug            Enable debug logging (env: AMP_PROXY_DEBUG)

Config File:
  Optional. Auto-detected at:
    %s/config.yaml
  Override with --config or AMP_PROXY_CONFIG env var.

Examples:
  amp-proxy                          Start with defaults
  amp-proxy login claude             Authenticate with Claude
  amp-proxy serve --port 9000        Start on custom port
  amp-proxy status                   Show provider auth status
`, defaultConfigDir())
}

func defaultConfigDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "~/.config"
	}
	return filepath.Join(dir, "amp-proxy")
}

func isFlagSet(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

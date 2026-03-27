package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
)

func main() {
	logLevel := &slog.LevelVar{}
	if os.Getenv("AMP_PROXY_DEBUG") != "" {
		logLevel.Set(slog.LevelDebug)
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})))

	config := NewDefaultConfig()

	// Parse flags
	listenPort := flag.Int("port", config.ListenPort, "Port to listen on")
	listenAddr := flag.String("addr", config.ListenAddr, "Address to listen on")
	ampcodeURL := flag.String("ampcode", config.DefaultTarget, "Ampcode URL (auth, threads, etc.)")
	vibeproxyURL := flag.String("vibeproxy", config.VibeProxyTarget, "VibeProxy URL (LLM provider requests)")
	flag.Parse()

	// Override from environment variables
	if v := os.Getenv("LISTEN_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			*listenPort = p
		}
	}
	if v := os.Getenv("LISTEN_ADDR"); v != "" {
		*listenAddr = v
	}
	if v := os.Getenv("AMPCODE_URL"); v != "" {
		*ampcodeURL = v
	}
	if v := os.Getenv("VIBEPROXY_URL"); v != "" {
		*vibeproxyURL = v
	}

	config.ExaAPIKey = os.Getenv("EXA_API_KEY")

	config.ListenPort = *listenPort
	config.ListenAddr = *listenAddr
	config.DefaultTarget = *ampcodeURL
	config.VibeProxyTarget = *vibeproxyURL

	// Update rule targets to use configured URLs
	for i := range config.Rules {
		switch config.Rules[i].Name {
		case "provider-to-vibeproxy":
			config.Rules[i].Target = config.VibeProxyTarget
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	handler := NewProxyHandler(config)
	if err := handler.Start(ctx); err != nil {
		slog.Error("failed to start proxy", "error", err)
		os.Exit(1)
	}
}

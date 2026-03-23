package main

import (
	"flag"
	"log"
	"os"
	"strconv"
)

func main() {
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

	handler := NewProxyHandler(config)
	if err := handler.Start(); err != nil {
		log.Fatalf("Failed to start proxy: %v\n", err)
	}
}

package main

import (
	"log/slog"
	"os"
	"strings"

	"haas/internal/mcpserver"
)

func parseMCPKeys(raw, fallback string) []string {
	if raw == "" {
		return []string{fallback}
	}
	var keys []string
	for _, k := range strings.Split(raw, ",") {
		if t := strings.TrimSpace(k); t != "" {
			keys = append(keys, t)
		}
	}
	return keys
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	haasURL := os.Getenv("HAAS_URL")
	if haasURL == "" {
		haasURL = "http://localhost:8080"
	}

	apiKey := os.Getenv("HAAS_API_KEY")
	if apiKey == "" {
		logger.Error("HAAS_API_KEY is required but not set")
		os.Exit(1)
	}

	// MCP_API_KEYS: comma-separated keys clients must send as Bearer tokens.
	// Defaults to HAAS_API_KEY so there's only one key to manage.
	mcpKeys := parseMCPKeys(os.Getenv("MCP_API_KEYS"), apiKey)

	transport := os.Getenv("MCP_TRANSPORT") // "stdio" (default) | "sse" | "http"
	s := mcpserver.New(haasURL, apiKey)

	switch transport {
	case "sse":
		addr := os.Getenv("MCP_LISTEN_ADDR")
		if addr == "" {
			addr = ":8090"
		}
		publicURL := os.Getenv("MCP_PUBLIC_URL")
		logger.Info("starting haas MCP server (SSE)", "addr", addr, "public_url", publicURL, "haas_url", haasURL)
		if err := s.ServeSSE(addr, publicURL); err != nil {
			logger.Error("MCP SSE server error", "error", err)
			os.Exit(1)
		}
	case "http":
		addr := os.Getenv("MCP_LISTEN_ADDR")
		if addr == "" {
			addr = ":8090"
		}
		logger.Info("starting haas MCP server (streamable HTTP)", "addr", addr, "haas_url", haasURL)
		if err := s.ServeStreamableHTTP(addr, mcpKeys); err != nil {
			logger.Error("MCP HTTP server error", "error", err)
			os.Exit(1)
		}
	default:
		logger.Info("starting haas MCP server (stdio)", "haas_url", haasURL)
		if err := s.ServeStdio(); err != nil {
			logger.Error("MCP stdio server error", "error", err)
			os.Exit(1)
		}
	}
}

package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddr           string
	DockerHost           string
	DefaultCPU           float64
	DefaultMemoryMB      int64
	DefaultDiskMB        int64
	IdleTimeout          time.Duration
	MaxLifetime          time.Duration
	DefaultNetworkPolicy string
	MaxFileUploadMB      int64
	APIKeys              []string
	AllowedImages        []string // empty = all images allowed

	// MCP server. Runs alongside the REST API.
	MCPListenAddr string // e.g. ":8091"
	MCPRESTURL    string // URL the MCP server uses to call the REST API (default: derived from ListenAddr)
}

func Load() *Config {
	return &Config{
		ListenAddr:           envOrDefault("HAAS_LISTEN_ADDR", ":8080"),
		DockerHost:           envOrDefault("DOCKER_HOST", ""),
		DefaultCPU:           envOrDefaultFloat("HAAS_DEFAULT_CPU", 1.0),
		DefaultMemoryMB:      envOrDefaultInt("HAAS_DEFAULT_MEMORY_MB", 2048),
		DefaultDiskMB:        envOrDefaultInt("HAAS_DEFAULT_DISK_MB", 4096),
		IdleTimeout:          envOrDefaultDuration("HAAS_IDLE_TIMEOUT", 10*time.Minute),
		MaxLifetime:          envOrDefaultDuration("HAAS_MAX_LIFETIME", 60*time.Minute),
		DefaultNetworkPolicy: envOrDefault("HAAS_DEFAULT_NETWORK_POLICY", "none"),
		MaxFileUploadMB:      envOrDefaultInt("HAAS_MAX_FILE_UPLOAD_MB", 100),
		APIKeys:              envOrDefaultStringSlice("HAAS_API_KEYS", nil),
		AllowedImages:        envOrDefaultStringSlice("HAAS_ALLOWED_IMAGES", nil),
		MCPListenAddr:        envOrDefault("HAAS_MCP_LISTEN_ADDR", ":8091"),
		MCPRESTURL:           envOrDefault("HAAS_MCP_REST_URL", ""),
	}
}

func envOrDefaultStringSlice(key string, fallback []string) []string {
	if v := os.Getenv(key); v != "" {
		parts := strings.Split(v, ",")
		keys := make([]string, 0, len(parts))
		for _, p := range parts {
			if t := strings.TrimSpace(p); t != "" {
				keys = append(keys, t)
			}
		}
		if len(keys) > 0 {
			return keys
		}
	}
	return fallback
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envOrDefaultFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return fallback
}

func envOrDefaultInt(key string, fallback int64) int64 {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.ParseInt(v, 10, 64); err == nil {
			return i
		}
	}
	return fallback
}

func envOrDefaultDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

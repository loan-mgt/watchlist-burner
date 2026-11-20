// Package config loads and validates the process configuration from flags and
// environment variables. Invalid values fail at boot instead of silently
// degrading a public deployment.
package config

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// Config is the fully resolved runtime configuration.
type Config struct {
	Addr     string
	CacheTTL time.Duration

	CacheSize         int
	BurnBudget        time.Duration
	ScrapeConcurrency int
	MaxFilms          int
	MaxWatchlistPages int
	MaxEnrich         int

	RateLimitPerMinute int
	MaxBodyBytes       int64

	LogLevel  slog.Level
	LogFormat string

	AllowPrivateJellyfin  bool
	AllowInsecureJellyfin bool
	AllowedJellyfinHosts  []string
	TrustProxyHeaders     bool

	Healthcheck bool
}

// Load parses args and env. getenv is injected so tests do not touch the real
// environment.
func Load(args []string, getenv func(string) string) (Config, error) {
	cfg := Config{
		Addr:                  envOr(getenv, "ADDR", ":8097"),
		CacheTTL:              envDuration(getenv, "CACHE_TTL", time.Hour),
		CacheSize:             envInt(getenv, "CACHE_SIZE", 500),
		BurnBudget:            envDuration(getenv, "BURN_BUDGET", 60*time.Second),
		ScrapeConcurrency:     envInt(getenv, "SCRAPE_CONCURRENCY", 8),
		MaxFilms:              envInt(getenv, "MAX_FILMS", 5000),
		MaxWatchlistPages:     envInt(getenv, "MAX_WATCHLIST_PAGES", 50),
		MaxEnrich:             envInt(getenv, "MAX_ENRICH", 25),
		RateLimitPerMinute:    envInt(getenv, "RATE_LIMIT_PER_MINUTE", 30),
		MaxBodyBytes:          int64(envInt(getenv, "MAX_BODY_BYTES", 8<<10)),
		LogFormat:             strings.ToLower(envOr(getenv, "LOG_FORMAT", "json")),
		AllowPrivateJellyfin:  envBool(getenv, "ALLOW_PRIVATE_JELLYFIN", false),
		AllowInsecureJellyfin: envBool(getenv, "ALLOW_INSECURE_JELLYFIN", false),
		AllowedJellyfinHosts:  envList(getenv, "ALLOWED_JELLYFIN_HOSTS"),
		TrustProxyHeaders:     envBool(getenv, "TRUST_PROXY_HEADERS", true),
	}

	level, err := parseLevel(envOr(getenv, "LOG_LEVEL", "info"))
	if err != nil {
		return Config{}, err
	}
	cfg.LogLevel = level

	fs := flag.NewFlagSet("watchlist-burner", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&cfg.Addr, "addr", cfg.Addr, "listen address")
	fs.DurationVar(&cfg.CacheTTL, "cache-ttl", cfg.CacheTTL, "watchlist cache lifetime")
	fs.BoolVar(&cfg.Healthcheck, "healthcheck", false, "probe the local /healthz endpoint and exit")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if fs.NArg() > 0 {
		return Config{}, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	switch {
	case c.Addr == "":
		return fmt.Errorf("ADDR must not be empty")
	case c.CacheTTL <= 0:
		return fmt.Errorf("CACHE_TTL must be positive")
	case c.CacheSize <= 0:
		return fmt.Errorf("CACHE_SIZE must be positive")
	case c.BurnBudget < 5*time.Second:
		return fmt.Errorf("BURN_BUDGET must be at least 5s")
	case c.ScrapeConcurrency <= 0:
		return fmt.Errorf("SCRAPE_CONCURRENCY must be positive")
	case c.MaxFilms <= 0 || c.MaxWatchlistPages <= 0:
		return fmt.Errorf("MAX_FILMS and MAX_WATCHLIST_PAGES must be positive")
	case c.MaxEnrich < 0:
		return fmt.Errorf("MAX_ENRICH must not be negative")
	case c.RateLimitPerMinute < 0:
		return fmt.Errorf("RATE_LIMIT_PER_MINUTE must not be negative")
	case c.MaxBodyBytes <= 0:
		return fmt.Errorf("MAX_BODY_BYTES must be positive")
	}
	if c.LogFormat != "json" && c.LogFormat != "text" {
		return fmt.Errorf("LOG_FORMAT must be json or text")
	}
	return nil
}

func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("LOG_LEVEL must be debug, info, warn or error")
}

func envOr(getenv func(string) string, name, fallback string) string {
	if v := strings.TrimSpace(getenv(name)); v != "" {
		return v
	}
	return fallback
}

func envInt(getenv func(string) string, name string, fallback int) int {
	raw := strings.TrimSpace(getenv(name))
	if raw == "" {
		return fallback
	}
	if n, err := strconv.Atoi(raw); err == nil {
		return n
	}
	return fallback
}

func envDuration(getenv func(string) string, name string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(getenv(name))
	if raw == "" {
		return fallback
	}
	if d, err := time.ParseDuration(raw); err == nil {
		return d
	}
	return fallback
}

func envBool(getenv func(string) string, name string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(getenv(name))) {
	case "":
		return fallback
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func envList(getenv func(string) string, name string) []string {
	raw := strings.TrimSpace(getenv(name))
	if raw == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

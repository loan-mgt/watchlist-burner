package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func env(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestDefaults(t *testing.T) {
	cfg, err := Load(nil, env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != ":8097" {
		t.Errorf("Addr = %q", cfg.Addr)
	}
	if cfg.CacheTTL != time.Hour {
		t.Errorf("CacheTTL = %v", cfg.CacheTTL)
	}
	if cfg.LogLevel != slog.LevelInfo || cfg.LogFormat != "json" {
		t.Errorf("logging = %v/%v", cfg.LogLevel, cfg.LogFormat)
	}
	if cfg.AllowPrivateJellyfin || cfg.AllowInsecureJellyfin {
		t.Error("Jellyfin relaxations must be off by default")
	}
	if cfg.RateLimitPerMinute <= 0 {
		t.Error("rate limiting must be on by default")
	}
}

func TestEnvOverrides(t *testing.T) {
	cfg, err := Load(nil, env(map[string]string{
		"ADDR":                   ":9090",
		"CACHE_TTL":              "15m",
		"LOG_LEVEL":              "debug",
		"LOG_FORMAT":             "text",
		"ALLOW_PRIVATE_JELLYFIN": "true",
		"ALLOWED_JELLYFIN_HOSTS": "a.example, b.example",
		"RATE_LIMIT_PER_MINUTE":  "5",
		"TRUST_PROXY_HEADERS":    "false",
		"MAX_ENRICH":             "0",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != ":9090" || cfg.CacheTTL != 15*time.Minute {
		t.Errorf("addr/ttl = %q/%v", cfg.Addr, cfg.CacheTTL)
	}
	if cfg.LogLevel != slog.LevelDebug || cfg.LogFormat != "text" {
		t.Errorf("logging = %v/%v", cfg.LogLevel, cfg.LogFormat)
	}
	if !cfg.AllowPrivateJellyfin || cfg.TrustProxyHeaders {
		t.Error("boolean env not applied")
	}
	if len(cfg.AllowedJellyfinHosts) != 2 || cfg.AllowedJellyfinHosts[1] != "b.example" {
		t.Errorf("hosts = %v", cfg.AllowedJellyfinHosts)
	}
	if cfg.RateLimitPerMinute != 5 || cfg.MaxEnrich != 0 {
		t.Errorf("numbers = %d/%d", cfg.RateLimitPerMinute, cfg.MaxEnrich)
	}
}

func TestFlagsOverrideEnv(t *testing.T) {
	cfg, err := Load([]string{"-addr", ":1234", "-cache-ttl", "2h"}, env(map[string]string{
		"ADDR":      ":9090",
		"CACHE_TTL": "15m",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != ":1234" || cfg.CacheTTL != 2*time.Hour {
		t.Errorf("addr/ttl = %q/%v", cfg.Addr, cfg.CacheTTL)
	}
}

func TestValidationErrors(t *testing.T) {
	cases := map[string]string{
		"bad level":  "LOG_LEVEL=loud",
		"bad format": "LOG_FORMAT=xml",
		"bad ttl":    "CACHE_TTL=0s",
		"bad budget": "BURN_BUDGET=1s",
		"bad enrich": "MAX_ENRICH=-1",
		"bad rate":   "RATE_LIMIT_PER_MINUTE=-2",
	}
	for name, envVar := range cases {
		t.Run(name, func(t *testing.T) {
			key, value, _ := strings.Cut(envVar, "=")
			if _, err := Load(nil, env(map[string]string{key: value})); err == nil {
				t.Fatalf("expected an error for %s", envVar)
			}
		})
	}
}

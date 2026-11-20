package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"watchlist-burner/internal/burner"
	"watchlist-burner/internal/config"
	"watchlist-burner/internal/httpapi"
	"watchlist-burner/internal/jellyfin"
	"watchlist-burner/internal/letterboxd"
	"watchlist-burner/internal/seerr"
	"watchlist-burner/web"
)

// version is injected at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Args[1:], os.Getenv)
	if err != nil {
		return err
	}
	if cfg.Healthcheck {
		return healthcheck(cfg.Addr)
	}

	log := newLogger(cfg)

	guard := jellyfin.NewGuard(cfg.AllowPrivateJellyfin, cfg.AllowInsecureJellyfin, cfg.AllowedJellyfinHosts)
	jellyfinClient := guard.HTTPClient(30 * time.Second)

	letterboxdClient := &http.Client{Timeout: 20 * time.Second}
	scraper := letterboxd.NewScraper(letterboxd.Options{
		Client:   letterboxdClient,
		MaxPages: cfg.MaxWatchlistPages,
		MaxFilms: cfg.MaxFilms,
	})
	meta := letterboxd.NewMetadataResolver(letterboxdClient, 250*time.Millisecond)

	burn := burner.New(guard, jellyfinClient, scraper, meta, burner.Config{
		CacheTTL:          cfg.CacheTTL,
		CacheSize:         cfg.CacheSize,
		BurnBudget:        cfg.BurnBudget,
		ScrapeConcurrency: cfg.ScrapeConcurrency,
		MaxEnrich:         cfg.MaxEnrich,
	})

	assets, err := web.FS()
	if err != nil {
		return fmt.Errorf("embedded assets: %w", err)
	}

	var discoverer *seerr.Discoverer
	if cfg.SeerrDiscovery {
		discoverer = seerr.New(guard, jellyfinClient, seerr.Options{
			Prefixes: cfg.SeerrPrefixes,
			Deep:     cfg.SeerrDeep,
		})
	}

	api := httpapi.New(burn, meta, cfg, assets, log, version, discoverer)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// WriteTimeout stays zero: responses stream for the length of a burn.
		WriteTimeout:   0,
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 64 << 10,
		ErrorLog:       slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Addr, "version", version)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

func newLogger(cfg config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	var h slog.Handler
	if cfg.LogFormat == "text" {
		h = slog.NewTextHandler(os.Stdout, opts)
	} else {
		h = slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.New(h)
}

// healthcheck probes the local /healthz endpoint. Distroless images have no
// shell or curl, so the binary doubles as its own health probe.
func healthcheck(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("healthcheck: parse addr %q: %w", addr, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	url := "http://" + net.JoinHostPort(host, port) + "/healthz"

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck: status %s", strconv.Itoa(resp.StatusCode))
	}
	return nil
}

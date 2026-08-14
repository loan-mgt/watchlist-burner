package main

import (
	"embed"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"time"

	"golang.org/x/net/websocket"

	"watchlist-burner/internal/jellyfin"
	"watchlist-burner/internal/letterboxd"
	"watchlist-burner/internal/server"
)

//go:embed web/templates web/static
var webFS embed.FS

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func defaultTTL() time.Duration {
	d, err := time.ParseDuration(envOr("CACHE_TTL", "1h"))
	if err != nil {
		return time.Hour
	}
	return d
}

func main() {
	addr := flag.String("addr", envOr("ADDR", ":8097"), "listen address")
	cacheTTL := flag.Duration("cache-ttl", defaultTTL(), "letterboxd watchlist cache lifetime")
	flag.Parse()

	store := jellyfin.NewSessionStore()
	srv := server.New(store, letterboxd.NewScraper(), *cacheTTL)

	templates, err := fs.Sub(webFS, "web/templates")
	if err != nil {
		log.Fatalf("templates: %v", err)
	}
	static, err := fs.Sub(webFS, "web/static")
	if err != nil {
		log.Fatalf("static: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/login", srv.HandleLogin)
	mux.HandleFunc("/api/logout", srv.HandleLogout)
	mux.HandleFunc("/api/watchlist", srv.HandleWatchlist)
	mux.Handle("/api/watchlist/ws", websocket.Server{
		Handshake: func(*websocket.Config, *http.Request) error { return nil },
		Handler:   srv.HandleWatchlistWS,
	})
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))
	mux.Handle("/", http.FileServer(http.FS(templates)))

	log.Printf("watchlist-burner listening on %s", *addr)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}

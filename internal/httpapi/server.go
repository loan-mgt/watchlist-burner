package httpapi

import (
	"context"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"strings"

	"watchlist-burner/internal/burner"
	"watchlist-burner/internal/config"
	"watchlist-burner/internal/letterboxd"
	"watchlist-burner/internal/seerr"
)

// MetadataSource is the seam to per-film metadata (poster, TMDB id).
type MetadataSource interface {
	Meta(ctx context.Context, slug string) (letterboxd.Meta, bool)
}

// Server is the HTTP adapter over the burner module.
type Server struct {
	burner       *burner.Burner
	meta         MetadataSource
	cfg          config.Config
	static       fs.FS
	log          *slog.Logger
	limiter      *limiter
	seerrLimiter *limiter
	seerr        *seerr.Discoverer
	version      string
}

// New builds the HTTP surface. static is the built web/assets tree.
func New(b *burner.Burner, meta MetadataSource, cfg config.Config, static fs.FS, log *slog.Logger, version string, discoverer *seerr.Discoverer) *Server {
	return &Server{
		burner:       b,
		meta:         meta,
		cfg:          cfg,
		static:       static,
		log:          log,
		limiter:      newLimiter(cfg.RateLimitPerMinute),
		seerrLimiter: newLimiter(cfg.SeerrScanPerMinute),
		seerr:        discoverer,
		version:      version,
	}
}

// Handler returns the fully wrapped HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/config", s.handleConfig)
	mux.HandleFunc("POST /api/watchlist", s.handleWatchlist)
	mux.HandleFunc("POST /api/watchlist/stream", s.handleWatchlistStream)
	mux.HandleFunc("GET /api/poster", s.handlePoster)
	mux.HandleFunc("POST /api/seerr/discover", s.handleSeerrDiscover)
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /readyz", s.handleHealth)
	mux.Handle("GET /", s.staticHandler())

	// Outermost first: the request id must exist before logging runs.
	return s.withRequestID(
		s.withLogging(
			s.withRecovery(
				s.withSecurityHeaders(
					s.withBodyLimit(
						s.withRateLimit(mux))))))
}

// staticHandler serves the embedded build. Hashed assets are immutable; the
// shell is always revalidated. Pre-compressed variants are served when the
// client advertises support.
func (s *Server) staticHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		if name != "index.html" && !strings.HasPrefix(name, "assets/") {
			http.NotFound(w, r)
			return
		}

		data, compressed, err := s.readAsset(r, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		w.Header().Add("Vary", "Accept-Encoding")
		if compressed != "" {
			w.Header().Set("Content-Encoding", compressed)
		}
		w.Header().Set("Content-Type", contentType(name))
		if name == "index.html" {
			w.Header().Set("Cache-Control", "no-cache")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		_, _ = w.Write(data)
	})
}

func (s *Server) readAsset(r *http.Request, name string) (data []byte, encoding string, err error) {
	accept := r.Header.Get("Accept-Encoding")
	candidates := []struct{ suffix, encoding string }{
		{".br", "br"},
		{".gz", "gzip"},
	}
	for _, c := range candidates {
		if !strings.Contains(accept, c.encoding) {
			continue
		}
		if b, err := fs.ReadFile(s.static, name+c.suffix); err == nil {
			return b, c.encoding, nil
		}
	}
	b, err := fs.ReadFile(s.static, name)
	return b, "", err
}

func contentType(name string) string {
	ext := path.Ext(name)
	if ct := mime.TypeByExtension(ext); ct != "" {
		return ct
	}
	switch ext {
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".json":
		return "application/json; charset=utf-8"
	case ".html":
		return "text/html; charset=utf-8"
	}
	return "application/octet-stream"
}

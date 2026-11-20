package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"watchlist-burner/internal/jellyfin"
	"watchlist-burner/internal/letterboxd"
	"watchlist-burner/internal/seerr"
)

func (s *Server) handleConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version":      s.version,
		"maxFilms":     s.cfg.MaxFilms,
		"rateLimit":    s.cfg.RateLimitPerMinute,
		"maxBodyBytes": s.cfg.MaxBodyBytes,
		"quickConnect": true,
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handlePoster resolves a film poster on demand, with the resolver's own cache.
func (s *Server) handlePoster(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimSpace(r.URL.Query().Get("slug"))
	if !letterboxd.ValidSlug(slug) {
		writeError(w, r, s.log, badRequest("invalid_slug", "Invalid film slug."))
		return
	}

	posterURL := ""
	if meta, ok := s.meta.Meta(r.Context(), slug); ok {
		posterURL = meta.PosterURL
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_ = json.NewEncoder(w).Encode(map[string]string{"posterUrl": posterURL})
}

// handleWatchlist is the non-streaming fallback: same input, one JSON response.
func (s *Server) handleWatchlist(w http.ResponseWriter, r *http.Request) {
	creds, lbUser, err := s.requestCredentials(r)
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}

	res, err := s.burner.Burn(r.Context(), creds, lbUser, nil)
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleWatchlistStream streams phases, sampled library progress and the final
// result as NDJSON. Errors after the stream starts are delivered as frames so
// the client can react (especially to an expired Jellyfin token).
func (s *Server) handleWatchlistStream(w http.ResponseWriter, r *http.Request) {
	creds, lbUser, err := s.requestCredentials(r)
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}
	// Authenticate before committing to 200 so bad URLs and expired tokens keep
	// their real HTTP status instead of arriving as an error frame.
	sess, err := s.burner.Authenticate(r.Context(), creds)
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, r, s.log, errors.New("httpapi: streaming unsupported"))
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	stream := newStreamWriter(w, flusher)
	stop := stream.heartbeat(5 * time.Second)
	defer stop()

	stream.send(frame{Type: "phase", Phase: "Scraping your Letterboxd watchlist…"})

	res, err := s.burner.BurnSession(r.Context(), sess, lbUser, func(index, total int, it jellyfin.Item) {
		if index%100 != 0 {
			return
		}
		stream.send(frame{
			Type:  "film",
			Title: it.Name,
			Year:  yearOf(it),
			Index: index,
			Total: total,
		})
	})
	if err != nil {
		status, code, message := classify(err)
		if status >= 500 {
			s.log.ErrorContext(r.Context(), "stream failed", "err", err, "request_id", requestIDFrom(r.Context()))
		}
		stream.send(frame{Type: "error", Code: code, Message: message})
		return
	}

	stream.send(frame{
		Type:  "result",
		Films: res.Films,
		Meta:  &resultMeta{Total: res.Total, Matched: res.Matched, Watched: res.Watched},
	})
}

// requestCredentials extracts the browser-supplied Jellyfin session.
func (s *Server) requestCredentials(r *http.Request) (jellyfin.Credentials, string, error) {
	baseURL := strings.TrimSpace(r.Header.Get("X-Jellyfin-URL"))
	token := strings.TrimSpace(r.Header.Get("X-Jellyfin-Token"))
	userID := strings.TrimSpace(r.Header.Get("X-Jellyfin-User-Id"))

	if baseURL == "" || token == "" {
		return jellyfin.Credentials{}, "", badRequest("missing_credentials", "A Jellyfin server URL and token are required.")
	}
	if len(baseURL) > 2048 || len(token) > 1024 || len(userID) > 128 {
		return jellyfin.Credentials{}, "", badRequest("invalid_credentials", "Jellyfin credentials are malformed.")
	}

	lbUser := strings.TrimSpace(r.URL.Query().Get("lbuser"))
	if !letterboxd.ValidUsername(lbUser) {
		return jellyfin.Credentials{}, "", badRequest("invalid_username", "Invalid Letterboxd username.")
	}

	return jellyfin.Credentials{BaseURL: baseURL, Token: token, UserID: userID}, lbUser, nil
}

func yearOf(it jellyfin.Item) int {
	if it.ProductionYear == nil {
		return 0
	}
	return *it.ProductionYear
}

// handleSeerrDiscover probes the conventional Seerr host names under the
// Jellyfin server's registrable domain. Seerr sends no CORS headers, so this
// has to run server-side; the discoverer reuses the Jellyfin SSRF guard and a
// dedicated, stricter rate limit.
func (s *Server) handleSeerrDiscover(w http.ResponseWriter, r *http.Request) {
	if s.seerr == nil {
		writeJSON(w, http.StatusNotFound, errorBody{
			Code:    "discovery_disabled",
			Message: "Seerr discovery is disabled on this server.",
		})
		return
	}
	if ok, wait := s.seerrLimiter.allow(s.clientIP(r)); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeJSON(w, http.StatusTooManyRequests, errorBody{
			Code:    "rate_limited",
			Message: "Too many discovery scans, please slow down.",
		})
		return
	}

	var req struct {
		JellyfinURL string `json:"jellyfinUrl"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeError(w, r, s.log, badRequest("invalid_body", "Invalid request body."))
		return
	}
	if strings.TrimSpace(req.JellyfinURL) == "" {
		writeError(w, r, s.log, badRequest("missing_url", "A Jellyfin server URL is required."))
		return
	}

	instances, cached, err := s.seerr.Discover(r.Context(), req.JellyfinURL)
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}
	if instances == nil {
		instances = []seerr.Instance{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"instances": instances,
		"cached":    cached,
	})
}

package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"watchlist-burner/internal/jellyfin"
	"watchlist-burner/internal/letterboxd"
)

// WatchlistFilm is the JSON shape sent to the frontend.
type WatchlistFilm struct {
	Slug          string `json:"slug"`
	Title         string `json:"title"`
	Year          int    `json:"year"`
	PosterURL     string `json:"posterUrl"`
	LetterboxdURL string `json:"letterboxdUrl"`
	ItemID        string `json:"itemId,omitempty"`
	InLibrary     bool   `json:"inLibrary"`
}

type watchlistResponse struct {
	Films   []WatchlistFilm `json:"films"`
	Total   int             `json:"total"`
	Matched int             `json:"matched"`
	Watched int             `json:"watched"`
}

// Server exposes the Burner module over HTTP and WebSocket transports.
type Server struct {
	burner   *Burner
	resolver *letterboxd.PosterResolver
}

func New(burner *Burner, resolver *letterboxd.PosterResolver) *Server {
	return &Server{burner: burner, resolver: resolver}
}

func (s *Server) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		JellyfinURL string `json:"jellyfinUrl"`
		Username    string `json:"username"`
		Password    string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	req.JellyfinURL = strings.TrimSpace(req.JellyfinURL)
	if req.JellyfinURL == "" || req.Username == "" || req.Password == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "jellyfinUrl, username and password are required"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	if err := s.burner.Login(ctx, req.JellyfinURL, req.Username, req.Password); err != nil {
		if errors.Is(err, jellyfin.ErrBadCredentials) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid username or password"})
			return
		}
		log.Printf("login error: %v", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "could not reach Jellyfin"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

func (s *Server) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.burner.Logout()
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

func (s *Server) HandleWatchlist(w http.ResponseWriter, r *http.Request) {
	lbUser := strings.TrimSpace(r.URL.Query().Get("lbuser"))
	if lbUser == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing lbuser query param"})
		return
	}

	resp, err := s.burner.Burn(r.Context(), lbUser, nil)
	if err != nil {
		if errors.Is(err, jellyfin.ErrSessionExpired) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Jellyfin session expired, please log in again"})
			return
		}
		log.Printf("watchlist error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"films": resp.Films, "meta": resp})
}

// HandlePoster resolves a film's poster URL on demand. The frontend falls
// back here when a constructed CDN URL is missing or fails to load.
func (s *Server) HandlePoster(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimSpace(r.URL.Query().Get("slug"))
	if slug == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing slug query param"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	writeJSON(w, http.StatusOK, map[string]string{"posterUrl": s.resolver.Resolve(ctx, slug)})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

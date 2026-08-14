package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sort"
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

// Server wires the HTTP handlers together.
type Server struct {
	store   *jellyfin.SessionStore
	scraper *letterboxd.Scraper
	cache   *watchlistCache

	refreshSem chan struct{} // capacity-1 semaphore serializing watchlist refresh
}

func New(store *jellyfin.SessionStore, scraper *letterboxd.Scraper, cacheTTL time.Duration) *Server {
	if cacheTTL <= 0 {
		cacheTTL = time.Hour
	}
	return &Server{
		store:      store,
		scraper:    scraper,
		cache:      &watchlistCache{ttl: cacheTTL},
		refreshSem: make(chan struct{}, 1),
	}
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

	client := jellyfin.NewClient(req.JellyfinURL)
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	sess, err := client.Authenticate(ctx, req.Username, req.Password)
	if err != nil {
		if errors.Is(err, jellyfin.ErrBadCredentials) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid username or password"})
			return
		}
		log.Printf("login error: %v", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "could not reach Jellyfin"})
		return
	}

	s.store.Set(sess)
	s.cache.invalidate()
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

func (s *Server) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.store.Clear()
	s.cache.invalidate()
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

func (s *Server) HandleWatchlist(w http.ResponseWriter, r *http.Request) {
	lbUser := strings.TrimSpace(r.URL.Query().Get("lbuser"))
	if lbUser == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing lbuser query param"})
		return
	}

	sess := s.store.Get()
	if sess == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not logged in to Jellyfin"})
		return
	}

	// Cache is keyed by letterboxd username only; watched-state is recomputed
	// cheaply when the Jellyfin library refresh re-runs (TTL-based).
	if films, meta, ok := s.cache.get(lbUser); ok {
		writeJSON(w, http.StatusOK, map[string]any{"films": films, "meta": meta})
		return
	}

	s.refreshSem <- struct{}{}
	defer func() { <-s.refreshSem }()

	// Re-check under lock: another request may have refreshed meanwhile.
	if films, meta, ok := s.cache.get(lbUser); ok {
		writeJSON(w, http.StatusOK, map[string]any{"films": films, "meta": meta})
		return
	}

	films, watched, err := s.build(r.Context(), lbUser, nil)
	if err != nil {
		if errors.Is(err, jellyfin.ErrSessionExpired) {
			s.store.Clear()
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Jellyfin session expired, please log in again"})
			return
		}
		log.Printf("watchlist error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	s.cache.put(lbUser, films, watched)

	writeJSON(w, http.StatusOK, map[string]any{"films": films, "meta": watched})
}

// build fetches the Letterboxd watchlist, matches against the Jellyfin library,
// and returns the films to display (watched ones filtered out). onItem, if
// non-nil, is called with each Jellyfin library movie's index, the total
// record count, and the movie as it is fetched.
func (s *Server) build(ctx context.Context, lbUser string, onItem func(index, total int, it jellyfin.Item)) ([]WatchlistFilm, *watchlistResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	sess := s.store.Get()
	if sess == nil {
		return nil, nil, jellyfin.ErrSessionExpired
	}

	lbFilms, err := s.scraper.Watchlist(ctx, lbUser)
	if err != nil {
		return nil, nil, err
	}

	client := jellyfin.NewClient(sess.JellyfinURL)
	movies, err := client.Movies(ctx, sess, distinctYears(lbFilms), onItem)
	if err != nil {
		return nil, nil, err
	}

	matcher := NewMatcher(movies)

	films := make([]WatchlistFilm, 0, len(lbFilms))
	matched := 0
	watched := 0
	for _, f := range lbFilms {
		item := matcher.Match(f)
		wf := WatchlistFilm{
			Slug:          f.Slug,
			Title:         f.Title,
			Year:          f.Year,
			PosterURL:     f.PosterURL,
			LetterboxdURL: f.URL,
		}
		if item != nil {
			wf.InLibrary = true
			wf.ItemID = item.ID
			matched++
			if item.UserData != nil && item.UserData.Played {
				watched++
				continue // hide watched films
			}
		}
		films = append(films, wf)
	}

	// In-library films first, preserving Letterboxd order within each group.
	sort.SliceStable(films, func(i, j int) bool {
		return films[i].InLibrary && !films[j].InLibrary
	})

	meta := &watchlistResponse{
		Films:   films,
		Total:   len(lbFilms),
		Matched: matched,
		Watched: watched,
	}
	return films, meta, nil
}

// distinctYears returns the sorted unique production years (ignoring 0, i.e.
// films whose year failed to parse) from a Letterboxd watchlist. Jellyfin
// items are filtered by these years so only library movies that could match
// are fetched.
func distinctYears(films []letterboxd.Film) []int {
	seen := make(map[int]struct{})
	for _, f := range films {
		if f.Year > 0 {
			seen[f.Year] = struct{}{}
		}
	}
	years := make([]int, 0, len(seen))
	for y := range seen {
		years = append(years, y)
	}
	sort.Ints(years)
	return years
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

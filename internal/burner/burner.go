// Package burner turns a Letterboxd watchlist into the films worth watching:
// it scrapes the watchlist, matches it against a Jellyfin library and hides
// what has already been watched. It owns the bounded result cache, the
// single-flight collapsing and the global scrape concurrency limit.
package burner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"watchlist-burner/internal/cache"
	"watchlist-burner/internal/jellyfin"
	"watchlist-burner/internal/letterboxd"
	"watchlist-burner/internal/match"
)

// ErrUnauthorized means Jellyfin rejected the token.
var ErrUnauthorized = errors.New("burner: jellyfin rejected the token")

// Film is one row of the result sent to the browser.
type Film struct {
	Slug          string `json:"slug"`
	Title         string `json:"title"`
	Year          int    `json:"year"`
	PosterURL     string `json:"posterUrl,omitempty"`
	LetterboxdURL string `json:"letterboxdUrl"`
	ItemID        string `json:"itemId,omitempty"`
	TmdbID        string `json:"tmdbId,omitempty"`
	InLibrary     bool   `json:"inLibrary"`
}

// Result is the burn output plus the counters the UI renders.
type Result struct {
	Films   []Film `json:"films"`
	Total   int    `json:"total"`
	Matched int    `json:"matched"`
	Watched int    `json:"watched"`
}

// Progress receives each library movie as it is fetched, in library order.
type Progress func(index, total int, it jellyfin.Item)

// Config holds the burner's tuning knobs.
type Config struct {
	CacheTTL          time.Duration
	CacheSize         int
	BurnBudget        time.Duration
	ScrapeConcurrency int
	MaxEnrich         int
}

// WatchlistSource is the seam to Letterboxd.
type WatchlistSource interface {
	Watchlist(ctx context.Context, username string) ([]letterboxd.Film, error)
}

// MetadataSource is the seam to per-film metadata (TMDB id, poster).
type MetadataSource interface {
	Meta(ctx context.Context, slug string) (letterboxd.Meta, bool)
}

// Burner is the deep module of the app.
type Burner struct {
	guard   *jellyfin.Guard
	http    *http.Client
	scraper WatchlistSource
	meta    MetadataSource

	cache *cache.LRU[string, *Result]
	group *cache.Group[string, *Result]
	sem   chan struct{}

	cfg Config
}

// New wires a Burner. httpClient must be the guard's client so Jellyfin traffic
// stays inside the SSRF policy.
func New(guard *jellyfin.Guard, httpClient *http.Client, scraper WatchlistSource, meta MetadataSource, cfg Config) *Burner {
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = time.Hour
	}
	if cfg.CacheSize <= 0 {
		cfg.CacheSize = 500
	}
	if cfg.BurnBudget <= 0 {
		cfg.BurnBudget = 60 * time.Second
	}
	if cfg.ScrapeConcurrency <= 0 {
		cfg.ScrapeConcurrency = 8
	}
	if cfg.MaxEnrich < 0 {
		cfg.MaxEnrich = 0
	}
	return &Burner{
		guard:   guard,
		http:    httpClient,
		scraper: scraper,
		meta:    meta,
		cache:   cache.NewLRU[string, *Result](cfg.CacheSize, cfg.CacheTTL),
		group:   &cache.Group[string, *Result]{},
		sem:     make(chan struct{}, cfg.ScrapeConcurrency),
		cfg:     cfg,
	}
}

// Session is a validated Jellyfin server + token pair.
type Session struct {
	BaseURL string
	Token   string
	User    jellyfin.User
}

// Authenticate validates the SSRF policy and the token, returning a session
// that can be reused for one or more burns. Streamed requests call this before
// committing to a 200 so auth failures keep proper HTTP statuses.
func (b *Burner) Authenticate(ctx context.Context, creds jellyfin.Credentials) (Session, error) {
	base, err := b.guard.ValidateURL(creds.BaseURL)
	if err != nil {
		return Session{}, err
	}
	if creds.Token == "" {
		return Session{}, ErrUnauthorized
	}

	client := jellyfin.NewClient(base, creds.Token, b.http)
	user, err := client.Me(ctx)
	if err != nil {
		if errors.Is(err, jellyfin.ErrUnauthorized) {
			return Session{}, ErrUnauthorized
		}
		return Session{}, err
	}
	if creds.UserID != "" && creds.UserID != user.ID {
		return Session{}, ErrUnauthorized
	}
	return Session{BaseURL: base.String(), Token: creds.Token, User: *user}, nil
}

// Burn authenticates then builds (or serves from cache) the watchlist.
func (b *Burner) Burn(ctx context.Context, creds jellyfin.Credentials, lbUser string, progress Progress) (*Result, error) {
	sess, err := b.Authenticate(ctx, creds)
	if err != nil {
		return nil, err
	}
	return b.BurnSession(ctx, sess, lbUser, progress)
}

// BurnSession builds (or serves from cache) the watchlist for an already
// validated session.
func (b *Burner) BurnSession(ctx context.Context, sess Session, lbUser string, progress Progress) (*Result, error) {
	if !letterboxd.ValidUsername(lbUser) {
		return nil, fmt.Errorf("burner: invalid letterboxd username")
	}
	base, err := b.guard.ValidateURL(sess.BaseURL)
	if err != nil {
		return nil, err
	}
	client := jellyfin.NewClient(base, sess.Token, b.http)
	user := sess.User

	key := cacheKey(base.String(), user.ID, lbUser)
	if res, ok := b.cache.Get(key); ok {
		return res, nil
	}

	return b.group.Do(ctx, key, func(ctx context.Context) (*Result, error) {
		if res, ok := b.cache.Get(key); ok {
			return res, nil
		}

		select {
		case b.sem <- struct{}{}:
			defer func() { <-b.sem }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}

		ctx, cancel := context.WithTimeout(ctx, b.cfg.BurnBudget)
		defer cancel()

		res, err := b.build(ctx, client, user.ID, lbUser, progress)
		if err != nil {
			return nil, err
		}
		b.cache.Put(key, res)
		return res, nil
	})
}

// build does the scrape, match and sort. It never returns partial results.
func (b *Burner) build(ctx context.Context, client *jellyfin.Client, userID, lbUser string, progress Progress) (*Result, error) {
	lbFilms, err := b.scraper.Watchlist(ctx, lbUser)
	if err != nil {
		return nil, err
	}

	years := distinctYears(lbFilms)
	if hasUnknownYear(lbFilms) {
		years = nil
	}

	movies, err := client.Movies(ctx, userID, years, func(index, total int, it jellyfin.Item) {
		if progress != nil {
			progress(index, total, it)
		}
	})
	if err != nil {
		if errors.Is(err, jellyfin.ErrUnauthorized) {
			return nil, ErrUnauthorized
		}
		return nil, err
	}

	matcher := match.NewMatcher(movies)

	type resolved struct {
		film letterboxd.Film
		item *jellyfin.Item
		tmdb string
	}
	rows := make([]resolved, len(lbFilms))
	unmatched := make([]int, 0, len(lbFilms))
	for i, f := range lbFilms {
		it := matcher.Match(f)
		rows[i] = resolved{film: f, item: it}
		if it == nil {
			unmatched = append(unmatched, i)
		}
	}

	// Only the films title matching could not resolve are worth a film-page
	// request, and only up to the configured budget.
	if b.cfg.MaxEnrich > 0 && len(unmatched) > 0 {
		if len(unmatched) > b.cfg.MaxEnrich {
			unmatched = unmatched[:b.cfg.MaxEnrich]
		}
		for _, i := range unmatched {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			f := &rows[i].film
			meta, ok := b.meta.Meta(ctx, f.Slug)
			if !ok {
				continue
			}
			if f.PosterURL == "" && meta.PosterURL != "" {
				f.PosterURL = meta.PosterURL
			}
			rows[i].tmdb = meta.TmdbID
			if it := matcher.MatchTmdb(meta.TmdbID); it != nil {
				rows[i].item = it
			}
		}
	}

	films := make([]Film, 0, len(rows))
	matched, watched := 0, 0
	for _, row := range rows {
		f := row.film
		out := Film{
			Slug:          f.Slug,
			Title:         f.Title,
			Year:          f.Year,
			PosterURL:     f.PosterURL,
			LetterboxdURL: f.URL,
			TmdbID:        row.tmdb,
		}
		if it := row.item; it != nil {
			if out.TmdbID == "" {
				out.TmdbID = it.TmdbID()
			}
			out.InLibrary = true
			out.ItemID = it.ID
			matched++
			if it.UserData != nil && it.UserData.Played {
				watched++
				continue // watched films are hidden
			}
		}
		films = append(films, out)
	}

	// In-library films first, keeping Letterboxd order inside each group.
	sort.SliceStable(films, func(i, j int) bool {
		return films[i].InLibrary && !films[j].InLibrary
	})

	return &Result{Films: films, Total: len(lbFilms), Matched: matched, Watched: watched}, nil
}

func cacheKey(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func distinctYears(films []letterboxd.Film) []int {
	seen := make(map[int]struct{}, len(films))
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

func hasUnknownYear(films []letterboxd.Film) bool {
	for _, f := range films {
		if f.Year <= 0 {
			return true
		}
	}
	return false
}

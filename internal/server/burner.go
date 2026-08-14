package server

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"watchlist-burner/internal/jellyfin"
	"watchlist-burner/internal/letterboxd"
)

// ProgressFunc receives each library movie as it is fetched, with its 0-based
// index and the total record count.
type ProgressFunc func(index, total int, it jellyfin.Item)

// Library is the seam to the Jellyfin library. sess carries the credentials
// for the request; years restricts the fetch to matching production years
// (empty means no restriction).
type Library interface {
	Movies(ctx context.Context, sess *jellyfin.Session, years []int, onItem ProgressFunc) ([]jellyfin.Item, error)
}

// WatchlistSource is the seam to the Letterboxd watchlist scraper.
type WatchlistSource interface {
	Watchlist(ctx context.Context, username string) ([]letterboxd.Film, error)
}

// JellyfinLibrary adapts the Jellyfin REST client to the Library seam. It is
// stateless: the client is created per call from the session's server URL.
type JellyfinLibrary struct{}

func NewJellyfinLibrary() *JellyfinLibrary {
	return &JellyfinLibrary{}
}

func (JellyfinLibrary) Movies(ctx context.Context, sess *jellyfin.Session, years []int, onItem ProgressFunc) ([]jellyfin.Item, error) {
	return jellyfin.NewClient(sess.JellyfinURL).Movies(ctx, sess, years, onItem)
}

// Burner is the module that turns a Letterboxd watchlist into the films worth
// watching. Behind a small interface it owns the single Jellyfin session, the
// watchlist result cache, refresh serialization, matching, and session-expiry
// handling.
type Burner struct {
	lib     Library
	scraper WatchlistSource
	ttl     time.Duration

	mu      sync.RWMutex
	session *jellyfin.Session

	cache *watchlistCache
	sem   chan struct{} // capacity-1 semaphore serializing refreshes
}

func NewBurner(lib Library, scraper WatchlistSource, cacheTTL time.Duration) *Burner {
	if cacheTTL <= 0 {
		cacheTTL = time.Hour
	}
	return &Burner{
		lib:     lib,
		scraper: scraper,
		ttl:     cacheTTL,
		cache:   &watchlistCache{ttl: cacheTTL},
		sem:     make(chan struct{}, 1),
	}
}

// Login authenticates against Jellyfin and replaces any current session,
// dropping the cached watchlist.
func (b *Burner) Login(ctx context.Context, url, username, password string) error {
	sess, err := jellyfin.NewClient(url).Authenticate(ctx, username, password)
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.session = sess
	b.mu.Unlock()
	b.cache.invalidate()
	return nil
}

// Logout clears the session and the cached watchlist.
func (b *Burner) Logout() {
	b.mu.Lock()
	b.session = nil
	b.mu.Unlock()
	b.cache.invalidate()
}

// Burn returns the burned watchlist for a Letterboxd user: every watchlist
// film that is neither watched nor absent from the Jellyfin library. A warm
// cache is served without taking the refresh semaphore; session expiry clears
// the session and is reported to the caller.
func (b *Burner) Burn(ctx context.Context, lbUser string, onItem ProgressFunc) (*watchlistResponse, error) {
	b.mu.RLock()
	sess := b.session
	b.mu.RUnlock()
	if sess == nil {
		return nil, jellyfin.ErrSessionExpired
	}

	if resp, ok := b.cache.get(lbUser); ok {
		return resp, nil
	}

	b.sem <- struct{}{}
	defer func() { <-b.sem }()

	// Re-check under the semaphore: another request may have refreshed meanwhile.
	if resp, ok := b.cache.get(lbUser); ok {
		return resp, nil
	}

	resp, err := b.build(ctx, sess, lbUser, onItem)
	if err != nil {
		if errors.Is(err, jellyfin.ErrSessionExpired) {
			b.mu.Lock()
			b.session = nil
			b.mu.Unlock()
			b.cache.invalidate()
		}
		return nil, err
	}

	b.cache.put(lbUser, resp)
	return resp, nil
}

// build scrapes the Letterboxd watchlist, matches it against the Jellyfin
// library, and returns the films to display with the watchlist meta counts.
func (b *Burner) build(ctx context.Context, sess *jellyfin.Session, lbUser string, onItem ProgressFunc) (*watchlistResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	lbFilms, err := b.scraper.Watchlist(ctx, lbUser)
	if err != nil {
		return nil, err
	}

	// The year filter is an optimization: only fetch library movies that could
	// match. A film whose year failed to parse could match any edition, so a
	// full fetch is required when one is present.
	years := distinctYears(lbFilms)
	if hasUnknownYear(lbFilms) {
		years = nil
	}
	movies, err := b.lib.Movies(ctx, sess, years, onItem)
	if err != nil {
		return nil, err
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

	return &watchlistResponse{
		Films:   films,
		Total:   len(lbFilms),
		Matched: matched,
		Watched: watched,
	}, nil
}

// distinctYears returns the sorted unique production years (ignoring 0, i.e.
// films whose year failed to parse) from a Letterboxd watchlist.
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

// hasUnknownYear reports whether any watchlist film has an unparsed year.
func hasUnknownYear(films []letterboxd.Film) bool {
	for _, f := range films {
		if f.Year <= 0 {
			return true
		}
	}
	return false
}

package letterboxd

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sync"
	"time"

	"watchlist-burner/internal/cache"
)

// Meta is the per-film metadata fetched from a Letterboxd film page.
type Meta struct {
	TmdbID    string
	PosterURL string
}

var (
	tmdbRe   = regexp.MustCompile(`data-tmdb-id="(\d+)"`)
	posterRe = regexp.MustCompile(`"image":"(https://a\.ltrbxd\.com/resized/[^"]+-0-\d+-0-\d+-crop\.jpg[^"]*)"`)
	cropRe   = regexp.MustCompile(`-0-\d+-0-\d+-crop\.jpg`)
	slugRe   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,200}$`)
)

// MetadataResolver fetches film pages on demand and caches the result. It is
// used both for poster fallbacks and for TMDB-id enrichment of films that title
// matching could not resolve.
type MetadataResolver struct {
	client *http.Client
	delay  time.Duration
	cache  *cache.LRU[string, Meta]
	group  *cache.Group[string, Meta]

	// pace serializes outbound film-page requests so bursty enrichment cannot
	// hammer Letterboxd.
	paceMu sync.Mutex
	last   time.Time
}

// NewMetadataResolver builds a resolver caching film metadata for 6h.
func NewMetadataResolver(client *http.Client, delay time.Duration) *MetadataResolver {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	if delay <= 0 {
		delay = 250 * time.Millisecond
	}
	return &MetadataResolver{
		client: client,
		delay:  delay,
		cache:  cache.NewLRU[string, Meta](2048, 6*time.Hour),
		group:  &cache.Group[string, Meta]{},
	}
}

// ValidSlug reports whether a slug is safe to put in a film URL.
func ValidSlug(slug string) bool { return slugRe.MatchString(slug) }

// Meta returns metadata for a film slug. The bool reports whether the page was
// fetched successfully; a failed fetch is not cached.
func (r *MetadataResolver) Meta(ctx context.Context, slug string) (Meta, bool) {
	if !ValidSlug(slug) {
		return Meta{}, false
	}
	if m, ok := r.cache.Get(slug); ok {
		return m, true
	}

	m, err := r.group.Do(ctx, slug, func(ctx context.Context) (Meta, error) {
		if m, ok := r.cache.Get(slug); ok {
			return m, nil
		}
		m, err := r.fetch(ctx, slug)
		if err != nil {
			return Meta{}, err
		}
		r.cache.Put(slug, m)
		return m, nil
	})
	if err != nil {
		return Meta{}, false
	}
	return m, true
}

func (r *MetadataResolver) fetch(ctx context.Context, slug string) (Meta, error) {
	if err := r.wait(ctx); err != nil {
		return Meta{}, err
	}

	u := fmt.Sprintf("%s/film/%s/", baseURL, slug)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Meta{}, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html")

	resp, err := r.client.Do(req)
	if err != nil {
		return Meta{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Meta{}, fmt.Errorf("letterboxd: film %s: status %d", slug, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Meta{}, err
	}

	var m Meta
	if match := tmdbRe.FindSubmatch(body); len(match) == 2 {
		m.TmdbID = string(match[1])
	}
	if match := posterRe.FindSubmatch(body); len(match) == 2 {
		m.PosterURL = cropRe.ReplaceAllString(string(match[1]), "-0-125-0-187-crop.jpg")
	}
	return m, nil
}

func (r *MetadataResolver) wait(ctx context.Context) error {
	r.paceMu.Lock()
	defer r.paceMu.Unlock()

	if !r.last.IsZero() {
		if d := r.delay - time.Since(r.last); d > 0 {
			if err := sleep(ctx, d); err != nil {
				return err
			}
		}
	}
	r.last = time.Now()
	return nil
}

package letterboxd

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/net/html"
)

const (
	baseURL       = "https://letterboxd.com"
	posterBaseURL = "https://a.ltrbxd.com/resized"
	userAgent     = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126 Safari/537.36"
)

// Options configures a Scraper.
type Options struct {
	Client   *http.Client
	Delay    time.Duration // pacing between pages
	MaxPages int
	MaxFilms int
}

// Scraper fetches and parses Letterboxd watchlist pages.
type Scraper struct {
	client   *http.Client
	delay    time.Duration
	maxPages int
	maxFilms int
}

// NewScraper builds a Scraper with sane, abuse-resistant defaults.
func NewScraper(opts Options) *Scraper {
	if opts.Client == nil {
		opts.Client = &http.Client{Timeout: 15 * time.Second}
	}
	if opts.Delay <= 0 {
		opts.Delay = 250 * time.Millisecond
	}
	if opts.MaxPages <= 0 {
		opts.MaxPages = 50
	}
	if opts.MaxFilms <= 0 {
		opts.MaxFilms = 5000
	}
	return &Scraper{client: opts.Client, delay: opts.Delay, maxPages: opts.MaxPages, maxFilms: opts.MaxFilms}
}

// Watchlist returns every film on a user's watchlist, walking all pages. Poster
// URLs are constructed where possible; the rest are resolved lazily by the
// metadata resolver.
func (s *Scraper) Watchlist(ctx context.Context, username string) ([]Film, error) {
	if !ValidUsername(username) {
		return nil, fmt.Errorf("letterboxd: invalid username %q", username)
	}

	films := make([]Film, 0, 50)
	for page := 1; ; page++ {
		if page > s.maxPages {
			return nil, fmt.Errorf("letterboxd: watchlist exceeds %d pages", s.maxPages)
		}
		if page > 1 {
			if err := sleep(ctx, s.delay); err != nil {
				return nil, err
			}
		}

		pageFilms, hasNext, err := s.watchlistPage(ctx, username, page)
		if err != nil {
			return nil, err
		}
		films = append(films, pageFilms...)
		if len(films) > s.maxFilms {
			return nil, fmt.Errorf("letterboxd: watchlist exceeds %d films", s.maxFilms)
		}
		if !hasNext {
			break
		}
	}
	return films, nil
}

func (s *Scraper) watchlistPage(ctx context.Context, username string, page int) ([]Film, bool, error) {
	u := fmt.Sprintf("%s/%s/watchlist/", baseURL, url.PathEscape(username))
	if page > 1 {
		u = fmt.Sprintf("%s/%s/watchlist/page/%d/", baseURL, url.PathEscape(username), page)
	}

	resp, err := s.get(ctx, u)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()

	doc, err := html.Parse(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, false, fmt.Errorf("letterboxd: parse watchlist page %d: %w", page, err)
	}

	var (
		films   []Film
		hasNext bool
		walk    func(*html.Node)
	)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch {
			case n.Data == "a" && hasClass(n, "next"):
				hasNext = true
			case n.Data == "div" && attr(n, "data-component-class") == "LazyPoster":
				if f := parsePoster(n); f != nil {
					films = append(films, *f)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	return films, hasNext, nil
}

// get performs a paced request with bounded retries on transient failures.
func (s *Scraper) get(ctx context.Context, u string) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			if err := sleep(ctx, time.Duration(attempt)*500*time.Millisecond); err != nil {
				return nil, err
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Accept", "text/html,application/xhtml+xml")

		resp, err := s.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("letterboxd: fetch %s: %w", u, err)
			continue
		}

		switch {
		case resp.StatusCode == http.StatusOK:
			return resp, nil
		case resp.StatusCode == http.StatusNotFound:
			resp.Body.Close()
			return nil, ErrWatchlistNotFound
		case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode == http.StatusForbidden,
			resp.StatusCode >= 500:
			resp.Body.Close()
			lastErr = fmt.Errorf("letterboxd: status %d fetching %s", resp.StatusCode, u)
			continue
		default:
			resp.Body.Close()
			return nil, fmt.Errorf("letterboxd: status %d fetching %s", resp.StatusCode, u)
		}
	}
	return nil, lastErr
}

func parsePoster(n *html.Node) *Film {
	slug := attr(n, "data-item-slug")
	fullName := attr(n, "data-item-full-display-name")
	if fullName == "" {
		fullName = attr(n, "data-item-name")
	}
	if slug == "" || fullName == "" {
		return nil
	}

	title, year := splitName(fullName)

	var uid string
	if m := uidRe.FindStringSubmatch(attr(n, "data-postered-identifier")); len(m) == 2 {
		uid = m[1]
	}

	f := &Film{
		Slug:  slug,
		Title: title,
		Year:  year,
		URL:   fmt.Sprintf("%s/film/%s/", baseURL, slug),
	}
	if uid != "" {
		f.PosterURL = posterURL(uid, slug)
	}
	return f
}

func attr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}

func hasClass(n *html.Node, class string) bool {
	for _, f := range splitFields(attr(n, "class")) {
		if f == class {
			return true
		}
	}
	return false
}

func splitFields(s string) []string {
	var out []string
	start := -1
	for i, r := range s {
		if r == ' ' || r == '\t' || r == '\n' {
			if start >= 0 {
				out = append(out, s[start:i])
				start = -1
			}
			continue
		}
		if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		out = append(out, s[start:])
	}
	return out
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

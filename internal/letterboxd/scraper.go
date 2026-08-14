package letterboxd

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/net/html"
)

const (
	baseURL       = "https://letterboxd.com"
	posterBaseURL = "https://a.ltrbxd.com/resized"
	userAgent     = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120 Safari/537.36"
)

// Film is a single entry scraped from a Letterboxd watchlist page.
type Film struct {
	Slug      string
	Title     string
	Year      int
	PosterURL string
	URL       string
}

var (
	fullNameRe = regexp.MustCompile(`^(.*?)\s+\((\d{4})\)$`)
	uidRe      = regexp.MustCompile(`"uid":"film:(\d+)"`)
	posterJSON = regexp.MustCompile(`"image":"(https://a\.ltrbxd\.com/resized/[^"]+-0-600-0-900-crop\.jpg[^"]*)"`)
)

// Scraper fetches and parses Letterboxd watchlist pages.
type Scraper struct {
	client *http.Client
}

func NewScraper() *Scraper {
	return &Scraper{client: &http.Client{}}
}

// Watchlist returns every film on a user's watchlist, walking all pages.
// Posters come from the constructed CDN URL where possible; films whose
// constructed URL is invalid (custom uploads, renamed slugs) fall back to
// the film page's JSON-LD image.
func (s *Scraper) Watchlist(ctx context.Context, username string) ([]Film, error) {
	var films []Film
	page := 1
	for {
		pageFilms, hasNext, err := s.watchlistPage(ctx, username, page)
		if err != nil {
			return nil, err
		}
		films = append(films, pageFilms...)
		if !hasNext {
			break
		}
		page++
	}
	s.resolvePosters(ctx, films)
	return films, nil
}

// resolvePosters fills in PosterURL for any film missing one. It first
// verifies the constructed CDN URL (cheap HEAD to the image CDN); for films
// whose constructed URL is invalid it falls back to the film page's JSON-LD
// image, rewritten to a small crop. Falls back to empty-poster placeholder.
func (s *Scraper) resolvePosters(ctx context.Context, films []Film) {
	// Phase 1: HEAD-check all constructed URLs in parallel.
	needsFilm := make([]int, 0, len(films))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i := range films {
		f := &films[i]
		if f.PosterURL == "" {
			mu.Lock()
			needsFilm = append(needsFilm, i)
			mu.Unlock()
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()
			ok := s.headOK(ctx, films[idx].PosterURL)
			if !ok {
				mu.Lock()
				needsFilm = append(needsFilm, idx)
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	if len(needsFilm) == 0 {
		return
	}

	// Phase 2: fetch film pages for the remaining films.
	var wg2 sync.WaitGroup
	sem2 := make(chan struct{}, 4)
	for _, idx := range needsFilm {
		f := &films[idx]
		wg2.Add(1)
		sem2 <- struct{}{}
		go func() {
			defer wg2.Done()
			defer func() { <-sem2 }()
			f.PosterURL = s.filmPoster(ctx, f.Slug)
		}()
	}
	wg2.Wait()
}

func (s *Scraper) headOK(ctx context.Context, url string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := s.client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// filmPoster fetches a film page and extracts its poster from JSON-LD,
// rewritten to a small crop. Returns empty string on any failure.
func (s *Scraper) filmPoster(ctx context.Context, slug string) string {
	u := fmt.Sprintf("%s/film/%s/", baseURL, slug)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := s.client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ""
	}
	if m := posterJSON.FindSubmatch(body); len(m) == 2 {
		return smallCrop(string(m[1]))
	}
	return ""
}

// smallCrop rewrites a poster URL's crop suffix to a 125x187 thumbnail.
func smallCrop(u string) string {
	re := regexp.MustCompile(`-0-\d+-0-\d+-crop\.jpg`)
	return re.ReplaceAllString(u, "-0-125-0-187-crop.jpg")
}

func (s *Scraper) watchlistPage(ctx context.Context, username string, page int) ([]Film, bool, error) {
	u := fmt.Sprintf("%s/%s/watchlist/", baseURL, url.PathEscape(username))
	if page > 1 {
		u = fmt.Sprintf("%s/%s/watchlist/page/%d/", baseURL, url.PathEscape(username), page)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("fetch watchlist page %d: %w", page, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, false, fmt.Errorf("letterboxd: watchlist for %q not found (HTTP 404)", username)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("letterboxd: unexpected status %d fetching watchlist page %d", resp.StatusCode, page)
	}

	doc, err := html.Parse(io.Reader(resp.Body))
	if err != nil {
		return nil, false, fmt.Errorf("letterboxd: parse watchlist page %d: %w", page, err)
	}

	var films []Film
	var hasNext bool
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if n.Data == "a" && attr(n, "class") == "next" {
				hasNext = true
			}
			if n.Data == "div" && attr(n, "data-component-class") == "LazyPoster" {
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

func splitName(fullName string) (string, int) {
	if m := fullNameRe.FindStringSubmatch(fullName); len(m) == 3 {
		year, err := strconv.Atoi(m[2])
		if err == nil {
			return m[1], year
		}
	}
	return fullName, 0
}

// posterURL builds the Letterboxd CDN poster URL from the internal film id
// and slug. The CDN layout is /film-poster/{each digit}/{id}-{slug}-0-125-0-187-crop.jpg
func posterURL(uid, slug string) string {
	var digits []string
	for _, r := range uid {
		digits = append(digits, string(r))
	}
	return fmt.Sprintf("%s/film-poster/%s/%s-%s-0-125-0-187-crop.jpg",
		posterBaseURL, strings.Join(digits, "/"), uid, slug)
}

func attr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}

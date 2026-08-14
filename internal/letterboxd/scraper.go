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
)

// Scraper fetches and parses Letterboxd watchlist pages.
type Scraper struct {
	client *http.Client
}

func NewScraper() *Scraper {
	return &Scraper{client: &http.Client{}}
}

// Watchlist returns every film on a user's watchlist, walking all pages.
// PosterURL holds the constructed CDN URL where one can be derived; films
// whose URL is missing or invalid are resolved lazily via PosterResolver.
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
	return films, nil
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

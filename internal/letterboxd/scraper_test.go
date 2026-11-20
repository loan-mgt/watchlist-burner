package letterboxd

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// rewriteTransport redirects the package's absolute Letterboxd URLs to a local
// test server, keeping the request path intact.
type rewriteTransport struct {
	base string
	next http.RoundTripper
}

func (t rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = "http"
	clone.URL.Host = strings.TrimPrefix(t.base, "http://")
	return t.next.RoundTrip(clone)
}

func testScraper(t *testing.T, handler http.HandlerFunc) *Scraper {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	client := &http.Client{Transport: rewriteTransport{base: srv.URL, next: http.DefaultTransport}}
	return NewScraper(Options{Client: client, Delay: 0, MaxPages: 5, MaxFilms: 100})
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestWatchlistParsesFilmsAndPagination(t *testing.T) {
	fixture := readFixture(t, "watchlist.html")

	var paths []string
	scraper := testScraper(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Header.Get("User-Agent") == "" {
			t.Error("missing User-Agent")
		}
		if strings.HasSuffix(r.URL.Path, "/page/2/") {
			_, _ = w.Write([]byte(`<html><body><div class="poster-grid"></div></body></html>`))
			return
		}
		_, _ = w.Write(fixture)
	})

	films, err := scraper.Watchlist(context.Background(), "someone")
	if err != nil {
		t.Fatalf("Watchlist: %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("requested %d pages (%v), want 2", len(paths), paths)
	}
	if len(films) != 3 {
		t.Fatalf("films = %d, want 3", len(films))
	}

	first := films[0]
	if first.Slug != "the-game" || first.Title != "The Game" || first.Year != 1997 {
		t.Errorf("first film = %+v", first)
	}
	if want := "https://a.ltrbxd.com/resized/film-poster/5/0/1/6/8/50168-the-game-0-125-0-187-crop.jpg"; first.PosterURL != want {
		t.Errorf("poster = %q, want %q", first.PosterURL, want)
	}
	if first.URL != "https://letterboxd.com/film/the-game/" {
		t.Errorf("URL = %q", first.URL)
	}

	second := films[1]
	if second.Title != "& Juliet" || second.Year != 2022 {
		t.Errorf("second film = %+v (entities must be decoded)", second)
	}

	third := films[2]
	if third.Title != "Unknown Year Film" || third.Year != 0 || third.PosterURL != "" {
		t.Errorf("third film = %+v", third)
	}
}

func TestWatchlistNotFound(t *testing.T) {
	scraper := testScraper(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	_, err := scraper.Watchlist(context.Background(), "someone")
	if !errors.Is(err, ErrWatchlistNotFound) {
		t.Fatalf("err = %v, want ErrWatchlistNotFound", err)
	}
}

func TestWatchlistRejectsInvalidUsername(t *testing.T) {
	scraper := testScraper(t, func(http.ResponseWriter, *http.Request) {
		t.Error("server must not be contacted")
	})

	if _, err := scraper.Watchlist(context.Background(), "../admin"); err == nil {
		t.Fatal("expected an error for a path-traversal username")
	}
}

func TestWatchlistStopsAtPageLimit(t *testing.T) {
	fixture := []byte(`<div data-component-class="LazyPoster" data-item-name="A (2000)" data-item-slug="a"></div><a class="next">Older</a>`)
	scraper := testScraper(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(fixture)
	})
	scraper.maxPages = 3

	_, err := scraper.Watchlist(context.Background(), "someone")
	if err == nil || !strings.Contains(err.Error(), "exceeds 3 pages") {
		t.Fatalf("err = %v, want page-limit error", err)
	}
}

func TestWatchlistStopsAtFilmLimit(t *testing.T) {
	fixture := readFixture(t, "watchlist.html")
	scraper := testScraper(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(fixture)
	})
	scraper.maxFilms = 2

	_, err := scraper.Watchlist(context.Background(), "someone")
	if err == nil || !strings.Contains(err.Error(), "exceeds 2 films") {
		t.Fatalf("err = %v, want film-limit error", err)
	}
}

func TestWatchlistRetriesTransientFailures(t *testing.T) {
	// A single page with no next link keeps the test focused on retries.
	fixture := []byte(`<div data-component-class="LazyPoster" data-item-name="A (2000)" data-item-slug="a"></div>`)
	attempts := 0
	scraper := testScraper(t, func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write(fixture)
	})

	if _, err := scraper.Watchlist(context.Background(), "someone"); err != nil {
		t.Fatalf("Watchlist: %v", err)
	}
	if attempts < 3 {
		t.Fatalf("attempts = %d, want at least 3", attempts)
	}
}

func TestValidUsername(t *testing.T) {
	for _, name := range []string{"someone", "some-one_2", "A1"} {
		if !ValidUsername(name) {
			t.Errorf("ValidUsername(%q) = false", name)
		}
	}
	for _, name := range []string{"", "../x", "a/b", "user name", strings.Repeat("a", 65)} {
		if ValidUsername(name) {
			t.Errorf("ValidUsername(%q) = true", name)
		}
	}
}

func TestMetadataResolver(t *testing.T) {
	filmPage := readFixture(t, "film.html")

	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/film/the-game/" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write(filmPage)
	}))
	t.Cleanup(srv.Close)

	client := &http.Client{Transport: rewriteTransport{base: srv.URL, next: http.DefaultTransport}}
	resolver := NewMetadataResolver(client, time.Millisecond)

	meta, ok := resolver.Meta(context.Background(), "the-game")
	if !ok {
		t.Fatal("Meta returned ok=false")
	}
	if meta.TmdbID != "2649" {
		t.Errorf("TmdbID = %q, want 2649", meta.TmdbID)
	}
	if want := "https://a.ltrbxd.com/resized/film-poster/5/0/1/6/8/50168-the-game-0-125-0-187-crop.jpg?v=abc"; meta.PosterURL != want {
		t.Errorf("PosterURL = %q, want %q", meta.PosterURL, want)
	}

	if _, ok := resolver.Meta(context.Background(), "the-game"); !ok {
		t.Fatal("cached Meta returned ok=false")
	}
	if requests != 1 {
		t.Errorf("requests = %d, want 1 (cached)", requests)
	}

	if _, ok := resolver.Meta(context.Background(), "../etc/passwd"); ok {
		t.Error("invalid slug must not resolve")
	}
	if !ValidSlug("the-game-2024") || ValidSlug("a/b") {
		t.Error("ValidSlug is wrong")
	}
}

func TestMetadataResolverFailureIsNotCached(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	client := &http.Client{Transport: rewriteTransport{base: srv.URL, next: http.DefaultTransport}}
	resolver := NewMetadataResolver(client, time.Millisecond)

	if _, ok := resolver.Meta(context.Background(), "the-game"); ok {
		t.Fatal("failed fetch must report ok=false")
	}
}

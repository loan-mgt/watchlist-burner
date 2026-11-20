package burner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"watchlist-burner/internal/jellyfin"
	"watchlist-burner/internal/letterboxd"
)

type stubScraper struct {
	calls atomic.Int32
	delay time.Duration
	films []letterboxd.Film
}

func (s *stubScraper) Watchlist(context.Context, string) ([]letterboxd.Film, error) {
	s.calls.Add(1)
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	return s.films, nil
}

type stubMeta struct {
	tmdb map[string]string
}

func (s stubMeta) Meta(_ context.Context, slug string) (letterboxd.Meta, bool) {
	id, ok := s.tmdb[slug]
	if !ok {
		return letterboxd.Meta{}, true
	}
	return letterboxd.Meta{TmdbID: id, PosterURL: "https://poster/" + slug}, true
}

func fakeLibrary(t *testing.T, items string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			t.Error("missing Authorization header")
		}
		switch r.URL.Path {
		case "/Users/Me":
			_, _ = w.Write([]byte(`{"Id":"user-1","Name":"loan","ServerId":"srv"}`))
		case "/Items":
			_, _ = w.Write([]byte(`{"Items":` + items + `,"TotalRecordCount":1}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newBurner(t *testing.T, scraper WatchlistSource, meta MetadataSource, cfg Config) *Burner {
	t.Helper()
	guard := jellyfin.NewGuard(true, true, nil)
	return New(guard, guard.HTTPClient(5*time.Second), scraper, meta, cfg)
}

func creds(jf *httptest.Server) jellyfin.Credentials {
	return jellyfin.Credentials{BaseURL: jf.URL, Token: "tok", UserID: "user-1"}
}

func TestBurnCachesResults(t *testing.T) {
	jf := fakeLibrary(t, `[{"Id":"1","Name":"The Game","ProductionYear":1997,"UserData":{"Played":false}}]`)
	scraper := &stubScraper{films: []letterboxd.Film{{Slug: "the-game", Title: "The Game", Year: 1997}}}
	b := newBurner(t, scraper, stubMeta{}, Config{CacheTTL: time.Hour, CacheSize: 4})

	for i := 0; i < 3; i++ {
		res, err := b.Burn(context.Background(), creds(jf), "someone", nil)
		if err != nil {
			t.Fatalf("Burn: %v", err)
		}
		if len(res.Films) != 1 || !res.Films[0].InLibrary {
			t.Fatalf("result = %+v", res)
		}
	}
	if got := scraper.calls.Load(); got != 1 {
		t.Errorf("scrape calls = %d, want 1 (cached)", got)
	}
}

func TestBurnCollapsesConcurrentRequests(t *testing.T) {
	jf := fakeLibrary(t, `[{"Id":"1","Name":"The Game","ProductionYear":1997,"UserData":{"Played":false}}]`)
	scraper := &stubScraper{
		delay: 50 * time.Millisecond,
		films: []letterboxd.Film{{Slug: "the-game", Title: "The Game", Year: 1997}},
	}
	b := newBurner(t, scraper, stubMeta{}, Config{CacheTTL: time.Hour, CacheSize: 4})

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := b.Burn(context.Background(), creds(jf), "someone", nil); err != nil {
				t.Errorf("Burn: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := scraper.calls.Load(); got != 1 {
		t.Errorf("scrape calls = %d, want 1 (single flight)", got)
	}
}

func TestBurnMatchesByTmdbWhenTitleDiffers(t *testing.T) {
	jf := fakeLibrary(t, `[{"Id":"1","Name":"The Original French Title","ProductionYear":1990,"UserData":{"Played":false},"ProviderIds":{"Tmdb":"999"}}]`)
	scraper := &stubScraper{films: []letterboxd.Film{
		{Slug: "english-title", Title: "English Title", Year: 1990},
	}}
	b := newBurner(t, scraper, stubMeta{tmdb: map[string]string{"english-title": "999"}}, Config{
		CacheTTL:  time.Hour,
		CacheSize: 4,
		MaxEnrich: 5,
	})

	res, err := b.Burn(context.Background(), creds(jf), "someone", nil)
	if err != nil {
		t.Fatalf("Burn: %v", err)
	}
	if !res.Films[0].InLibrary || res.Films[0].ItemID != "1" {
		t.Fatalf("TMDB enrichment did not match: %+v", res.Films[0])
	}
}

func TestBurnRejectsBadCredentials(t *testing.T) {
	jf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(jf.Close)

	b := newBurner(t, &stubScraper{}, stubMeta{}, Config{})
	if _, err := b.Burn(context.Background(), creds(jf), "someone", nil); err != ErrUnauthorized {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestBurnRejectsUserMismatch(t *testing.T) {
	jf := fakeLibrary(t, `[]`)
	b := newBurner(t, &stubScraper{}, stubMeta{}, Config{})

	bad := creds(jf)
	bad.UserID = "someone-else"
	if _, err := b.Burn(context.Background(), bad, "someone", nil); err != ErrUnauthorized {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestBurnHidesWatchedAndSorts(t *testing.T) {
	jf := fakeLibrary(t, `[
		{"Id":"1","Name":"The Game","ProductionYear":1997,"UserData":{"Played":true}},
		{"Id":"2","Name":"Heat","ProductionYear":1995,"UserData":{"Played":false}}
	]`)
	scraper := &stubScraper{films: []letterboxd.Film{
		{Slug: "missing", Title: "Missing", Year: 2001},
		{Slug: "the-game", Title: "The Game", Year: 1997},
		{Slug: "heat", Title: "Heat", Year: 1995},
	}}
	b := newBurner(t, scraper, stubMeta{}, Config{CacheTTL: time.Hour, CacheSize: 4})

	res, err := b.Burn(context.Background(), creds(jf), "someone", nil)
	if err != nil {
		t.Fatalf("Burn: %v", err)
	}
	if res.Watched != 1 {
		t.Errorf("watched = %d, want 1", res.Watched)
	}
	if len(res.Films) != 2 {
		t.Fatalf("films = %d, want 2", len(res.Films))
	}
	if res.Films[0].Slug != "heat" || res.Films[1].Slug != "missing" {
		t.Errorf("sort order = %q, %q", res.Films[0].Slug, res.Films[1].Slug)
	}
}

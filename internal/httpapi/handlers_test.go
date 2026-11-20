package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"watchlist-burner/internal/burner"
	"watchlist-burner/internal/config"
	"watchlist-burner/internal/jellyfin"
	"watchlist-burner/internal/letterboxd"
	"watchlist-burner/internal/seerr"
)

type fakeWatchlist struct {
	films []letterboxd.Film
	err   error
}

func (f fakeWatchlist) Watchlist(context.Context, string) ([]letterboxd.Film, error) {
	return f.films, f.err
}

type fakeMeta struct{}

func (fakeMeta) Meta(_ context.Context, slug string) (letterboxd.Meta, bool) {
	return letterboxd.Meta{PosterURL: "https://poster.example/" + slug, TmdbID: "2649"}, true
}

func testConfig() config.Config {
	return config.Config{
		Addr:               ":0",
		CacheTTL:           time.Hour,
		CacheSize:          10,
		BurnBudget:         20 * time.Second,
		ScrapeConcurrency:  1,
		MaxFilms:           100,
		MaxWatchlistPages:  10,
		MaxEnrich:          0,
		RateLimitPerMinute: 100,
		MaxBodyBytes:       1 << 20,
		LogFormat:          "json",
		TrustProxyHeaders:  true,
	}
}

var testAssets = fstest.MapFS{
	"index.html":              {Data: []byte("<!doctype html><title>test</title>")},
	"assets/app.abc123.js":    {Data: []byte(`console.log("hello")`)},
	"assets/app.abc123.js.br": {Data: []byte("brotli-bytes")},
}

func newTestAPI(t *testing.T, cfg config.Config, jf http.HandlerFunc, films []letterboxd.Film) (apiURL, jellyfinURL string) {
	t.Helper()
	return newTestAPIWith(t, cfg, jf, films, nil)
}

func newTestAPIWith(t *testing.T, cfg config.Config, jf http.HandlerFunc, films []letterboxd.Film, discoverer *seerr.Discoverer) (apiURL, jellyfinURL string) {
	t.Helper()

	jfSrv := httptest.NewServer(jf)
	t.Cleanup(jfSrv.Close)

	guard := jellyfin.NewGuard(true, true, nil)
	burn := burner.New(
		guard,
		guard.HTTPClient(10*time.Second),
		fakeWatchlist{films: films},
		fakeMeta{},
		burner.Config{
			CacheTTL:          time.Hour,
			CacheSize:         10,
			BurnBudget:        20 * time.Second,
			ScrapeConcurrency: 1,
			MaxEnrich:         0,
		},
	)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(New(burn, fakeMeta{}, cfg, testAssets, logger, "test", discoverer).Handler())
	t.Cleanup(srv.Close)

	return srv.URL, jfSrv.URL
}

func fakeJellyfin(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); !strings.Contains(got, "Token=") {
			t.Errorf("%s: missing standard Authorization header: %q", r.URL.Path, got)
		}
		switch r.URL.Path {
		case "/Users/Me":
			_, _ = w.Write([]byte(`{"Id":"user-1","Name":"loan","ServerId":"srv"}`))
		case "/Items":
			_, _ = w.Write([]byte(`{"Items":[
				{"Id":"item-1","Name":"The Game","ProductionYear":1997,"UserData":{"Played":false},"ProviderIds":{"Tmdb":"2649"}},
				{"Id":"item-2","Name":"Moonlight","ProductionYear":2016,"UserData":{"Played":true}},
				{"Id":"item-3","Name":"Heat","ProductionYear":1995,"UserData":{"Played":false}}
			],"TotalRecordCount":3}`))
		default:
			http.NotFound(w, r)
		}
	}
}

func postWatchlist(t *testing.T, apiURL, jellyfinURL string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, apiURL+"/api/watchlist/stream?lbuser=someone", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Jellyfin-URL", jellyfinURL)
	req.Header.Set("X-Jellyfin-Token", "token-abc")
	req.Header.Set("X-Jellyfin-User-Id", "user-1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestStreamReturnsFrames(t *testing.T) {
	films := []letterboxd.Film{
		{Slug: "the-game", Title: "The Game", Year: 1997, PosterURL: "https://poster.example/the-game"},
		{Slug: "moonlight", Title: "Moonlight", Year: 2016},
		{Slug: "unmatched", Title: "Unmatched", Year: 1990},
	}
	apiURL, jfURL := newTestAPI(t, testConfig(), fakeJellyfin(t), films)

	resp := postWatchlist(t, apiURL, jfURL)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/x-ndjson") {
		t.Errorf("content-type = %q", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("cache-control = %q", cc)
	}
	if resp.Header.Get("X-Request-Id") == "" {
		t.Error("missing X-Request-Id")
	}
	if resp.Header.Get("Content-Security-Policy") == "" {
		t.Error("missing CSP header")
	}

	var frames []frame
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		var f frame
		if err := json.Unmarshal(scanner.Bytes(), &f); err != nil {
			t.Fatalf("bad frame %q: %v", scanner.Text(), err)
		}
		frames = append(frames, f)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}

	var result *frame
	sawPhase := false
	for i := range frames {
		switch frames[i].Type {
		case "phase":
			sawPhase = true
		case "result":
			result = &frames[i]
		case "error":
			t.Fatalf("unexpected error frame: %+v", frames[i])
		}
	}
	if !sawPhase {
		t.Error("missing phase frame")
	}
	if result == nil {
		t.Fatal("missing result frame")
	}

	// Moonlight is matched but watched, so it is hidden. The Game is in the
	// library and kept; Unmatched stays but is not in the library.
	if len(result.Films) != 2 {
		t.Fatalf("films = %d, want 2: %+v", len(result.Films), result.Films)
	}
	if result.Films[0].Slug != "the-game" || !result.Films[0].InLibrary || result.Films[0].ItemID != "item-1" {
		t.Errorf("first film = %+v", result.Films[0])
	}
	if result.Films[1].Slug != "unmatched" || result.Films[1].InLibrary {
		t.Errorf("second film = %+v", result.Films[1])
	}
	if result.Meta == nil || result.Meta.Total != 3 || result.Meta.Matched != 2 || result.Meta.Watched != 1 {
		t.Errorf("meta = %+v", result.Meta)
	}
}

func TestStreamRequiresCredentials(t *testing.T) {
	apiURL, _ := newTestAPI(t, testConfig(), fakeJellyfin(t), nil)

	req, _ := http.NewRequest(http.MethodPost, apiURL+"/api/watchlist/stream?lbuser=someone", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	var body errorBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "missing_credentials" {
		t.Errorf("code = %q", body.Code)
	}
}

func TestStreamRejectsInvalidUsername(t *testing.T) {
	apiURL, jfURL := newTestAPI(t, testConfig(), fakeJellyfin(t), nil)

	req, _ := http.NewRequest(http.MethodPost, apiURL+"/api/watchlist/stream?lbuser=../etc/passwd", nil)
	req.Header.Set("X-Jellyfin-URL", jfURL)
	req.Header.Set("X-Jellyfin-Token", "token-abc")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestStreamBlocksMetadataAddress(t *testing.T) {
	apiURL, _ := newTestAPI(t, testConfig(), fakeJellyfin(t), nil)

	req, _ := http.NewRequest(http.MethodPost, apiURL+"/api/watchlist/stream?lbuser=someone", nil)
	req.Header.Set("X-Jellyfin-URL", "http://169.254.169.254")
	req.Header.Set("X-Jellyfin-Token", "token-abc")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	var body errorBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "blocked_address" {
		t.Errorf("code = %q, want blocked_address", body.Code)
	}
}

func TestUnauthorizedTokenMapsTo401(t *testing.T) {
	apiURL, jfURL := newTestAPI(t, testConfig(), func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Users/Me" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.NotFound(w, r)
	}, nil)

	resp := postWatchlist(t, apiURL, jfURL)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestRateLimit(t *testing.T) {
	cfg := testConfig()
	cfg.RateLimitPerMinute = 1
	apiURL, _ := newTestAPI(t, cfg, fakeJellyfin(t), nil)

	first, err := http.Get(apiURL + "/api/poster?slug=the-game")
	if err != nil {
		t.Fatal(err)
	}
	first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first status = %d", first.StatusCode)
	}

	second, err := http.Get(apiURL + "/api/poster?slug=the-game")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Body.Close()
	if second.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second status = %d, want 429", second.StatusCode)
	}
	if second.Header.Get("Retry-After") == "" {
		t.Error("missing Retry-After")
	}
}

func TestPosterEndpoint(t *testing.T) {
	apiURL, _ := newTestAPI(t, testConfig(), fakeJellyfin(t), nil)

	resp, err := http.Get(apiURL + "/api/poster?slug=the-game")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var body struct {
		PosterURL string `json:"posterUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.PosterURL != "https://poster.example/the-game" {
		t.Errorf("posterUrl = %q", body.PosterURL)
	}
	if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "max-age") {
		t.Errorf("cache-control = %q", cc)
	}
}

func TestStaticAssets(t *testing.T) {
	apiURL, _ := newTestAPI(t, testConfig(), fakeJellyfin(t), nil)

	resp, err := http.Get(apiURL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("shell cache-control = %q", cc)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type = %q", ct)
	}

	req, _ := http.NewRequest(http.MethodGet, apiURL+"/assets/app.abc123.js", nil)
	req.Header.Set("Accept-Encoding", "br")
	asset, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer asset.Body.Close()
	if got := asset.Header.Get("Content-Encoding"); got != "br" {
		t.Errorf("content-encoding = %q", got)
	}
	if cc := asset.Header.Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("asset cache-control = %q", cc)
	}

	missing, err := http.Get(apiURL + "/assets/nope.js")
	if err != nil {
		t.Fatal(err)
	}
	missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Errorf("missing asset status = %d", missing.StatusCode)
	}
}

func TestLegacyLoginEndpointIsGone(t *testing.T) {
	apiURL, _ := newTestAPI(t, testConfig(), fakeJellyfin(t), nil)

	resp, err := http.Post(apiURL+"/api/login", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 404/405", resp.StatusCode)
	}
}

func TestHealthEndpoints(t *testing.T) {
	apiURL, _ := newTestAPI(t, testConfig(), fakeJellyfin(t), nil)

	for _, path := range []string{"/healthz", "/readyz"} {
		resp, err := http.Get(apiURL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s status = %d", path, resp.StatusCode)
		}
	}
}

func TestSeerrDiscoveryDisabled(t *testing.T) {
	apiURL, _ := newTestAPI(t, testConfig(), fakeJellyfin(t), nil)

	resp, err := http.Post(apiURL+"/api/seerr/discover", "application/json",
		strings.NewReader(`{"jellyfinUrl":"https://jelly.example.com"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestSeerrDiscoveryRejectsInvalidURL(t *testing.T) {
	cfg := testConfig()
	guard := jellyfin.NewGuard(false, false, nil)
	discoverer := seerr.New(guard, guard.HTTPClient(5*time.Second), seerr.Options{})

	apiURL, _ := newTestAPIWith(t, cfg, fakeJellyfin(t), nil, discoverer)

	resp, err := http.Post(apiURL+"/api/seerr/discover", "application/json",
		strings.NewReader(`{"jellyfinUrl":"http://insecure.example.com"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	var body errorBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "invalid_server_url" {
		t.Errorf("code = %q", body.Code)
	}
}

func TestSeerrDiscoveryReturnsEmptyForIPHost(t *testing.T) {
	cfg := testConfig()
	guard := jellyfin.NewGuard(true, true, nil)
	discoverer := seerr.New(guard, guard.HTTPClient(5*time.Second), seerr.Options{})

	apiURL, _ := newTestAPIWith(t, cfg, fakeJellyfin(t), nil, discoverer)

	resp, err := http.Post(apiURL+"/api/seerr/discover", "application/json",
		strings.NewReader(`{"jellyfinUrl":"http://127.0.0.1:8096"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Instances []seerr.Instance `json:"instances"`
		Cached    bool             `json:"cached"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Instances) != 0 {
		t.Errorf("instances = %+v, want none for an IP host", body.Instances)
	}
}

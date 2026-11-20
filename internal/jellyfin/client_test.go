package jellyfin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return NewClient(base, "tok-123", srv.Client())
}

// TestRequestsCarryStandardAuthorizationHeader is the regression test for the
// production bug: Jellyfin rejected requests that only sent
// X-Emby-Authorization. Every request must carry the standard header, and the
// legacy one as a fallback for older servers.
func TestRequestsCarryStandardAuthorizationHeader(t *testing.T) {
	var seen []*http.Request
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Clone(context.Background()))
		switch r.URL.Path {
		case "/Users/Me":
			_, _ = w.Write([]byte(`{"Id":"user-1","Name":"loan","ServerId":"srv"}`))
		case "/Items":
			_, _ = w.Write([]byte(`{"Items":[],"TotalRecordCount":0}`))
		default:
			http.NotFound(w, r)
		}
	})

	if _, err := client.Me(context.Background()); err != nil {
		t.Fatalf("Me: %v", err)
	}
	if _, err := client.Movies(context.Background(), "user-1", nil, nil); err != nil {
		t.Fatalf("Movies: %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("requests = %d, want 2", len(seen))
	}

	for _, r := range seen {
		std := r.Header.Get("Authorization")
		legacy := r.Header.Get("X-Emby-Authorization")
		if !strings.HasPrefix(std, "MediaBrowser ") {
			t.Errorf("%s: Authorization = %q", r.URL.Path, std)
		}
		if !strings.Contains(std, `Token="tok-123"`) {
			t.Errorf("%s: token missing from Authorization: %q", r.URL.Path, std)
		}
		if legacy != std {
			t.Errorf("%s: legacy header mismatch: %q vs %q", r.URL.Path, legacy, std)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("%s: Accept = %q", r.URL.Path, got)
		}
	}
}

func TestMeValidatesToken(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	if _, err := client.Me(context.Background()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}

	empty := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})
	if _, err := empty.Me(context.Background()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized for empty user", err)
	}
}

func TestMoviesUsesSlimFields(t *testing.T) {
	var query url.Values
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		_, _ = w.Write([]byte(`{"Items":[{"Id":"1","Name":"The Game","ProductionYear":1997,"ProviderIds":{"Tmdb":"2649"},"UserData":{"Played":false}}],"TotalRecordCount":1}`))
	})

	items, err := client.Movies(context.Background(), "user-1", []int{1997, 1999}, nil)
	if err != nil {
		t.Fatalf("Movies: %v", err)
	}
	if len(items) != 1 || items[0].TmdbID() != "2649" {
		t.Fatalf("items = %+v", items)
	}

	want := map[string]string{
		"recursive":        "true",
		"includeItemTypes": "Movie",
		"enableUserData":   "true",
		"enableImages":     "false",
		"userId":           "user-1",
	}
	for k, v := range want {
		if got := query.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if got := query.Get("fields"); !strings.Contains(got, "ProviderIds") || !strings.Contains(got, "UserData") {
		t.Errorf("fields = %q", got)
	}
	if years := query["Years"]; len(years) != 2 || years[0] != "1997" || years[1] != "1999" {
		t.Errorf("Years = %v", years)
	}
}

func TestMoviesPaginatesInOrder(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("startIndex") == "0" {
			var b strings.Builder
			b.WriteString(`{"Items":[`)
			for i := 0; i < 500; i++ {
				if i > 0 {
					b.WriteString(",")
				}
				b.WriteString(`{"Id":"p0-`)
				b.WriteString(strconv.Itoa(i))
				b.WriteString(`","Name":"A","ProductionYear":2000}`)
			}
			b.WriteString(`],"TotalRecordCount":501}`)
			_, _ = w.Write([]byte(b.String()))
			return
		}
		_, _ = w.Write([]byte(`{"Items":[{"Id":"last","Name":"Z","ProductionYear":2001}],"TotalRecordCount":501}`))
	})

	var progress []int
	items, err := client.Movies(context.Background(), "user-1", nil, func(index, total int, _ Item) {
		if total != 501 {
			t.Errorf("total = %d, want 501", total)
		}
		progress = append(progress, index)
	})
	if err != nil {
		t.Fatalf("Movies: %v", err)
	}
	if len(items) != 501 {
		t.Fatalf("items = %d, want 501", len(items))
	}
	if items[0].ID != "p0-0" || items[len(items)-1].ID != "last" {
		t.Errorf("page order lost: first=%q last=%q", items[0].ID, items[len(items)-1].ID)
	}
	if len(progress) != 501 || progress[0] != 0 || progress[500] != 500 {
		t.Errorf("progress was not monotonic: len=%d first=%d last=%d", len(progress), progress[0], progress[len(progress)-1])
	}
}

func TestMoviesMapsUnauthorizedAndStatusErrors(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	if _, err := client.Movies(context.Background(), "u", nil, nil); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}

	failing := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("Error processing request."))
	})
	_, err := failing.Movies(context.Background(), "u", nil, nil)
	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("err = %v, want StatusError", err)
	}
	if statusErr.Code != http.StatusBadRequest || !strings.Contains(statusErr.Body, "Error processing request") {
		t.Errorf("statusErr = %+v", statusErr)
	}
}

func TestAuthorizationHeaderShape(t *testing.T) {
	h := Authorization("")
	for _, part := range []string{"MediaBrowser", `Client="WatchlistBurner"`, `Device="Server"`, `DeviceId="watchlist-burner"`, `Version=`} {
		if !strings.Contains(h, part) {
			t.Errorf("Authorization(%q) missing %q", "", part)
		}
	}
	if strings.Contains(h, "Token=") {
		t.Errorf("unauthenticated header must not carry a token: %q", h)
	}
	if !strings.Contains(Authorization("abc"), `Token="abc"`) {
		t.Error("authenticated header must carry the token")
	}
}

func TestHTTPClientRespectsTimeout(t *testing.T) {
	g := NewGuard(true, true, nil)
	hc := g.HTTPClient(time.Second)
	if hc.Timeout != time.Second {
		t.Errorf("timeout = %v", hc.Timeout)
	}
}

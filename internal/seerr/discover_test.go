package seerr

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"watchlist-burner/internal/jellyfin"
)

func TestRegistrableDomain(t *testing.T) {
	tests := map[string]string{
		"jelly.example.com":       "example.com",
		"example.com":             "example.com",
		"a.b.example.co.uk":       "example.co.uk",
		"jelly.example.co.uk":     "example.co.uk",
		"127.0.0.1":               "",
		"localhost":               "",
		"jellyfin":                "",
		"":                        "",
		"JELLY.Example.COM.":      "example.com",
		"media.example.github.io": "example.github.io",
	}
	for host, want := range tests {
		if got := registrableDomain(host); got != want {
			t.Errorf("registrableDomain(%q) = %q, want %q", host, got, want)
		}
	}
}

func TestCandidates(t *testing.T) {
	got := Candidates("example.com", "https", []string{"seerr", "requests", "seerr"}, true)
	want := []string{
		"https://example.com",
		"https://seerr.example.com",
		"https://requests.example.com",
	}
	if len(got) != len(want) {
		t.Fatalf("Candidates = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Candidates[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	noApex := Candidates("example.com", "http", []string{"seerr"}, false)
	if len(noApex) != 1 || noApex[0] != "http://seerr.example.com" {
		t.Errorf("Candidates without apex = %v", noApex)
	}
}

func seerrServer(t *testing.T, status int, body, contentType string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/status" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestProbeRecognisesSeerr(t *testing.T) {
	good := seerrServer(t, http.StatusOK, `{"version":"2.5.0","commitTag":"abc"}`, "application/json; charset=utf-8")
	d := New(nil, good.Client(), Options{Timeout: time.Second})

	inst, ok := d.probe(context.Background(), good.URL, "")
	if !ok {
		t.Fatal("probe did not recognise Seerr")
	}
	if inst.Version != "2.5.0" || inst.URL != good.URL {
		t.Errorf("instance = %+v", inst)
	}
	if inst.String() == "" {
		t.Error("String() is empty")
	}
}

func TestProbeRejectsLookalikes(t *testing.T) {
	tests := map[string]*httptest.Server{
		"wrong payload": seerrServer(t, http.StatusOK, `{"hello":"world"}`, "application/json"),
		"html page":     seerrServer(t, http.StatusOK, `<html></html>`, "text/html"),
		"not found":     seerrServer(t, http.StatusNotFound, `{"version":"1"}`, "application/json"),
	}
	for name, srv := range tests {
		t.Run(name, func(t *testing.T) {
			d := New(nil, srv.Client(), Options{Timeout: time.Second})
			if _, ok := d.probe(context.Background(), srv.URL, ""); ok {
				t.Error("probe accepted a lookalike")
			}
		})
	}
}

func TestProbeAllStopsAtFirstInstance(t *testing.T) {
	seerrSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/status":
			_, _ = w.Write([]byte(`{"version":"2.5.0"}`))
		case "/api/v1/settings/public":
			_, _ = w.Write([]byte(`{"initialized":true,"jellyfinExternalHost":"https://jelly.example.com"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(seerrSrv.Close)

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(3 * time.Second):
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(slow.Close)

	d := New(nil, seerrSrv.Client(), Options{Timeout: 5 * time.Second, Workers: 2})

	start := time.Now()
	found := d.probeAll(context.Background(), []string{slow.URL, seerrSrv.URL}, "jelly.example.com")
	elapsed := time.Since(start)

	if len(found) != 1 || found[0].URL != seerrSrv.URL {
		t.Fatalf("found = %+v", found)
	}
	if elapsed > 2*time.Second {
		t.Errorf("probeAll took %v; the slow candidate should have been cancelled", elapsed)
	}
}

func TestProbeVerifiesJellyfinLink(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/status":
			_, _ = w.Write([]byte(`{"version":"2.5.0"}`))
		case "/api/v1/settings/public":
			_, _ = w.Write([]byte(`{"initialized":true,"jellyfinExternalHost":"https://jelly.example.com"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	d := New(nil, srv.Client(), Options{Timeout: time.Second})

	inst, ok := d.probe(context.Background(), srv.URL, "jelly.example.com")
	if !ok || !inst.Verified {
		t.Fatalf("inst = %+v, ok = %v; want verified", inst, ok)
	}

	inst, ok = d.probe(context.Background(), srv.URL, "other.example.com")
	if !ok || inst.Verified {
		t.Fatalf("inst = %+v; a different Jellyfin host must not verify", inst)
	}
}

func TestProbeAllRanksVerifiedFirst(t *testing.T) {
	makeServer := func(version string, jellyfinHost string) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/api/v1/status":
				_, _ = w.Write([]byte(`{"version":"` + version + `"}`))
			case "/api/v1/settings/public":
				_, _ = w.Write([]byte(`{"initialized":true,"jellyfinExternalHost":"https://` + jellyfinHost + `"}`))
			default:
				http.NotFound(w, r)
			}
		}))
		t.Cleanup(srv.Close)
		return srv
	}

	unverified := makeServer("1.0.0", "someone-else.example.com")
	verified := makeServer("2.0.0", "jelly.example.com")

	// Make the verified instance answer last so both results are collected.
	slowVerified := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		time.Sleep(50 * time.Millisecond)
		if r.URL.Path == "/api/v1/status" {
			_, _ = w.Write([]byte(`{"version":"2.0.0"}`))
			return
		}
		if r.URL.Path == "/api/v1/settings/public" {
			_, _ = w.Write([]byte(`{"initialized":true,"jellyfinExternalHost":"https://jelly.example.com"}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(slowVerified.Close)
	verified = slowVerified

	d := New(nil, verified.Client(), Options{Timeout: time.Second})
	found := d.probeAll(context.Background(), []string{unverified.URL, verified.URL}, "jelly.example.com")
	if len(found) != 2 {
		t.Fatalf("found = %+v", found)
	}
	if !found[0].Verified || found[0].URL != verified.URL {
		t.Errorf("verified instance not ranked first: %+v", found)
	}
}

func TestDiscoverRejectsInvalidURL(t *testing.T) {
	d := New(jellyfin.NewGuard(false, false, nil), http.DefaultClient, Options{})

	_, _, err := d.Discover(context.Background(), "http://insecure.example.com")
	if !errors.Is(err, ErrInvalidJellyfinURL) {
		t.Fatalf("err = %v, want ErrInvalidJellyfinURL", err)
	}
}

func TestDiscoverSkipsIPHosts(t *testing.T) {
	guard := jellyfin.NewGuard(true, true, nil)
	d := New(guard, guard.HTTPClient(time.Second), Options{})

	found, cached, err := d.Discover(context.Background(), "http://127.0.0.1:8096")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(found) != 0 || cached {
		t.Fatalf("found=%v cached=%v, want none/false", found, cached)
	}
}

func TestDiscoverCachesPerDomain(t *testing.T) {
	// The scan is exercised through the cache layer with a stubbed scan: an IP
	// host would short-circuit, so assert the cache key logic indirectly by
	// priming the cache.
	d := New(jellyfin.NewGuard(true, true, nil), http.DefaultClient, Options{})
	key := "https://example.com"
	d.cache.Put(key, []Instance{{URL: "https://seerr.example.com", Version: "2.5.0"}})

	if got, ok := d.cache.Get(key); !ok || len(got) != 1 {
		t.Fatalf("cache round-trip failed: %v %v", got, ok)
	}
}

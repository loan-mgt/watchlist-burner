// Package seerr discovers a Seerr (formerly Jellyseerr/Overseerr) instance by
// probing the conventional host names under the Jellyfin server's registrable
// domain.
//
// Seerr sends no CORS headers, so a browser cannot probe it directly; the scan
// therefore runs server-side through the same SSRF guard as Jellyfin traffic.
package seerr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/publicsuffix"

	"watchlist-burner/internal/cache"
	"watchlist-burner/internal/jellyfin"
)

// ErrInvalidJellyfinURL marks a malformed or disallowed server URL.
var ErrInvalidJellyfinURL = errors.New("seerr: invalid jellyfin url")

// Instance is a confirmed Seerr server.
type Instance struct {
	URL     string `json:"url"`
	Version string `json:"version,omitempty"`
	// Verified is true when the instance's public settings point at the same
	// Jellyfin host the user is connected to, which rules out a Seerr belonging
	// to someone else on a shared domain.
	Verified bool `json:"verified,omitempty"`
}

// Tier1 is the default prefix set: names that almost always mean Seerr.
var Tier1 = []string{
	"seerr",
	"jellyseerr",
	"overseerr",
	"requests",
	"request",
	"ask",
	"discover",
}

// Tier2 is the long tail, only probed when deep discovery is enabled. Several
// of these are common on unrelated sites, which is why the response signature
// is checked strictly.
var Tier2 = []string{
	"req",
	"jellyrequest",
	"jellyrequests",
	"jelly-request",
	"jelly-requests",
	"jellyfin-requests",
	"media-request",
	"media-requests",
	"mediarequest",
	"mediarequests",
	"wish",
	"wishlist",
	"want",
	"get",
	"add",
	"watch",
	"movies",
	"shows",
	"tv",
	"plex-requests",
	"petitions",
	"portal",
}

// Options configures a Discoverer.
type Options struct {
	Client   *http.Client
	Prefixes []string // overrides the tier-1 set when non-empty
	Deep     bool
	Timeout  time.Duration
	Workers  int
	CacheTTL time.Duration
}

// Discoverer probes candidate hosts and caches the result per registrable
// domain.
type Discoverer struct {
	guard   *jellyfin.Guard
	client  *http.Client
	tier1   []string
	tier2   []string
	deep    bool
	timeout time.Duration
	workers int
	cache   *cache.LRU[string, []Instance]
	group   *cache.Group[string, []Instance]
}

// New builds a Discoverer. client must be the guard's client so candidates stay
// inside the SSRF policy.
func New(guard *jellyfin.Guard, client *http.Client, opts Options) *Discoverer {
	if opts.Client != nil {
		client = opts.Client
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 4 * time.Second
	}
	if opts.Workers <= 0 {
		opts.Workers = 4
	}
	if opts.CacheTTL <= 0 {
		opts.CacheTTL = 30 * time.Minute
	}
	tier1 := Tier1
	if len(opts.Prefixes) > 0 {
		tier1 = opts.Prefixes
	}
	tier2 := Tier2
	if len(opts.Prefixes) > 0 {
		tier2 = nil
	}
	return &Discoverer{
		guard:   guard,
		client:  client,
		tier1:   tier1,
		tier2:   tier2,
		deep:    opts.Deep,
		timeout: opts.Timeout,
		workers: opts.Workers,
		cache:   cache.NewLRU[string, []Instance](512, opts.CacheTTL),
		group:   &cache.Group[string, []Instance]{},
	}
}

// Discover validates the Jellyfin URL, derives candidate Seerr hosts from its
// registrable domain and probes them. The bool reports a cache hit.
func (d *Discoverer) Discover(ctx context.Context, rawJellyfinURL string) ([]Instance, bool, error) {
	u, err := d.guard.ValidateURL(rawJellyfinURL)
	if err != nil {
		return nil, false, fmt.Errorf("%w: %v", ErrInvalidJellyfinURL, err)
	}
	base := registrableDomain(u.Hostname())
	if base == "" {
		return nil, false, nil // IP or localhost: nothing sensible to probe
	}

	key := u.Scheme + "://" + base
	if cached, ok := d.cache.Get(key); ok {
		return cached, true, nil
	}

	instances, err := d.group.Do(ctx, key, func(ctx context.Context) ([]Instance, error) {
		if cached, ok := d.cache.Get(key); ok {
			return cached, nil
		}
		found := d.scan(ctx, u, base)
		d.cache.Put(key, found)
		return found, nil
	})
	if err != nil {
		return nil, false, err
	}
	return instances, false, nil
}

// scan probes tier 1 first and stops as soon as one instance answers. Tier 2 is
// only reached when deep discovery is enabled and tier 1 found nothing.
func (d *Discoverer) scan(ctx context.Context, u *url.URL, base string) []Instance {
	scheme := "https"
	if u.Scheme == "http" {
		scheme = "http"
	}

	first := Candidates(base, scheme, d.tier1, true)
	if found := d.probeAll(ctx, first, u.Hostname()); len(found) > 0 {
		return found
	}
	if !d.deep || len(d.tier2) == 0 {
		return nil
	}
	return d.probeAll(ctx, Candidates(base, scheme, d.tier2, false), u.Hostname())
}

// Candidates lists base URLs to try. The apex is included because Seerr is
// sometimes served at the domain root.
func Candidates(base, scheme string, prefixes []string, includeApex bool) []string {
	seen := make(map[string]struct{}, len(prefixes)+1)
	out := make([]string, 0, len(prefixes)+1)
	add := func(host string) {
		if host == "" {
			return
		}
		u := scheme + "://" + host
		if _, ok := seen[u]; ok {
			return
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	if includeApex {
		add(base)
	}
	for _, p := range prefixes {
		p = strings.TrimSpace(strings.ToLower(p))
		if p == "" {
			continue
		}
		add(p + "." + base)
	}
	return out
}

// registrableDomain returns the eTLD+1 of a host, or "" for IPs and
// single-label names.
func registrableDomain(host string) string {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" || host == "localhost" || !strings.Contains(host, ".") {
		return ""
	}
	base, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil || base == "" {
		return ""
	}
	return base
}

// probeAll runs probes with a bounded worker pool and cancels the rest as soon
// as one instance is confirmed.
func (d *Discoverer) probeAll(ctx context.Context, urls []string, jellyfinHost string) []Instance {
	if len(urls) == 0 {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make(chan Instance, len(urls))
	sem := make(chan struct{}, d.workers)

	var wg sync.WaitGroup
	for _, candidate := range urls {
		wg.Add(1)
		go func(candidate string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			if inst, ok := d.probe(ctx, candidate, jellyfinHost); ok {
				select {
				case results <- inst:
					// A verified instance is conclusive: stop probing. An
					// unverified one may still be beaten by a verified
					// sibling, so keep looking.
					if inst.Verified {
						cancel()
					}
				case <-ctx.Done():
				}
			}
		}(candidate)
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	var found []Instance
	for inst := range results {
		found = append(found, inst)
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].Verified != found[j].Verified {
			return found[i].Verified
		}
		return found[i].URL < found[j].URL
	})
	return found
}

// probe confirms a host is Seerr by reading its public /api/v1/status, which is
// unauthenticated and identifies the service by its version payload.
func (d *Discoverer) probe(ctx context.Context, baseURL, jellyfinHost string) (Instance, bool) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/v1/status", nil)
	if err != nil {
		return Instance{}, false
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "WatchlistBurner")

	resp, err := d.client.Do(req)
	if err != nil {
		return Instance{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Instance{}, false
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "json") {
		return Instance{}, false
	}

	var body struct {
		Version   string `json:"version"`
		CommitTag string `json:"commitTag"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body); err != nil {
		return Instance{}, false
	}
	if body.Version == "" && body.CommitTag == "" {
		return Instance{}, false
	}

	inst := Instance{URL: baseURL, Version: body.Version}
	inst.Verified = d.linkedTo(ctx, baseURL, jellyfinHost)
	return inst, true
}

// linkedTo reads the instance's public settings and checks it is paired with
// the same Jellyfin server. Missing settings (older builds) simply mean
// "unverified".
func (d *Discoverer) linkedTo(ctx context.Context, baseURL, jellyfinHost string) bool {
	if jellyfinHost == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/v1/settings/public", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "WatchlistBurner")

	resp, err := d.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}

	var body struct {
		JellyfinExternalHost string `json:"jellyfinExternalHost"`
		Initialized          bool   `json:"initialized"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body); err != nil {
		return false
	}
	if !body.Initialized || body.JellyfinExternalHost == "" {
		return false
	}
	return sameHost(body.JellyfinExternalHost, jellyfinHost)
}

func sameHost(rawURL, host string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return false
	}
	return strings.EqualFold(strings.TrimSuffix(u.Hostname(), "."), strings.TrimSuffix(host, "."))
}

// String makes log lines readable.
func (i Instance) String() string {
	if i.Version == "" {
		return i.URL
	}
	return fmt.Sprintf("%s (%s)", i.URL, i.Version)
}

package jellyfin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// ErrUnauthorized means the token is missing, expired or revoked.
var ErrUnauthorized = errors.New("jellyfin: unauthorized")

// StatusError reports an unexpected Jellyfin response, keeping a bounded body
// snippet so operators can diagnose the upstream without leaking it to users.
type StatusError struct {
	Op   string
	Code int
	Body string
}

func (e *StatusError) Error() string {
	if e.Body != "" {
		return fmt.Sprintf("%s: unexpected status %d: %s", e.Op, e.Code, e.Body)
	}
	return fmt.Sprintf("%s: unexpected status %d", e.Op, e.Code)
}

// Credentials identify one Jellyfin server and the token the browser obtained
// for it. The password never reaches this package.
type Credentials struct {
	BaseURL string
	Token   string
	UserID  string
}

// User is the subset of /Users/Me the app relies on.
type User struct {
	ID       string `json:"Id"`
	Name     string `json:"Name"`
	ServerID string `json:"ServerId"`
}

// UserData carries per-user playback state from the Items API.
type UserData struct {
	Played bool `json:"Played"`
}

// Item is a subset of Jellyfin's BaseItemDto.
type Item struct {
	ID             string            `json:"Id"`
	Name           string            `json:"Name"`
	OriginalTitle  string            `json:"OriginalTitle"`
	ProductionYear *int              `json:"ProductionYear"`
	UserData       *UserData         `json:"UserData"`
	ProviderIds    map[string]string `json:"ProviderIds"`
}

// TmdbID returns the TMDB provider id, or "" when Jellyfin has none.
func (it Item) TmdbID() string {
	for k, v := range it.ProviderIds {
		if strings.EqualFold(k, "Tmdb") {
			return v
		}
	}
	return ""
}

type itemsResponse struct {
	Items            []Item `json:"Items"`
	TotalRecordCount int    `json:"TotalRecordCount"`
}

// Client talks to a single Jellyfin server with a single token.
type Client struct {
	base  *url.URL
	token string
	http  *http.Client
}

// NewClient builds a client for a validated base URL.
func NewClient(base *url.URL, token string, hc *http.Client) *Client {
	return &Client{base: base, token: token, http: hc}
}

// Me validates the token and returns the authenticated user.
func (c *Client) Me(ctx context.Context) (*User, error) {
	resp, err := c.get(ctx, "/Users/Me", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, ErrUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return nil, statusError("jellyfin me", resp)
	}
	var u User
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&u); err != nil {
		return nil, fmt.Errorf("jellyfin me: decode: %w", err)
	}
	if u.ID == "" {
		return nil, ErrUnauthorized
	}
	return &u, nil
}

// Movies returns every movie matching years (empty means no year filter),
// walking pagination with bounded parallelism. onItem, when non-nil, is called
// from a single goroutine in library order with a monotonically increasing
// index and the total record count.
func (c *Client) Movies(ctx context.Context, userID string, years []int, onItem func(index, total int, it Item)) ([]Item, error) {
	const (
		limit         = 500
		pagesInFlight = 4
		maxPages      = 60
	)

	// A huge watchlist would produce a URL longer than most proxies accept.
	// Falling back to an unfiltered fetch is correct, just slower.
	if len(years) > 200 {
		years = nil
	}

	first, err := c.itemsPage(ctx, userID, 0, limit, years)
	if err != nil {
		return nil, err
	}
	total := first.TotalRecordCount
	if total == 0 {
		total = len(first.Items)
	}

	all := first.Items
	if onItem != nil {
		for i, it := range first.Items {
			onItem(i, total, it)
		}
	}

	pageCount := (total + limit - 1) / limit
	if pageCount <= 1 {
		return all, nil
	}
	if pageCount > maxPages {
		return nil, fmt.Errorf("jellyfin: library too large (%d items)", total)
	}

	// Fetch the remaining pages concurrently, then emit them in order so the
	// caller never observes out-of-order progress.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type page struct {
		index int
		items []Item
		err   error
	}
	ch := make(chan page, pageCount)

	var wg sync.WaitGroup
	sem := make(chan struct{}, pagesInFlight)
	for p := 1; p < pageCount; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			res, err := c.itemsPage(ctx, userID, p*limit, limit, years)
			if err != nil {
				cancel()
				ch <- page{index: p, err: err}
				return
			}
			ch <- page{index: p, items: res.Items}
		}(p)
	}
	go func() { wg.Wait(); close(ch) }()

	pages := make([][]Item, pageCount)
	for res := range ch {
		if res.err != nil {
			return nil, res.err
		}
		pages[res.index] = res.items
	}

	streamed := len(first.Items)
	for p := 1; p < pageCount; p++ {
		for _, it := range pages[p] {
			all = append(all, it)
			if onItem != nil {
				onItem(streamed, total, it)
				streamed++
			}
		}
	}
	return all, nil
}

func (c *Client) itemsPage(ctx context.Context, userID string, startIndex, limit int, years []int) (*itemsResponse, error) {
	q := url.Values{}
	q.Set("recursive", "true")
	q.Set("includeItemTypes", "Movie")
	q.Set("enableUserData", "true")
	q.Set("enableImages", "false")
	q.Set("fields", "UserData,ProductionYear,ProviderIds,OriginalTitle")
	q.Set("userId", userID)
	q.Set("limit", strconv.Itoa(limit))
	q.Set("startIndex", strconv.Itoa(startIndex))
	for _, y := range years {
		q.Add("Years", strconv.Itoa(y))
	}

	resp, err := c.get(ctx, "/Items", q)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, ErrUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return nil, statusError("jellyfin items", resp)
	}

	var page itemsResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&page); err != nil {
		return nil, fmt.Errorf("jellyfin items: decode: %w", err)
	}
	return &page, nil
}

func (c *Client) get(ctx context.Context, path string, q url.Values) (*http.Response, error) {
	u := *c.base
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	setAuth(req, c.token)
	return c.http.Do(req)
}

// statusError reads a bounded prefix of the response body for diagnostics.
func statusError(op string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	return &StatusError{Op: op, Code: resp.StatusCode, Body: strings.TrimSpace(string(body))}
}

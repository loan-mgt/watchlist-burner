package jellyfin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// UserData carries per-user playback state from the Items API.
type UserData struct {
	Played bool `json:"Played"`
}

// Item is a subset of Jellyfin's BaseItemDto.
type Item struct {
	ID             string    `json:"Id"`
	Name           string    `json:"Name"`
	ProductionYear *int      `json:"ProductionYear"`
	UserData       *UserData `json:"UserData"`
}

type itemsResponse struct {
	Items            []Item `json:"Items"`
	TotalRecordCount int    `json:"TotalRecordCount"`
}

// Client talks to a single Jellyfin server.
type Client struct {
	baseURL string
	http    *http.Client
}

func NewClient(baseURL string) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &http.Client{}}
}

// Session is an authenticated, ephemeral Jellyfin session.
type Session struct {
	AccessToken string
	UserID      string
	Username    string
	JellyfinURL string
}

// Authenticate logs in by username and password and returns a session.
func (c *Client) Authenticate(ctx context.Context, username, password string) (*Session, error) {
	body, _ := json.Marshal(map[string]string{
		"Username": username,
		"Pw":       password,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/Users/AuthenticateByName", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Emby-Authorization", "MediaBrowser Client=\"WatchlistBurner\", Device=\"server\", DeviceId=\"watchlist-burner\", Version=\"0.1.0\"")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jellyfin auth: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, ErrBadCredentials
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jellyfin auth: unexpected status %d", resp.StatusCode)
	}

	var result struct {
		AccessToken string `json:"AccessToken"`
		User        struct {
			ID string `json:"Id"`
		} `json:"User"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("jellyfin auth: decode response: %w", err)
	}
	if result.AccessToken == "" {
		return nil, ErrBadCredentials
	}

	return &Session{AccessToken: result.AccessToken, UserID: result.User.ID, Username: username, JellyfinURL: c.baseURL}, nil
}

// Movies returns every movie in the user's library, walking pagination.
// UserData is included so watched state is known. onItem, if non-nil, is
// called with each movie as its page is received.
func (c *Client) Movies(ctx context.Context, s *Session, onItem func(Item)) ([]Item, error) {
	var all []Item
	startIndex := 0
	const limit = 200

	for {
		q := url.Values{}
		q.Set("recursive", "true")
		q.Set("includeItemTypes", "Movie")
		q.Set("fields", "ProviderIds")
		q.Set("enableUserData", "true")
		q.Set("userId", s.UserID)
		q.Set("limit", strconv.Itoa(limit))
		q.Set("startIndex", strconv.Itoa(startIndex))

		u := c.baseURL + "/Items?" + q.Encode()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("X-Emby-Token", s.AccessToken)
		req.Header.Set("Accept", "application/json")

		resp, err := c.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("jellyfin items: %w", err)
		}
		if resp.StatusCode == http.StatusUnauthorized {
			resp.Body.Close()
			return nil, ErrSessionExpired
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("jellyfin items: unexpected status %d", resp.StatusCode)
		}

		var page itemsResponse
		decodeErr := json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if decodeErr != nil {
			return nil, fmt.Errorf("jellyfin items: decode: %w", decodeErr)
		}

		if onItem != nil {
			for _, it := range page.Items {
				onItem(it)
			}
		}
		all = append(all, page.Items...)
		if len(page.Items) < limit || len(all) >= page.TotalRecordCount {
			break
		}
		startIndex += len(page.Items)
	}

	return all, nil
}

var (
	ErrBadCredentials = fmt.Errorf("jellyfin: invalid username or password")
	ErrSessionExpired = fmt.Errorf("jellyfin: session expired")
)

// SessionStore holds one authenticated session in memory.
type SessionStore struct {
	mu      sync.RWMutex
	session *Session
}

func NewSessionStore() *SessionStore {
	return &SessionStore{}
}

func (s *SessionStore) Set(sess *Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.session = sess
}

func (s *SessionStore) Get() *Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.session
}

func (s *SessionStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.session = nil
}

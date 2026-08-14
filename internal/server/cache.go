package server

import (
	"sync"
	"time"
)

// watchlistCache holds the last watchlist result, keyed by Letterboxd
// username and expired on a TTL. It is safe for concurrent use; reads return
// a reference to the current slices, and put replaces them wholesale, so a
// reader can keep using a snapshot after a concurrent put.
type watchlistCache struct {
	mu    sync.Mutex
	ttl   time.Duration
	films []WatchlistFilm
	meta  *watchlistResponse
	at    time.Time
	key   string
}

func (c *watchlistCache) get(key string) ([]WatchlistFilm, *watchlistResponse, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.films != nil && c.key == key && time.Since(c.at) < c.ttl {
		return c.films, c.meta, true
	}
	return nil, nil, false
}

func (c *watchlistCache) put(key string, films []WatchlistFilm, meta *watchlistResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.films = films
	c.meta = meta
	c.at = time.Now()
	c.key = key
}

func (c *watchlistCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.films = nil
	c.meta = nil
	c.at = time.Time{}
	c.key = ""
}

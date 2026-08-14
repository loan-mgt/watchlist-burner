package server

import (
	"sync"
	"time"
)

// watchlistCache holds the last watchlist result, keyed by Letterboxd
// username and expired on a TTL. It is safe for concurrent use; reads return
// a reference to the current response, and put replaces it wholesale, so a
// reader can keep using a snapshot after a concurrent put.
type watchlistCache struct {
	mu   sync.Mutex
	ttl  time.Duration
	resp *watchlistResponse
	at   time.Time
	key  string
}

func (c *watchlistCache) get(key string) (*watchlistResponse, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.resp != nil && c.key == key && time.Since(c.at) < c.ttl {
		return c.resp, true
	}
	return nil, false
}

func (c *watchlistCache) put(key string, resp *watchlistResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resp = resp
	c.at = time.Now()
	c.key = key
}

func (c *watchlistCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resp = nil
	c.at = time.Time{}
	c.key = ""
}

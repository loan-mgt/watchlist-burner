package server

import "time"

// watchlistCache holds the last watchlist result, keyed by Letterboxd
// username and expired on a TTL. It is not safe for concurrent use; callers
// serialize access behind the Server's refresh semaphore.
type watchlistCache struct {
	ttl   time.Duration
	films []WatchlistFilm
	meta  *watchlistResponse
	at    time.Time
	key   string
}

func (c *watchlistCache) get(key string) ([]WatchlistFilm, *watchlistResponse, bool) {
	if c.films != nil && c.key == key && time.Since(c.at) < c.ttl {
		return c.films, c.meta, true
	}
	return nil, nil, false
}

func (c *watchlistCache) put(key string, films []WatchlistFilm, meta *watchlistResponse) {
	c.films = films
	c.meta = meta
	c.at = time.Now()
	c.key = key
}

func (c *watchlistCache) invalidate() {
	c.films = nil
	c.meta = nil
	c.at = time.Time{}
	c.key = ""
}

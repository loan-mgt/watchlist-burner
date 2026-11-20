// Package cache provides the small bounded caches the app needs: a TTL-aware
// LRU and a context-aware single-flight group. Both are generic and dependency
// free, which keeps the runtime surface tiny.
package cache

import (
	"container/list"
	"sync"
	"time"
)

type entry[K comparable, V any] struct {
	key     K
	value   V
	expires time.Time
}

// LRU is a size-bounded, TTL-expiring cache safe for concurrent use.
type LRU[K comparable, V any] struct {
	mu    sync.Mutex
	max   int
	ttl   time.Duration
	now   func() time.Time
	order *list.List
	items map[K]*list.Element
}

// NewLRU builds a cache holding at most max entries for at most ttl. ttl <= 0
// means entries never expire.
func NewLRU[K comparable, V any](max int, ttl time.Duration) *LRU[K, V] {
	if max <= 0 {
		max = 1
	}
	return &LRU[K, V]{
		max:   max,
		ttl:   ttl,
		now:   time.Now,
		order: list.New(),
		items: make(map[K]*list.Element, max),
	}
}

// SetClock replaces the time source. Test-only.
func (c *LRU[K, V]) SetClock(now func() time.Time) { c.now = now }

// Get returns a live entry, refreshing its recency.
func (c *LRU[K, V]) Get(key K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.items[key]
	if !ok {
		var zero V
		return zero, false
	}
	e := el.Value.(*entry[K, V])
	if c.ttl > 0 && c.now().After(e.expires) {
		c.order.Remove(el)
		delete(c.items, key)
		var zero V
		return zero, false
	}
	c.order.MoveToFront(el)
	return e.value, true
}

// Put stores a value, evicting the least recently used entry when full.
func (c *LRU[K, V]) Put(key K, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()

	expires := time.Time{}
	if c.ttl > 0 {
		expires = c.now().Add(c.ttl)
	}
	if el, ok := c.items[key]; ok {
		e := el.Value.(*entry[K, V])
		e.value = value
		e.expires = expires
		c.order.MoveToFront(el)
		return
	}
	el := c.order.PushFront(&entry[K, V]{key: key, value: value, expires: expires})
	c.items[key] = el
	for c.order.Len() > c.max {
		last := c.order.Back()
		if last == nil {
			break
		}
		c.order.Remove(last)
		delete(c.items, last.Value.(*entry[K, V]).key)
	}
}

// Delete removes a key.
func (c *LRU[K, V]) Delete(key K) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.order.Remove(el)
		delete(c.items, key)
	}
}

// Len reports the current number of entries.
func (c *LRU[K, V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

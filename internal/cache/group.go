package cache

import (
	"context"
	"sync"
)

// Group collapses concurrent calls for the same key into one execution. It is a
// minimal, dependency-free stand-in for singleflight: the first caller runs fn,
// everyone else waits for the same result. Leader cancellation is propagated to
// joiners, which is acceptable because joiners share the request's lifetime.
type Group[K comparable, V any] struct {
	mu    sync.Mutex
	calls map[K]*call[V]
}

type call[V any] struct {
	done  chan struct{}
	value V
	err   error
}

// Do runs fn for key, or waits for the in-flight call for that key.
func (g *Group[K, V]) Do(ctx context.Context, key K, fn func(context.Context) (V, error)) (V, error) {
	g.mu.Lock()
	if g.calls == nil {
		g.calls = make(map[K]*call[V])
	}
	if c, ok := g.calls[key]; ok {
		g.mu.Unlock()
		select {
		case <-c.done:
			return c.value, c.err
		case <-ctx.Done():
			var zero V
			return zero, ctx.Err()
		}
	}
	c := &call[V]{done: make(chan struct{})}
	g.calls[key] = c
	g.mu.Unlock()

	c.value, c.err = fn(ctx)
	close(c.done)

	g.mu.Lock()
	delete(g.calls, key)
	g.mu.Unlock()

	return c.value, c.err
}

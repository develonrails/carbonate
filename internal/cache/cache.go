// Package cache keeps decrypted data in memory until the source says it has
// changed.
//
// A DAV client polls: it asks for the whole collection every time it syncs.
// Fetching and decrypting thousands of events on each poll would make the
// bridge unusable, and a timer would either serve stale data or throw away
// good data on a schedule unrelated to anything. Proton reports a token that
// moves when a calendar moves, so that is what decides.
package cache

import (
	"context"
	"sync"
)

// Entries caches items by key, keyed also on a token describing their version.
type Entries[T any] struct {
	mu      sync.Mutex
	entries map[string]*entry[T]
}

type entry[T any] struct {
	items []T
	token string
	valid bool

	// inflight serialises fetches for one key, so a burst of requests from a
	// polling client becomes a single call rather than several.
	inflight sync.Mutex
}

// New returns an empty cache.
func New[T any]() *Entries[T] {
	return &Entries[T]{entries: make(map[string]*entry[T])}
}

// Get returns the cached items for key, calling fetch when the token differs
// from the one they were fetched with.
//
// An empty token means "cannot tell", and always refetches: serving data that
// might be stale is worse than fetching data that might be current.
func (c *Entries[T]) Get(ctx context.Context, key, token string, fetch func(context.Context) ([]T, error)) ([]T, error) {
	c.mu.Lock()
	e, ok := c.entries[key]
	if !ok {
		e = &entry[T]{}
		c.entries[key] = e
	}
	c.mu.Unlock()

	e.inflight.Lock()
	defer e.inflight.Unlock()

	c.mu.Lock()
	fresh := e.valid && token != "" && e.token == token
	items := e.items
	c.mu.Unlock()

	if fresh {
		return items, nil
	}

	items, err := fetch(ctx)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	e.items, e.token, e.valid = items, token, true
	c.mu.Unlock()

	return items, nil
}

// Invalidate drops the entry for key.
//
// Proton records a change in its event loop a moment after accepting it, so
// straight after a write the token still reads as it did before. Our own
// write is the one case where we know better than the token.
func (c *Entries[T]) Invalidate(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if e, ok := c.entries[key]; ok {
		e.valid = false
	}
}

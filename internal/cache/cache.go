// Package cache keeps decrypted calendar events in memory.
//
// A CalDAV client polls: Evolution asks for the whole calendar every time it
// syncs. Fetching and decrypting thousands of events on each poll would make
// the bridge unusable, so results are held for a short while and dropped as
// soon as we change something ourselves.
package cache

import (
	"context"
	"sync"
	"time"
)

// Events caches per-calendar event lists.
type Events[T any] struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	entries map[string]*entry[T]
}

type entry[T any] struct {
	items    []T
	fetched  time.Time
	inflight sync.Mutex
}

// New returns a cache holding entries for ttl.
func New[T any](ttl time.Duration) *Events[T] {
	return &Events[T]{
		ttl:     ttl,
		now:     time.Now,
		entries: make(map[string]*entry[T]),
	}
}

// Get returns the cached items for key, calling fetch when they are missing or
// stale.
func (c *Events[T]) Get(ctx context.Context, key string, fetch func(context.Context) ([]T, error)) ([]T, error) {
	c.mu.Lock()
	e, ok := c.entries[key]
	if !ok {
		e = &entry[T]{}
		c.entries[key] = e
	}
	c.mu.Unlock()

	// Hold the per-key lock across the fetch so a burst of concurrent
	// requests results in one call to Proton rather than several.
	e.inflight.Lock()
	defer e.inflight.Unlock()

	c.mu.Lock()
	fresh := !e.fetched.IsZero() && c.now().Sub(e.fetched) < c.ttl
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
	e.items, e.fetched = items, c.now()
	c.mu.Unlock()

	return items, nil
}

// Invalidate drops the entry for key, so the next Get refetches. Call it after
// any write: our own change is the one case where we know the cache is wrong.
func (c *Events[T]) Invalidate(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if e, ok := c.entries[key]; ok {
		e.fetched = time.Time{}
	}
}

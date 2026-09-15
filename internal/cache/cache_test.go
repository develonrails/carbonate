package cache_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/develonrails/carbonate/internal/cache"
)

func counting(calls *int) func(context.Context) ([]int, error) {
	return func(context.Context) ([]int, error) {
		*calls++

		return []int{*calls}, nil
	}
}

// The same token means nothing has changed, so nothing needs fetching again.
func TestUnchangedTokenServesTheCache(t *testing.T) {
	c := cache.New[int]()

	calls := 0
	for range 3 {
		if _, err := c.Get(context.Background(), "key", "token-1", counting(&calls)); err != nil {
			t.Fatalf("Get: %v", err)
		}
	}

	if calls != 1 {
		t.Errorf("fetched %d times, want 1", calls)
	}
}

// A different token means the source moved, so the cache is worthless.
func TestChangedTokenRefetches(t *testing.T) {
	c := cache.New[int]()

	calls := 0

	if _, err := c.Get(context.Background(), "key", "token-1", counting(&calls)); err != nil {
		t.Fatalf("Get: %v", err)
	}

	got, err := c.Get(context.Background(), "key", "token-2", counting(&calls))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if calls != 2 {
		t.Errorf("fetched %d times, want 2", calls)
	}

	if got[0] != 2 {
		t.Errorf("served the old items after the token changed: %v", got)
	}
}

// An empty token means the source could not say. Serving data that might be
// stale is worse than fetching data that might already be current.
func TestEmptyTokenAlwaysRefetches(t *testing.T) {
	c := cache.New[int]()

	calls := 0
	for range 3 {
		if _, err := c.Get(context.Background(), "key", "", counting(&calls)); err != nil {
			t.Fatalf("Get: %v", err)
		}
	}

	if calls != 3 {
		t.Errorf("fetched %d times, want 3", calls)
	}
}

// Proton records a change in its event loop a moment after accepting it, so
// just after a write the token still reads as it did before.
func TestInvalidateBeatsAnUnchangedToken(t *testing.T) {
	c := cache.New[int]()

	calls := 0

	if _, err := c.Get(context.Background(), "key", "token-1", counting(&calls)); err != nil {
		t.Fatalf("Get: %v", err)
	}

	c.Invalidate("key")

	if _, err := c.Get(context.Background(), "key", "token-1", counting(&calls)); err != nil {
		t.Fatalf("Get: %v", err)
	}

	if calls != 2 {
		t.Errorf("fetched %d times after invalidating, want 2", calls)
	}
}

func TestKeysAreIndependent(t *testing.T) {
	c := cache.New[int]()

	calls := 0

	if _, err := c.Get(context.Background(), "a", "token", counting(&calls)); err != nil {
		t.Fatalf("Get: %v", err)
	}

	c.Invalidate("a")

	before := calls
	if _, err := c.Get(context.Background(), "b", "token", counting(&calls)); err != nil {
		t.Fatalf("Get: %v", err)
	}

	if calls != before+1 {
		t.Errorf("key b fetched %d times, want once", calls-before)
	}
}

func TestFetchErrorIsNotCached(t *testing.T) {
	c := cache.New[int]()

	boom := errors.New("boom")

	if _, err := c.Get(context.Background(), "key", "token", func(context.Context) ([]int, error) {
		return nil, boom
	}); !errors.Is(err, boom) {
		t.Fatalf("Get = %v, want boom", err)
	}

	items, err := c.Get(context.Background(), "key", "token", func(context.Context) ([]int, error) {
		return []int{42}, nil
	})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if len(items) != 1 || items[0] != 42 {
		t.Errorf("after a failed fetch, Get returned %v", items)
	}
}

// A polling client opens several connections at once.
func TestConcurrentGetFetchesOnce(t *testing.T) {
	c := cache.New[int]()

	var mu sync.Mutex
	calls := 0

	fetch := func(context.Context) ([]int, error) {
		mu.Lock()
		calls++
		mu.Unlock()

		time.Sleep(10 * time.Millisecond)

		return []int{1}, nil
	}

	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			if _, err := c.Get(context.Background(), "key", "token", fetch); err != nil {
				t.Errorf("Get: %v", err)
			}
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()

	if calls != 1 {
		t.Errorf("fetched %d times concurrently, want 1", calls)
	}
}

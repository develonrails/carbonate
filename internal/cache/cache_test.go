package cache_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/develonrails/carbonate/internal/cache"
)

func TestGetCachesUntilTTL(t *testing.T) {
	c := cache.New[int](time.Minute)

	calls := 0
	fetch := func(context.Context) ([]int, error) {
		calls++
		return []int{calls}, nil
	}

	for range 3 {
		if _, err := c.Get(context.Background(), "key", fetch); err != nil {
			t.Fatalf("Get: %v", err)
		}
	}

	if calls != 1 {
		t.Errorf("fetched %d times, want 1", calls)
	}
}

func TestInvalidateForcesRefetch(t *testing.T) {
	c := cache.New[int](time.Minute)

	calls := 0
	fetch := func(context.Context) ([]int, error) {
		calls++
		return []int{calls}, nil
	}

	if _, err := c.Get(context.Background(), "key", fetch); err != nil {
		t.Fatalf("Get: %v", err)
	}

	c.Invalidate("key")

	if _, err := c.Get(context.Background(), "key", fetch); err != nil {
		t.Fatalf("Get: %v", err)
	}

	if calls != 2 {
		t.Errorf("fetched %d times after invalidating, want 2", calls)
	}
}

func TestKeysAreIndependent(t *testing.T) {
	c := cache.New[int](time.Minute)

	fetch := func(context.Context) ([]int, error) { return []int{1}, nil }

	if _, err := c.Get(context.Background(), "a", fetch); err != nil {
		t.Fatalf("Get: %v", err)
	}

	c.Invalidate("a")

	calls := 0
	counting := func(context.Context) ([]int, error) {
		calls++
		return []int{1}, nil
	}

	if _, err := c.Get(context.Background(), "b", counting); err != nil {
		t.Fatalf("Get: %v", err)
	}

	if calls != 1 {
		t.Errorf("key b fetched %d times, want 1", calls)
	}
}

// A failed fetch must not be cached as a result.
func TestFetchErrorIsNotCached(t *testing.T) {
	c := cache.New[int](time.Minute)

	boom := errors.New("boom")

	if _, err := c.Get(context.Background(), "key", func(context.Context) ([]int, error) {
		return nil, boom
	}); !errors.Is(err, boom) {
		t.Fatalf("Get = %v, want boom", err)
	}

	items, err := c.Get(context.Background(), "key", func(context.Context) ([]int, error) {
		return []int{42}, nil
	})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if len(items) != 1 || items[0] != 42 {
		t.Errorf("after a failed fetch, Get returned %v", items)
	}
}

// A client polling from several connections must not multiply the work.
func TestConcurrentGetFetchesOnce(t *testing.T) {
	c := cache.New[int](time.Minute)

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

			if _, err := c.Get(context.Background(), "key", fetch); err != nil {
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

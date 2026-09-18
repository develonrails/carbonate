package caldav

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Proton rate-limits. A watch set to half a minute that kept that pace through
// an outage is the thing that would earn the limit.
func TestBackoffDoublesUpToACeiling(t *testing.T) {
	every := 30 * time.Second

	wait := backoff(0, every)
	if wait != every {
		t.Fatalf("first backoff = %s, want the interval that was asked for", wait)
	}

	seen := []time.Duration{wait}

	for range 12 {
		wait = backoff(wait, every)
		seen = append(seen, wait)
	}

	for i := 1; i < len(seen); i++ {
		if seen[i] < seen[i-1] {
			t.Errorf("backoff went backwards: %v", seen)
		}
	}

	if last := seen[len(seen)-1]; last != maxWatchInterval {
		t.Errorf("settled at %s, want the ceiling %s", last, maxWatchInterval)
	}
}

// A watch asked for once an hour must not be sped up to half an hour by the
// ceiling meant to slow other people down.
func TestBackoffNeverOutpacesTheChosenInterval(t *testing.T) {
	every := 2 * time.Hour

	wait := backoff(0, every)
	for range 5 {
		wait = backoff(wait, every)

		if wait < every {
			t.Fatalf("backoff = %s, which is more often than the %s asked for", wait, every)
		}
	}
}

// A watch that is working keeps to the pace it was given. Backing off is for
// failure, and must not leak into the healthy path.
func TestWatchKeepsPaceWhileHealthy(t *testing.T) {
	fake := newFake()
	b := New(fake, nil)

	every := 20 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	b.Watch(ctx, every, nil)

	// Deliberately loose: this asserts that the interval did not grow, not
	// that the scheduler is punctual.
	if fake.tokenCalls < 5 {
		t.Errorf("checked %d times in 300ms at a %s interval, which means it backed off while healthy",
			fake.tokenCalls, every)
	}
}

func TestCheckReportsAFailure(t *testing.T) {
	fake := newFake()
	b := New(fake, nil)

	if !b.check(context.Background()) {
		t.Error("a healthy store was reported as failing")
	}

	fake.err = errors.New("Proton is unreachable")

	if b.check(context.Background()) {
		t.Error("a failing store was reported as healthy")
	}
}

// Zero means the clients decide, so nothing should be running at all.
func TestWatchDoesNothingWhenDisabled(t *testing.T) {
	fake := newFake()
	b := New(fake, nil)

	done := make(chan struct{})

	go func() {
		b.Watch(context.Background(), 0, nil)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Watch kept running although it was disabled")
	}

	if fake.eventCalls != 0 {
		t.Errorf("a disabled watch read the calendar %d times", fake.eventCalls)
	}
}

// The store says what went wrong; the watch says what it will do about it,
// which is the part the store cannot know.
func TestWatchSaysWhenItWillTryAgain(t *testing.T) {
	fake := newFake()
	fake.err = errors.New("Proton is unreachable")

	var log bytes.Buffer

	b := New(fake, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	b.Watch(ctx, 10*time.Millisecond, &log)

	if !strings.Contains(log.String(), "trying again in") {
		t.Errorf("the watch never said when it would retry:\n%s", log.String())
	}
}

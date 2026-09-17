package caldav

import (
	"context"
	"fmt"
	"io"
	"time"
)

// maxWatchInterval is how far apart the checks are allowed to drift while
// Proton is unreachable.
//
// Backing off without a ceiling would mean a bridge that recovers hours after
// the network did. Half an hour is far enough to stop hammering a rate limit
// and near enough that nobody notices the delay once it clears.
const maxWatchInterval = 30 * time.Minute

// Watch asks Proton whether anything has changed, on its own schedule, until
// the context is cancelled.
//
// Nothing needs this to serve a client correctly: a poll from a client already
// checks the change token, and CalDAV has no way to tell a client to come
// sooner, so this cannot make a calendar app notice a change any earlier.
//
// What it does is make the change visible. Without it, "nothing is arriving
// from Proton" cannot be told apart from "nothing is asking" — carbonate is
// silent in both cases, because it only talks to Proton when a client does.
// Watching turns the first into a line in the log.
//
// It also leaves the cache warm, so the client that eventually polls is
// answered from memory rather than waiting for a calendar to be decrypted.
//
// A failing check doubles the wait, up to maxWatchInterval, and success puts
// it straight back. Proton rate-limits, and a watch set to half a minute that
// kept that pace through an outage would be the thing that earned the limit.
func (b *Backend) Watch(ctx context.Context, every time.Duration, out io.Writer) {
	if every <= 0 {
		return
	}

	wait := time.Duration(0)

	for {
		timer := time.NewTimer(wait)

		select {
		case <-ctx.Done():
			timer.Stop()

			return
		case <-timer.C:
		}

		if b.check(ctx) {
			wait = every

			continue
		}

		// The store has already said what went wrong; this says what carbonate
		// is going to do about it, which is the part it cannot know.
		wait = backoff(wait, every)

		if out != nil {
			fmt.Fprintf(out, "carbonate: that did not work — trying again in %s\n", wait)
		}
	}
}

// backoff doubles the wait, without going below the interval that was asked
// for or above the ceiling.
func backoff(current, every time.Duration) time.Duration {
	next := current * 2
	if next < every {
		next = every
	}

	ceiling := maxWatchInterval
	if every > ceiling {
		ceiling = every
	}

	if next > ceiling {
		next = ceiling
	}

	return next
}

// check reads every calendar, refetching only the ones whose change token has
// moved, and reports whether all of them answered.
//
// What went wrong is left to the store to say: it is the thing that knows.
func (b *Backend) check(ctx context.Context) bool {
	calendars, err := b.store.Calendars(ctx)
	if err != nil {
		return false
	}

	ok := true

	for _, c := range calendars {
		if ctx.Err() != nil {
			return true
		}

		if _, err := b.events(ctx, c.ID); err != nil {
			ok = false
		}
	}

	return ok
}

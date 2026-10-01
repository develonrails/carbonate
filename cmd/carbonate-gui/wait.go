//go:build gtk

package main

import (
	"context"
	"time"
)

// bindGrace is how long start waits before calling the server up. Long enough
// for a bind to fail and be reported, short enough not to be felt.
const bindGrace = 250 * time.Millisecond

func afterBind() <-chan time.Time {
	return time.After(bindGrace)
}

// wait sleeps for d, and reports false if ctx ended first.
func wait(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

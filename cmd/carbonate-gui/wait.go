//go:build gtk

package main

import "time"

// bindGrace is how long start waits before calling the server up. Long enough
// for a bind to fail and be reported, short enough not to be felt.
const bindGrace = 250 * time.Millisecond

func afterBind() <-chan time.Time {
	return time.After(bindGrace)
}

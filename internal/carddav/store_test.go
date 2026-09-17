package carddav

import (
	"strings"
	"testing"
)

// A contact that cannot be decrypted is left out of the address book. Leaving
// it out quietly is the part that hurts: the book is simply one contact short
// and nothing says which, or why.
func TestUnreadableContactsAreNamed(t *testing.T) {
	var log strings.Builder

	s := &protonStore{out: &log, said: make(map[string]bool)}

	s.report([]string{"aaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbb"})

	for _, want := range []string{"aaaaaaaa", "bbbbbbbb", "decrypted"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("the log does not mention %q:\n%s", want, log.String())
		}
	}
}

// The listing runs on every poll, so the same unreadable contact would fill
// the log with one line a minute.
func TestUnreadableContactsAreNamedOnce(t *testing.T) {
	var log strings.Builder

	s := &protonStore{out: &log, said: make(map[string]bool)}

	s.report([]string{"aaaaaaaaaaaaaaaaaaaa"})
	s.report([]string{"aaaaaaaaaaaaaaaaaaaa"})
	s.report([]string{"aaaaaaaaaaaaaaaaaaaa"})

	if got := strings.Count(log.String(), "aaaaaaaa"); got != 1 {
		t.Errorf("the same contact was reported %d times:\n%s", got, log.String())
	}
}

// A store with nowhere to report to must not panic on one.
func TestUnreadableContactsWithoutALog(t *testing.T) {
	s := &protonStore{said: make(map[string]bool)}

	s.report([]string{"aaaaaaaaaaaaaaaaaaaa"})
}

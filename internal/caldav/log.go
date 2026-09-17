package caldav

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/develonrails/carbonate/internal/calendar"
)

// Logging returns a Store that says when Proton reports a change, and what
// reading it cost.
//
// "Nothing arrives from Proton" is the hardest report to act on, because every
// stage of the path is silent. The change token either moves or it does not;
// the cache either refetches or it does not. Watching the DAV requests shows
// neither — a client polling a calendar that never changes looks exactly like
// a client polling a calendar whose changes carbonate cannot see.
//
// A poll that found nothing says nothing, so an idle bridge stays quiet and
// the lines that do appear are the ones worth reading.
type loggingStore struct {
	Store

	out io.Writer

	// seen is the last token reported for each calendar, so that only a
	// change is worth a line.
	mu   sync.Mutex
	seen map[string]string
}

// Logging wraps a Store so that changes coming from Proton are reported to
// out. A nil writer returns the Store unchanged.
func Logging(s Store, out io.Writer) Store {
	if out == nil {
		return s
	}

	return &loggingStore{Store: s, out: out, seen: make(map[string]string)}
}

// short renders a Proton ID at a length that identifies it in a log without
// filling the line. They are base64 and about ninety characters.
func short(id string) string {
	if len(id) <= 8 {
		return id
	}

	return id[:8] + "…"
}

func (s *loggingStore) ChangeToken(ctx context.Context, calendarID string) (string, error) {
	token, err := s.Store.ChangeToken(ctx, calendarID)
	if err != nil {
		s.printf("calendar %s: cannot read the change token: %v", short(calendarID), err)

		return token, err
	}

	s.mu.Lock()
	previous, known := s.seen[calendarID]
	s.seen[calendarID] = token
	s.mu.Unlock()

	// The first token is the starting point, not a change.
	if known && previous != token {
		s.printf("calendar %s: Proton reports a change (%s)", short(calendarID), short(token))
	}

	return token, nil
}

func (s *loggingStore) Events(ctx context.Context, calendarID string) ([]calendar.Event, error) {
	events, err := s.Store.Events(ctx, calendarID)
	if err != nil {
		s.printf("calendar %s: reading events failed: %v", short(calendarID), err)

		return nil, err
	}

	s.printf("calendar %s: read %d events from Proton", short(calendarID), len(events))

	return events, nil
}

func (s *loggingStore) Put(ctx context.Context, calendarID, ics string) (string, bool, error) {
	uid, created, err := s.Store.Put(ctx, calendarID, ics)
	if err != nil {
		s.printf("calendar %s: writing failed: %v", short(calendarID), err)

		return uid, created, err
	}

	what := "updated"
	if created {
		what = "created"
	}

	s.printf("calendar %s: %s %s", short(calendarID), what, uid)

	return uid, created, nil
}

func (s *loggingStore) Delete(ctx context.Context, calendarID, uid string) (bool, error) {
	deleted, err := s.Store.Delete(ctx, calendarID, uid)
	if err != nil {
		s.printf("calendar %s: deleting %s failed: %v", short(calendarID), uid, err)

		return deleted, err
	}

	if deleted {
		s.printf("calendar %s: deleted %s", short(calendarID), uid)
	}

	return deleted, nil
}

func (s *loggingStore) printf(format string, args ...any) {
	fmt.Fprintf(s.out, "carbonate: "+format+"\n", args...)
}

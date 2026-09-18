package caldav

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/develonrails/carbonate/internal/calendar"
	"github.com/develonrails/carbonate/internal/proton"
)

// Store is everything the CalDAV backend needs from Proton.
//
// It exists so the backend can be exercised without an account. The methods
// are the operations CalDAV actually performs, not a window onto the API:
// that keeps the fake in the tests small enough to be obviously correct.
type Store interface {
	// Calendars lists the account's calendars.
	Calendars(ctx context.Context) ([]calendar.Calendar, error)

	// Events returns every event in a calendar, decrypted.
	Events(ctx context.Context, calendarID string) ([]calendar.Event, error)

	// Put creates an event or replaces the one with the same UID, reporting
	// whether it was created.
	Put(ctx context.Context, calendarID, ics string) (uid string, created bool, err error)

	// Delete removes the event with the given UID, reporting whether one was
	// there.
	Delete(ctx context.Context, calendarID, uid string) (bool, error)

	// ChangeToken returns a value that changes whenever anything in the
	// calendar does, and only then.
	ChangeToken(ctx context.Context, calendarID string) (string, error)

	// Cursor returns the position in the change log that a client starting
	// from nothing should sync from.
	Cursor(ctx context.Context, calendarID string) (string, error)

	// Changes reports which events have moved since a token, and returns a
	// token describing the new state.
	//
	// resync means the answer cannot be given — the token is too old, or
	// Proton asked for a fresh start — and the caller should fall back to
	// reading everything.
	Changes(ctx context.Context, calendarID, since string) (ids []string, token string, resync bool, err error)
}

// protonStore is the Store backed by a live Proton connection.
type protonStore struct {
	conn *proton.Conn

	// out is where an event that cannot be read is reported, and reported is
	// how it stays: a calendar that is quietly one event short is worse than
	// one that says which and why.
	out io.Writer

	// said remembers which ids have been reported, because the listing runs
	// on every poll and the same unreadable event would otherwise fill the
	// log.
	mu   sync.Mutex
	said map[string]bool
}

// NewStore returns a Store reading and writing a Proton account. Events that
// cannot be decrypted are reported to out; a nil writer says nothing.
func NewStore(conn *proton.Conn, out io.Writer) Store {
	return &protonStore{conn: conn, out: out, said: make(map[string]bool)}
}

func (s *protonStore) Calendars(ctx context.Context) ([]calendar.Calendar, error) {
	return calendar.List(ctx, s.conn)
}

func (s *protonStore) Events(ctx context.Context, calendarID string) ([]calendar.Event, error) {
	events, unreadable, err := calendar.Events(ctx, s.conn, calendarID)
	if err != nil {
		return nil, err
	}

	s.report(calendarID, unreadable)

	return events, nil
}

// report names each unreadable event once.
func (s *protonStore) report(calendarID string, ids []string) {
	if s.out == nil || len(ids) == 0 {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, id := range ids {
		if s.said[id] {
			continue
		}

		s.said[id] = true

		fmt.Fprintf(s.out, "carbonate: calendar %s: event %s cannot be decrypted, so it is not being served\n", short(calendarID), short(id))
	}
}

func (s *protonStore) Put(ctx context.Context, calendarID, ics string) (string, bool, error) {
	return calendar.Put(ctx, s.conn, calendarID, ics)
}

func (s *protonStore) Delete(ctx context.Context, calendarID, uid string) (bool, error) {
	return calendar.Delete(ctx, s.conn, calendarID, uid)
}

// ChangeToken reads the calendar event loop's latest ID.
//
// One cheap request for a token that moves whenever the calendar does. A token
// derived from the events themselves would mean listing them, which is the
// work it exists to avoid.
func (s *protonStore) ChangeToken(ctx context.Context, calendarID string) (string, error) {
	var res struct {
		CalendarModelEventID string
	}

	if err := s.conn.Get(ctx, "/calendar/v1/"+calendarID+"/modelevents/latest", &res); err != nil {
		return "", err
	}

	return res.CalendarModelEventID, nil
}

// Cursor returns the position a client starting from nothing should sync from:
// the event loop's latest ID, and nothing cleverer.
//
// This used to read the loop *at* that ID and hand back the cursor that came
// with it, on the reasoning that the latest ID is not a consumed position and
// would make the next delta repeat the change that produced it. Measured
// against a live account, both halves of that are wrong, and the second one
// loses data:
//
//	from the latest ID     two events created since → both reported
//	from the derived one   the same two             → only the later one
//
// The derived cursor sits past changes that had not happened when it was
// issued, so the first change after a full sync is skipped — and since the
// client stores the token it is given, that event never arrives at all. It is
// exactly the shape of a calendar that syncs once and then goes quiet.
//
// The repeat that was feared does not happen either: asking the loop for
// changes since its own latest ID, with nothing changed, answers with no
// events and leaves the cursor where it was.
func (s *protonStore) Cursor(ctx context.Context, calendarID string) (string, error) {
	return s.ChangeToken(ctx, calendarID)
}

// Changes walks Proton's calendar event loop from a cursor.
//
// The loop reports an action per event, but carbonate ignores it and looks
// each ID up in the current state instead: an event that is still there
// changed, one that is gone was removed. That needs no assumption about what
// the action codes mean, and cannot disagree with what a read would show.
func (s *protonStore) Changes(ctx context.Context, calendarID, since string) ([]string, string, bool, error) {
	if since == "" {
		return nil, "", true, nil
	}

	var ids []string

	cursor := since

	// The loop is paged, and a client that has been away a while may have to
	// be walked through several pages to catch up.
	for range maxChangePages {
		var res struct {
			CalendarModelEventID string
			Refresh              int
			More                 int
			CalendarEvents       []struct {
				ID string
			}
		}

		if err := s.conn.Get(ctx, "/calendar/v1/"+calendarID+"/modelevents/"+cursor, &res); err != nil {
			// An unusable cursor is not a failure: it means the client has
			// been away longer than Proton remembers, and must read
			// everything again.
			return nil, "", true, nil
		}

		// Proton asking for a refresh means it will not account for what
		// changed, so neither can we.
		if res.Refresh != 0 {
			return nil, "", true, nil
		}

		for _, e := range res.CalendarEvents {
			ids = append(ids, e.ID)
		}

		cursor = res.CalendarModelEventID

		if res.More == 0 {
			return ids, cursor, false, nil
		}
	}

	// Further behind than we will walk. Reading everything is cheaper than
	// paging indefinitely, and gives the same answer.
	return nil, "", true, nil
}

// maxChangePages bounds how far back a client may be caught up from before
// carbonate gives up and has it read everything instead.
const maxChangePages = 20

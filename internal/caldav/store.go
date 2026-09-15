package caldav

import (
	"context"

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

	// Cursor returns a position in the change log that has already been
	// consumed, for a client starting from nothing.
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
}

// NewStore returns a Store reading and writing a Proton account.
func NewStore(conn *proton.Conn) Store {
	return protonStore{conn: conn}
}

func (s protonStore) Calendars(ctx context.Context) ([]calendar.Calendar, error) {
	return calendar.List(ctx, s.conn)
}

func (s protonStore) Events(ctx context.Context, calendarID string) ([]calendar.Event, error) {
	return calendar.Events(ctx, s.conn, calendarID)
}

func (s protonStore) Put(ctx context.Context, calendarID, ics string) (string, bool, error) {
	return calendar.Put(ctx, s.conn, calendarID, ics)
}

func (s protonStore) Delete(ctx context.Context, calendarID, uid string) (bool, error) {
	return calendar.Delete(ctx, s.conn, calendarID, uid)
}

// ChangeToken reads the calendar event loop's latest ID.
//
// One cheap request for a token that moves whenever the calendar does. A token
// derived from the events themselves would mean listing them, which is the
// work it exists to avoid.
func (s protonStore) ChangeToken(ctx context.Context, calendarID string) (string, error) {
	var res struct {
		CalendarModelEventID string
	}

	if err := s.conn.Get(ctx, "/calendar/v1/"+calendarID+"/modelevents/latest", &res); err != nil {
		return "", err
	}

	return res.CalendarModelEventID, nil
}

// Cursor returns a position in the calendar event loop that has already been
// consumed, so that asking for changes since it reports only what happens
// afterwards.
//
// The event loop's "latest" ID and the cursor a delta hands back are not the
// same value, and handing out the former as a sync token makes the next delta
// repeat the change that produced it.
func (s protonStore) Cursor(ctx context.Context, calendarID string) (string, error) {
	latest, err := s.ChangeToken(ctx, calendarID)
	if err != nil {
		return "", err
	}

	var res struct {
		CalendarModelEventID string
	}

	if err := s.conn.Get(ctx, "/calendar/v1/"+calendarID+"/modelevents/"+latest, &res); err != nil {
		return "", err
	}

	return res.CalendarModelEventID, nil
}

// Changes walks Proton's calendar event loop from a cursor.
//
// The loop reports an action per event, but carbonate ignores it and looks
// each ID up in the current state instead: an event that is still there
// changed, one that is gone was removed. That needs no assumption about what
// the action codes mean, and cannot disagree with what a read would show.
func (s protonStore) Changes(ctx context.Context, calendarID, since string) ([]string, string, bool, error) {
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

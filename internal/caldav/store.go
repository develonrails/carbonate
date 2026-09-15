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

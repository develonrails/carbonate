package caldav

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/develonrails/carbonate/internal/calendar"
)

// fakeStore stands in for Proton. It records what was asked of it, so a test
// can check not only the answer but the work done to produce it.
type fakeStore struct {
	calendars []calendar.Calendar
	events    map[string][]calendar.Event
	token     string

	calendarCalls int
	eventCalls    int
	tokenCalls    int

	put    []string
	delete []string

	err error
}

func newFake() *fakeStore {
	return &fakeStore{
		calendars: []calendar.Calendar{{ID: "cal-1", Name: "Work"}},
		events: map[string][]calendar.Event{
			"cal-1": {{
				ID:         "event-1",
				UID:        "meeting@example.com",
				Start:      time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC),
				Modified:   time.Unix(1000, 0),
				Properties: []string{"UID:meeting@example.com", "DTSTART:20260920T090000Z", "SUMMARY:Meeting"},
			}},
		},
		token: "token-1",
	}
}

func (f *fakeStore) Calendars(context.Context) ([]calendar.Calendar, error) {
	f.calendarCalls++

	if f.err != nil {
		return nil, f.err
	}

	return f.calendars, nil
}

func (f *fakeStore) Events(_ context.Context, calendarID string) ([]calendar.Event, error) {
	f.eventCalls++

	if f.err != nil {
		return nil, f.err
	}

	return f.events[calendarID], nil
}

func (f *fakeStore) Put(_ context.Context, calendarID, ics string) (string, bool, error) {
	f.put = append(f.put, ics)

	if f.err != nil {
		return "", false, f.err
	}

	uid := ""
	for _, line := range strings.Split(ics, "\r\n") {
		if after, ok := strings.CutPrefix(line, "UID:"); ok {
			uid = after
		}
	}

	if uid == "" {
		return "", false, errors.New("no UID")
	}

	f.events[calendarID] = append(f.events[calendarID], calendar.Event{
		ID:         "new",
		UID:        uid,
		Modified:   time.Unix(2000, 0),
		Properties: []string{"UID:" + uid},
	})

	return uid, true, nil
}

func (f *fakeStore) Delete(_ context.Context, calendarID, uid string) (bool, error) {
	f.delete = append(f.delete, uid)

	if f.err != nil {
		return false, f.err
	}

	kept := make([]calendar.Event, 0, len(f.events[calendarID]))
	found := false

	for _, e := range f.events[calendarID] {
		if e.UID == uid {
			found = true

			continue
		}

		kept = append(kept, e)
	}

	f.events[calendarID] = kept

	return found, nil
}

func (f *fakeStore) ChangeToken(context.Context, string) (string, error) {
	f.tokenCalls++

	if f.err != nil {
		return "", f.err
	}

	return f.token, nil
}

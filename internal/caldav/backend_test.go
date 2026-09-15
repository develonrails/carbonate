package caldav

import (
	"context"
	"strings"

	"github.com/emersion/go-ical"
	"testing"
	"time"

	"github.com/develonrails/carbonate/internal/calendar"
	"github.com/develonrails/carbonate/internal/proton"
)

func TestCalendarSegment(t *testing.T) {
	tests := map[string]string{
		homeSetPath + "abc123/":          "abc123",
		homeSetPath + "abc123":           "abc123",
		homeSetPath + "abc123/event.ics": "abc123",
		homeSetPath:                      "",
		principalPath:                    "",

		// A path outside the home set must yield nothing rather than a
		// segment that would address someone else's calendar.
		"/carddav/principal/contacts/default/": "",
		"/somewhere/else/abc123/":              "",
		"/":                                    "",
	}

	for path, want := range tests {
		if got := calendarSegment(path); got != want {
			t.Errorf("calendarSegment(%q) = %q, want %q", path, got, want)
		}
	}
}

// Proton IDs are base64 with padding, which has no place in a URL path.
func TestTokenIsURLSafe(t *testing.T) {
	id := "7WcS1DSkFTERiPA3YO1zL3Qm+yC/vTQBjZwfINezH775Xxq2XManNs8xTpJTiNwCTTMZPWmhWGohMKWJUxxt3cw=="

	got := token(id)

	for _, r := range got {
		if !('a' <= r && r <= 'f') && !('0' <= r && r <= '9') {
			t.Fatalf("token(%q) = %q, which is not URL-safe", id, got)
		}
	}

	if token(id) != got {
		t.Error("token is not stable across calls")
	}

	if token(id) == token(id+"x") {
		t.Error("different IDs produced the same token")
	}
}

func newBackend() *Backend {
	return New((*proton.Conn)(nil), time.Minute)
}

// A client may name a resource whatever it likes; the UID lives in the body.
// Losing that mapping means a later GET or DELETE cannot find the event.
func TestObjectUIDUsesRememberedName(t *testing.T) {
	b := newBackend()

	path := homeSetPath + "abc123/some-client-chosen-name.ics"
	b.names[path] = "the-real-uid@example.com"

	got, err := b.objectUID(path)
	if err != nil {
		t.Fatalf("objectUID: %v", err)
	}

	if got != "the-real-uid@example.com" {
		t.Errorf("objectUID = %q, want the remembered UID", got)
	}
}

// Paths carbonate advertises are named after the UID, so an unknown path is
// assumed to follow that convention.
func TestObjectUIDFallsBackToFilename(t *testing.T) {
	b := newBackend()

	got, err := b.objectUID(homeSetPath + "abc123/event%40example.com.ics")
	if err != nil {
		t.Fatalf("objectUID: %v", err)
	}

	if got != "event@example.com" {
		t.Errorf("objectUID = %q, want %q", got, "event@example.com")
	}
}

func TestObjectUIDRejectsNonObject(t *testing.T) {
	b := newBackend()

	if _, err := b.objectUID(homeSetPath + "abc123/notanevent"); err == nil {
		t.Error("a path without .ics was accepted as a calendar object")
	}
}

// A UID containing characters that are not URL-safe must survive the round
// trip through a path.
func TestObjectPathRoundTrip(t *testing.T) {
	b := newBackend()

	for _, uid := range []string{
		"simple@example.com",
		"with spaces@example.com",
		"with/slash@example.com",
		"with?question@example.com",
		"with#hash@example.com",
	} {
		path := objectPath("abc123", uid)

		got, err := b.objectUID(path)
		if err != nil {
			t.Errorf("objectUID(%q): %v", path, err)
			continue
		}

		if got != uid {
			t.Errorf("round trip of %q gave %q (path was %q)", uid, got, path)
		}
	}
}

// The ETag must change when the event does, or a client will never refetch.
func TestETagTracksModification(t *testing.T) {
	base := calendar.Event{ID: "event-id", Modified: time.Unix(1000, 0)}

	edited := base
	edited.Modified = time.Unix(2000, 0)

	if etag(base) == etag(edited) {
		t.Error("ETag did not change when the event was edited")
	}

	other := calendar.Event{ID: "different-id", Modified: time.Unix(1000, 0)}

	if etag(base) == etag(other) {
		t.Error("two different events share an ETag")
	}

	if etag(base) != etag(base) {
		t.Error("ETag is not stable")
	}
}

func TestPrincipalAndHomeSet(t *testing.T) {
	b := newBackend()

	principal, err := b.CurrentUserPrincipal(context.Background())
	if err != nil {
		t.Fatalf("CurrentUserPrincipal: %v", err)
	}

	if principal != principalPath {
		t.Errorf("principal = %q, want %q", principal, principalPath)
	}

	home, err := b.CalendarHomeSetPath(context.Background())
	if err != nil {
		t.Fatalf("CalendarHomeSetPath: %v", err)
	}

	if home != homeSetPath {
		t.Errorf("home set = %q, want %q", home, homeSetPath)
	}
}

// Calendars are made in Proton, not through the bridge. Refusing plainly beats
// accepting and quietly doing nothing.
func TestCalendarCreationIsRefused(t *testing.T) {
	if err := newBackend().CreateCalendar(context.Background(), nil); err == nil {
		t.Error("creating a calendar was allowed")
	}
}

// go-webdav infers a resource's kind from path depth below the prefix, so the
// layout must not drift: principal 1, home set 2, calendar 3, event 4.
func TestPathDepths(t *testing.T) {
	for path, want := range map[string]int{
		principalPath: 1,
		homeSetPath:   2,
	} {
		trimmed := path[len(prefix):]

		got := 0
		for i := 1; i < len(trimmed); i++ {
			if trimmed[i] == '/' {
				got++
			}
		}

		if got != want {
			t.Errorf("%s has depth %d below the prefix, want %d", path, got, want)
		}
	}
}

func calendarWith(t *testing.T, ics string) *ical.Calendar {
	t.Helper()

	cal, err := ical.NewDecoder(strings.NewReader(ics)).Decode()
	if err != nil {
		t.Fatalf("decoding test calendar: %v", err)
	}

	return cal
}

// A recurring event with an exception arrives as several components sharing a
// UID. carbonate stores one event per object, so this has to be refused — and
// refused as a limitation rather than a server fault, or the client's user is
// left thinking something broke.
func TestPutRejectsARecurrenceException(t *testing.T) {
	series := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\n" +
		"BEGIN:VEVENT\r\nUID:s@x\r\nDTSTAMP:20260915T080000Z\r\nDTSTART:20261006T090000Z\r\nRRULE:FREQ=WEEKLY\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:s@x\r\nDTSTAMP:20260915T080000Z\r\nRECURRENCE-ID:20261013T090000Z\r\nDTSTART:20261013T140000Z\r\nEND:VEVENT\r\n" +
		"END:VCALENDAR\r\n"

	err := checkSupported(calendarWith(t, series))
	if err == nil {
		t.Fatal("a multi-component event was accepted")
	}

	// go-webdav keeps its HTTP error type unexported, so the status is
	// asserted end to end elsewhere; here the message is what matters, since
	// it is what the user is shown.
	if !strings.Contains(err.Error(), "recurring") {
		t.Errorf("error %q does not explain what is unsupported", err)
	}
}

// The same applies to an exception sent on its own.
func TestPutRejectsALoneException(t *testing.T) {
	lone := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\n" +
		"BEGIN:VEVENT\r\nUID:s@x\r\nDTSTAMP:20260915T080000Z\r\nRECURRENCE-ID:20261013T090000Z\r\nDTSTART:20261013T140000Z\r\nEND:VEVENT\r\n" +
		"END:VCALENDAR\r\n"

	if err := checkSupported(calendarWith(t, lone)); err == nil {
		t.Error("a lone recurrence exception was accepted")
	}
}

// An ordinary recurring event is not an exception and must still go through.
func TestPutAcceptsAPlainSeries(t *testing.T) {
	series := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\n" +
		"BEGIN:VEVENT\r\nUID:s@x\r\nDTSTAMP:20260915T080000Z\r\nDTSTART:20261006T090000Z\r\nRRULE:FREQ=WEEKLY\r\nEND:VEVENT\r\n" +
		"END:VCALENDAR\r\n"

	if err := checkSupported(calendarWith(t, series)); err != nil {
		t.Errorf("a plain recurring event was rejected: %v", err)
	}
}

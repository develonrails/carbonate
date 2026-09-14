package caldav

import (
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

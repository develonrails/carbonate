package caldav

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"

	"github.com/develonrails/carbonate/internal/calendar"
)

func backendWith(store Store) *Backend {
	return New(store, nil)
}

func calendarPath(t *testing.T, b *Backend) string {
	t.Helper()

	calendars, err := b.ListCalendars(context.Background())
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}

	if len(calendars) != 1 {
		t.Fatalf("got %d calendars, want 1", len(calendars))
	}

	return calendars[0].Path
}

func TestListCalendarsNamesThem(t *testing.T) {
	b := backendWith(newFake())

	calendars, err := b.ListCalendars(context.Background())
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}

	if calendars[0].Name != "Work" {
		t.Errorf("name = %q, want Work", calendars[0].Name)
	}

	if !strings.HasPrefix(calendars[0].Path, homeSetPath) {
		t.Errorf("path %q is not under the home set", calendars[0].Path)
	}
}

// A calendar with no name would otherwise appear blank in a client's list.
func TestUnnamedCalendarStillGetsALabel(t *testing.T) {
	fake := newFake()
	fake.calendars[0].Name = ""

	calendars, err := backendWith(fake).ListCalendars(context.Background())
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}

	if calendars[0].Name == "" {
		t.Error("the calendar was left without a name")
	}
}

func TestListCalendarObjects(t *testing.T) {
	b := backendWith(newFake())
	path := calendarPath(t, b)

	objects, err := b.ListCalendarObjects(context.Background(), path, nil)
	if err != nil {
		t.Fatalf("ListCalendarObjects: %v", err)
	}

	if len(objects) != 1 {
		t.Fatalf("got %d objects, want 1", len(objects))
	}

	if objects[0].ETag == "" {
		t.Error("the object has no ETag, so a client can never skip it")
	}

	if !strings.HasSuffix(objects[0].Path, ".ics") {
		t.Errorf("path %q does not look like a calendar object", objects[0].Path)
	}
}

// The whole point of the change token: a second read costs nothing.
func TestEventsAreNotRefetchedWhileUnchanged(t *testing.T) {
	fake := newFake()
	b := backendWith(fake)
	path := calendarPath(t, b)

	for range 3 {
		if _, err := b.ListCalendarObjects(context.Background(), path, nil); err != nil {
			t.Fatalf("ListCalendarObjects: %v", err)
		}
	}

	if fake.eventCalls != 1 {
		t.Errorf("fetched the events %d times, want 1", fake.eventCalls)
	}
}

func TestEventsAreRefetchedWhenTheTokenMoves(t *testing.T) {
	fake := newFake()
	b := backendWith(fake)
	path := calendarPath(t, b)

	if _, err := b.ListCalendarObjects(context.Background(), path, nil); err != nil {
		t.Fatalf("ListCalendarObjects: %v", err)
	}

	fake.token = "token-2"

	if _, err := b.ListCalendarObjects(context.Background(), path, nil); err != nil {
		t.Fatalf("ListCalendarObjects: %v", err)
	}

	if fake.eventCalls != 2 {
		t.Errorf("fetched the events %d times, want 2", fake.eventCalls)
	}
}

// Proton records a change a moment after accepting it, so straight after a
// write the token still reads as it did before.
func TestWriteMakesTheNextReadFresh(t *testing.T) {
	fake := newFake()
	b := backendWith(fake)
	path := calendarPath(t, b)

	if _, err := b.ListCalendarObjects(context.Background(), path, nil); err != nil {
		t.Fatalf("ListCalendarObjects: %v", err)
	}

	before := fake.eventCalls

	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, "-//test//EN")

	event := ical.NewEvent()
	event.Props.SetText("UID", "new@example.com")
	event.Props.SetText("DTSTAMP", "20260915T000000Z")
	cal.Children = append(cal.Children, event.Component)

	if _, err := b.PutCalendarObject(context.Background(), path+"new.ics", cal, nil); err != nil {
		t.Fatalf("PutCalendarObject: %v", err)
	}

	if _, err := b.ListCalendarObjects(context.Background(), path, nil); err != nil {
		t.Fatalf("ListCalendarObjects: %v", err)
	}

	if fake.eventCalls == before {
		t.Error("the cache survived our own write, so the new event would be invisible")
	}
}

func TestGetCalendarObjectFindsByUID(t *testing.T) {
	b := backendWith(newFake())
	path := calendarPath(t, b)

	obj, err := b.GetCalendarObject(context.Background(), objectPath(calendarSegment(path), "meeting@example.com"), nil)
	if err != nil {
		t.Fatalf("GetCalendarObject: %v", err)
	}

	if obj.Data == nil {
		t.Fatal("the object carries no calendar data")
	}
}

func TestGetCalendarObjectRejectsAnUnknownUID(t *testing.T) {
	b := backendWith(newFake())
	path := calendarPath(t, b)

	if _, err := b.GetCalendarObject(context.Background(), objectPath(calendarSegment(path), "nobody@example.com"), nil); err == nil {
		t.Error("an unknown event was found")
	}
}

func TestDeleteRemovesTheEvent(t *testing.T) {
	fake := newFake()
	b := backendWith(fake)
	path := calendarPath(t, b)

	if err := b.DeleteCalendarObject(context.Background(), objectPath(calendarSegment(path), "meeting@example.com")); err != nil {
		t.Fatalf("DeleteCalendarObject: %v", err)
	}

	if len(fake.delete) != 1 || fake.delete[0] != "meeting@example.com" {
		t.Errorf("deleted %v, want the event's UID", fake.delete)
	}
}

func TestDeleteReportsAMissingEvent(t *testing.T) {
	b := backendWith(newFake())
	path := calendarPath(t, b)

	if err := b.DeleteCalendarObject(context.Background(), objectPath(calendarSegment(path), "nobody@example.com")); err == nil {
		t.Error("deleting an event that is not there succeeded")
	}
}

func TestQueryFiltersByTimeRange(t *testing.T) {
	b := backendWith(newFake())
	path := calendarPath(t, b)

	query := &caldav.CalendarQuery{
		CompFilter: caldav.CompFilter{
			Name: "VCALENDAR",
			Comps: []caldav.CompFilter{{
				Name:  "VEVENT",
				Start: mustTime("2020-01-01T00:00:00Z"),
				End:   mustTime("2020-02-01T00:00:00Z"),
			}},
		},
	}

	got, err := b.QueryCalendarObjects(context.Background(), path, query)
	if err != nil {
		t.Fatalf("QueryCalendarObjects: %v", err)
	}

	if len(got) != 0 {
		t.Errorf("a range with no events returned %d", len(got))
	}
}

// A failure from Proton must surface rather than be served as an empty
// calendar, which a client would take as "everything was deleted".
func TestStoreFailureIsReported(t *testing.T) {
	fake := newFake()
	b := backendWith(fake)
	path := calendarPath(t, b)

	fake.err = errors.New("Proton is unreachable")

	if _, err := b.ListCalendarObjects(context.Background(), path, nil); err == nil {
		t.Error("a failing store produced an empty calendar instead of an error")
	}
}

func mustTime(value string) time.Time {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}

	return t
}

// The shape of issue #18, at the layer the client actually sees.
//
// A calendar whose events cannot be read answers every listing with 500. GNOME
// Calendar shows an empty calendar rather than an error, so that is
// indistinguishable from Proton having sent nothing — which is exactly how it
// was reported. This pins the mechanism: the 500 is what has to stop happening
// for one bad event, not the listing that follows it.
func TestFailedEventReadAnswersTheCollectionWith500(t *testing.T) {
	fake := newFake()
	b := backendWith(fake)
	path := calendarPath(t, b)

	body := `<propfind xmlns="DAV:"><prop><getetag/></prop></propfind>`

	listing := func() int {
		req := httptest.NewRequest("PROPFIND", path, strings.NewReader(body))
		req.Header.Set("Depth", "1")
		req.Header.Set("Content-Type", "application/xml")

		rec := httptest.NewRecorder()
		b.Handler().ServeHTTP(rec, req)

		return rec.Code
	}

	if got := listing(); got != http.StatusMultiStatus {
		t.Fatalf("a readable calendar answered %d, want 207", got)
	}

	// One event carbonate cannot decode used to fail the whole read, which is
	// what this store is standing in for.
	fake.err = errors.New("decoding event G4Vr: Signature Verification Error")

	if got := listing(); got != http.StatusInternalServerError {
		t.Fatalf("a calendar whose events fail to read answered %d, want 500", got)
	}
}

// Decrypting is only the first place one event could empty a calendar;
// reassembling it into an iCalendar object is the second, and it ends the same
// way — an error from the listing, a 500, and a client showing nothing.
//
// The event that will not render is left out and named, and the rest of the
// calendar is served.
func TestUnrenderableEventIsSkippedAndNamed(t *testing.T) {
	fake := newFake()
	fake.events["cal-1"] = append(fake.events["cal-1"], calendar.Event{
		ID:         "event-2",
		UID:        "broken@example.com",
		Modified:   time.Unix(1000, 0),
		Properties: []string{"UID:broken@example.com", "this line has no colon"},
	})

	var log bytes.Buffer

	b := New(fake, &log)
	path := calendarPath(t, b)

	objects, err := b.ListCalendarObjects(context.Background(), path, nil)
	if err != nil {
		t.Fatalf("one unrenderable event failed the whole listing: %v", err)
	}

	if len(objects) != 1 {
		t.Fatalf("listed %d objects, want the 1 that could be rendered", len(objects))
	}

	if !strings.Contains(objects[0].Path, "meeting@example.com") {
		t.Errorf("listed %q, want the readable event", objects[0].Path)
	}

	if !strings.Contains(log.String(), "broken@example.com") {
		t.Errorf("the skipped event was not named:\n%s", log.String())
	}

	// A listing runs on every poll, so the same event must not fill the log.
	before := log.Len()

	if _, err := b.ListCalendarObjects(context.Background(), path, nil); err != nil {
		t.Fatalf("second listing: %v", err)
	}

	if log.Len() != before {
		t.Errorf("the same event was named twice:\n%s", log.String())
	}
}

// The third way one event could empty a calendar, and the one that matters
// most: calendar-query is the report Evolution — and so GNOME Calendar — uses
// to find out what is in a collection.
//
// Deciding whether an event falls in the queried range means parsing its dates
// and expanding its recurrence rule. go-webdav's Filter returns the first
// error and drops everything it had already matched, so one malformed event
// answered the whole report with a 500 and the client downsynced nothing.
func TestBadEventDoesNotFailTheWholeQuery(t *testing.T) {
	fake := newFake()
	fake.events["cal-1"] = append(fake.events["cal-1"], calendar.Event{
		ID:         "event-2",
		UID:        "baddate@example.com",
		Modified:   time.Unix(1000, 0),
		Properties: []string{"UID:baddate@example.com", "DTSTART:not-a-date"},
	})

	var log bytes.Buffer

	b := New(fake, &log)
	path := calendarPath(t, b)

	query := &caldav.CalendarQuery{
		CompFilter: caldav.CompFilter{
			Name: "VCALENDAR",
			Comps: []caldav.CompFilter{{
				Name:  "VEVENT",
				Start: mustTime("2026-09-01T00:00:00Z"),
				End:   mustTime("2026-10-01T00:00:00Z"),
			}},
		},
	}

	got, err := b.QueryCalendarObjects(context.Background(), path, query)
	if err != nil {
		t.Fatalf("one unmatchable event failed the whole query: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("query returned %d objects, want the 1 that could be matched", len(got))
	}

	if !strings.Contains(got[0].Path, "meeting@example.com") {
		t.Errorf("returned %q, want the readable event", got[0].Path)
	}

	if !strings.Contains(log.String(), "baddate@example.com") {
		t.Errorf("the skipped event was not named:\n%s", log.String())
	}
}

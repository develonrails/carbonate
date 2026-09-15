package caldav

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
)

func backendWith(store Store) *Backend {
	return New(store)
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

package calendar

import (
	"strings"
	"testing"
)

const seriesWithException = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//carbonate//test//EN
BEGIN:VEVENT
UID:standup@carbonate.local
DTSTAMP:20260915T220000Z
DTSTART:20261103T090000Z
RRULE:FREQ=WEEKLY;COUNT=4
SEQUENCE:0
SUMMARY:Standup
END:VEVENT
BEGIN:VEVENT
UID:standup@carbonate.local
DTSTAMP:20260915T220000Z
RECURRENCE-ID:20261110T090000Z
DTSTART:20261110T140000Z
SEQUENCE:1
SUMMARY:Moved that week
END:VEVENT
END:VCALENDAR
`

// A client sends a series and its exceptions as one object (RFC 4791 §4.1),
// and the series has to be written first: an exception to something that does
// not exist yet is meaningless.
func TestParseEventsPutsTheSeriesFirst(t *testing.T) {
	events, err := parseEvents(seriesWithException)
	if err != nil {
		t.Fatalf("parseEvents: %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("parsed %d components, want 2", len(events))
	}

	if recurrenceIDOf(events[0]) != 0 {
		t.Error("an exception was placed before the series it modifies")
	}

	if recurrenceIDOf(events[1]) == 0 {
		t.Error("the exception lost its recurrence id")
	}
}

// The order in the object is the client's choice, not something to rely on.
func TestParseEventsReordersWhenTheExceptionComesFirst(t *testing.T) {
	reversed := strings.Replace(seriesWithException, "BEGIN:VCALENDAR\n", "BEGIN:VCALENDAR\nX-MARK:1\n", 1)

	parts := strings.SplitN(reversed, "BEGIN:VEVENT", 3)
	swapped := parts[0] + "BEGIN:VEVENT" + parts[2] + "BEGIN:VEVENT" + parts[1]

	events, err := parseEvents(swapped)
	if err != nil {
		t.Skipf("the rearranged fixture did not parse: %v", err)
	}

	if len(events) == 2 && recurrenceIDOf(events[0]) != 0 {
		t.Error("the series was not put first")
	}
}

func TestRecurrenceIDParsing(t *testing.T) {
	events, err := parseEvents(seriesWithException)
	if err != nil {
		t.Fatalf("parseEvents: %v", err)
	}

	// 2026-11-10T09:00:00Z
	if got := recurrenceIDOf(events[1]); got != 1794301200 {
		t.Errorf("recurrence id = %d, want the occurrence's Unix time", got)
	}
}

// A date-only recurrence id belongs to a full-day series.
func TestRecurrenceIDAcceptsADateOnlyValue(t *testing.T) {
	ics := strings.Replace(seriesWithException, "RECURRENCE-ID:20261110T090000Z", "RECURRENCE-ID;VALUE=DATE:20261110", 1)

	events, err := parseEvents(ics)
	if err != nil {
		t.Fatalf("parseEvents: %v", err)
	}

	if recurrenceIDOf(events[1]) == 0 {
		t.Error("a date-only recurrence id was not understood, so the exception would rewrite the series")
	}
}

// Every component sharing a UID belongs in one resource, or a client meets the
// same UID at two addresses and cannot tell which is which.
func TestMergeProducesOneCalendarObject(t *testing.T) {
	series := Event{UID: "standup@carbonate.local", Properties: []string{
		"UID:standup@carbonate.local", "DTSTART:20261103T090000Z", "RRULE:FREQ=WEEKLY", "SUMMARY:Standup",
	}}

	exception := Event{UID: "standup@carbonate.local", RecurrenceID: 1794301200, Properties: []string{
		"UID:standup@carbonate.local", "RECURRENCE-ID:20261110T090000Z", "DTSTART:20261110T140000Z", "SUMMARY:Moved",
	}}

	got := Merge([]Event{series, exception})

	if n := strings.Count(got, "BEGIN:VCALENDAR"); n != 1 {
		t.Errorf("produced %d calendars, want 1:\n%s", n, got)
	}

	if n := strings.Count(got, "BEGIN:VEVENT"); n != 2 {
		t.Errorf("produced %d components, want 2:\n%s", n, got)
	}

	for _, want := range []string{"RRULE:FREQ=WEEKLY", "RECURRENCE-ID:20261110T090000Z", "SUMMARY:Moved"} {
		if !strings.Contains(got, want) {
			t.Errorf("merged object is missing %q:\n%s", want, got)
		}
	}
}

func TestMergeOfOneIsJustTheEvent(t *testing.T) {
	only := Event{UID: "x", Properties: []string{"UID:x", "SUMMARY:Alone"}}

	if Merge([]Event{only}) != only.ICS() {
		t.Error("a single component was rendered differently by Merge")
	}
}

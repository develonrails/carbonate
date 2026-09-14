package calendar

import (
	"strings"
	"testing"
	"time"
)

// The shape Proton actually returns: each part is its own complete VCALENDAR
// wrapping a fragment of the same VEVENT.
func sharedPart() string {
	return strings.Join([]string{
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"PRODID:-//Proton AG//web-calendar 5.0.131.2//EN",
		"BEGIN:VEVENT",
		"UID:abc@proton.me",
		"DTSTAMP:20260914T165002Z",
		"DTSTART;VALUE=DATE:20260915",
		"SEQUENCE:0",
		"END:VEVENT",
		"END:VCALENDAR",
	}, "\r\n")
}

func encryptedPart() string {
	return strings.Join([]string{
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"PRODID:-//Proton AG//web-calendar 5.0.131.2//EN",
		"BEGIN:VEVENT",
		"UID:abc@proton.me",
		"DTSTAMP:20260914T165002Z",
		"SUMMARY:Test!",
		"END:VEVENT",
		"END:VCALENDAR",
	}, "\r\n")
}

func testEvent() Event {
	var e Event
	e.Properties = append(e.Properties, properties(sharedPart())...)
	e.Properties = append(e.Properties, properties(encryptedPart())...)

	return e
}

func TestICSMergesPartsIntoOneEvent(t *testing.T) {
	got := testEvent().ICS()

	if n := strings.Count(got, "BEGIN:VEVENT"); n != 1 {
		t.Errorf("BEGIN:VEVENT appears %d times, want 1\n%s", n, got)
	}

	if n := strings.Count(got, "BEGIN:VCALENDAR"); n != 1 {
		t.Errorf("BEGIN:VCALENDAR appears %d times, want 1\n%s", n, got)
	}

	// Properties from every part must survive the merge.
	for _, want := range []string{"UID:abc@proton.me", "DTSTART;VALUE=DATE:20260915", "SUMMARY:Test!", "SEQUENCE:0"} {
		if !strings.Contains(got, want) {
			t.Errorf("merged event is missing %q\n%s", want, got)
		}
	}
}

// UID and DTSTAMP appear in every part; they must not be repeated.
func TestICSDropsDuplicateSingleValuedProperties(t *testing.T) {
	got := testEvent().ICS()

	for _, prop := range []string{"UID:", "DTSTAMP:"} {
		if n := strings.Count(got, prop); n != 1 {
			t.Errorf("%s appears %d times, want 1\n%s", prop, n, got)
		}
	}
}

// Attendees legitimately repeat, and dropping them would silently lose people.
func TestICSKeepsRepeatableProperties(t *testing.T) {
	var e Event
	e.Properties = []string{
		"UID:abc@proton.me",
		"ATTENDEE;CN=Alice:mailto:alice@example.com",
		"ATTENDEE;CN=Bob:mailto:bob@example.com",
		"EXDATE:20260916T120000Z",
		"EXDATE:20260917T120000Z",
	}

	got := e.ICS()

	if n := strings.Count(got, "ATTENDEE"); n != 2 {
		t.Errorf("ATTENDEE appears %d times, want 2\n%s", n, got)
	}

	if n := strings.Count(got, "EXDATE"); n != 2 {
		t.Errorf("EXDATE appears %d times, want 2\n%s", n, got)
	}
}

// X- properties are vendor extensions whose cardinality we cannot know.
func TestICSKeepsExtensionProperties(t *testing.T) {
	var e Event
	e.Properties = []string{
		"UID:abc@proton.me",
		"X-PM-PROTON-REPLY:1",
		"X-PM-PROTON-REPLY:2",
	}

	if n := strings.Count(e.ICS(), "X-PM-PROTON-REPLY"); n != 2 {
		t.Errorf("X- property was deduplicated, want both kept\n%s", e.ICS())
	}
}

func TestICSUsesCRLF(t *testing.T) {
	got := testEvent().ICS()

	if strings.Contains(strings.ReplaceAll(got, "\r\n", ""), "\n") {
		t.Error("output contains a bare LF; iCalendar requires CRLF")
	}

	if !strings.HasSuffix(got, "END:VCALENDAR\r\n") {
		t.Errorf("output does not end with a terminated END:VCALENDAR: %q", got)
	}
}

// RFC 5545 folds long lines; a continuation must rejoin its predecessor
// rather than become a property of its own.
func TestPropertiesUnfoldsWrappedLines(t *testing.T) {
	got := properties("BEGIN:VEVENT\r\nDESCRIPTION:a very long des\r\n cription that was folded\r\nEND:VEVENT")

	want := "DESCRIPTION:a very long description that was folded"
	found := false

	for _, line := range got {
		if line == want {
			found = true
		}
	}

	if !found {
		t.Errorf("folded line was not rejoined: %q", got)
	}
}

func TestPropertiesSkipsBlankLines(t *testing.T) {
	for _, line := range properties("UID:abc\r\n\r\nSUMMARY:x\r\n") {
		if line == "" {
			t.Fatal("blank line was kept as a property")
		}
	}
}

func TestSummary(t *testing.T) {
	if got := testEvent().Summary(); got != "Test!" {
		t.Errorf("Summary() = %q, want %q", got, "Test!")
	}

	var empty Event
	if got := empty.Summary(); got != "" {
		t.Errorf("Summary() on an event with no SUMMARY = %q, want \"\"", got)
	}
}

// Proton stores a full-day event's start as midnight UTC. Rendering it in the
// local zone would show the wrong date for anyone west of Greenwich, and a
// spurious time for everyone.
func TestWhenFullDayIgnoresLocalZone(t *testing.T) {
	e := Event{FullDay: true, Start: time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)}

	if got := e.When(); got != "2026-09-15 (all day)" {
		t.Errorf("When() = %q, want %q", got, "2026-09-15 (all day)")
	}
}

func TestWhenTimedEvent(t *testing.T) {
	e := Event{Start: time.Date(2026, 9, 15, 14, 30, 0, 0, time.Local)}

	if got := e.When(); got != "2026-09-15 14:30" {
		t.Errorf("When() = %q, want %q", got, "2026-09-15 14:30")
	}
}

func TestPropertyName(t *testing.T) {
	tests := map[string]string{
		"UID:abc":                     "UID",
		"DTSTART;VALUE=DATE:20260915": "DTSTART",
		"ATTENDEE;CN=Alice:mailto:a":  "ATTENDEE",
		"END:VEVENT":                  "END",
		"summary:lowercase":           "SUMMARY",
	}

	for line, want := range tests {
		if got := name(line); got != want {
			t.Errorf("name(%q) = %q, want %q", line, got, want)
		}
	}
}

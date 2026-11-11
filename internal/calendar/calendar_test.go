package calendar

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	api "github.com/ProtonMail/go-proton-api"
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

// The colour Proton keeps beside an event must reach the client as COLOR.
func TestICSCarriesTheEventsOwnColor(t *testing.T) {
	e := testEvent()
	e.Color = "#EC3E7C"

	got := e.ICS()
	if n := strings.Count(got, "COLOR:#EC3E7C"); n != 1 {
		t.Errorf("COLOR appears %d times, want 1\n%s", n, got)
	}
}

// RFC 5545 §3.6.1 puts an event's properties before its VALARM components,
// so a colour served next to a reminder has to come first.
func TestICSPutsColorBeforeTheAlarms(t *testing.T) {
	e := testEvent()
	e.Color = "#EC3E7C"
	e.Alarms = []notification{{Type: notifyDevice, Trigger: "-PT15M"}}

	got := e.ICS()

	color := strings.Index(got, "COLOR:")
	alarm := strings.Index(got, "BEGIN:VALARM")

	if alarm < 0 {
		t.Fatalf("no alarm served\n%s", got)
	}

	if color < 0 {
		t.Fatalf("no colour served\n%s", got)
	}

	if color > alarm {
		t.Errorf("COLOR follows the VALARM, want it before\n%s", got)
	}
}

// A COLOR line stored in a part is not the colour Proton shows, and must
// not reach the client alongside — or instead of — the event's own.
func TestICSIgnoresColorStoredInAPart(t *testing.T) {
	e := testEvent()
	e.Properties = append(e.Properties, "COLOR:orange")
	e.Color = "#1DA583"

	got := e.ICS()
	if strings.Contains(got, "COLOR:orange") {
		t.Errorf("colour stored in a part was served\n%s", got)
	}

	if n := strings.Count(got, "COLOR:"); n != 1 {
		t.Errorf("COLOR appears %d times, want 1\n%s", n, got)
	}
}

// An event with no colour of its own gets no COLOR line: the client then
// falls back to the calendar's colour, as Proton's own apps do.
func TestICSWithoutColorHasNoColorLine(t *testing.T) {
	got := testEvent().ICS()
	if strings.Contains(got, "COLOR") {
		t.Errorf("colourless event was served a COLOR line\n%s", got)
	}
}

// Proton sends the colour beside the event, as a palette hex or as null.
// go-proton-api decodes neither, so rawEvent has to.
func TestRawEventDecodesColor(t *testing.T) {
	var raw rawEvent
	if err := json.Unmarshal([]byte(`{"Color":"#EC3E7C"}`), &raw); err != nil {
		t.Fatalf("unmarshalling: %v", err)
	}

	if raw.Color == nil || *raw.Color != "#EC3E7C" {
		t.Errorf("colour decoded as %v, want #EC3E7C", raw.Color)
	}

	var bare rawEvent
	if err := json.Unmarshal([]byte(`{"Color":null}`), &bare); err != nil {
		t.Fatalf("unmarshalling: %v", err)
	}

	if bare.Color != nil {
		t.Errorf("null colour decoded as %q, want nil", *bare.Color)
	}
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

// A reply made anywhere else has to reach the client.
//
// Proton records it in the clear list beside the event and nowhere near the
// encrypted card, whose PARTSTAT stays at whatever the guest was invited
// with. Without this the client never sees an answer — and writes the stale
// one back on the next edit, which Proton then files as the guest's reply.
func TestRepliesReachTheClient(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef01234567"

	properties := []string{
		"UID:event@carbonate.local",
		"ATTENDEE;PARTSTAT=NEEDS-ACTION;X-PM-TOKEN=" + token + ":mailto:alice@example.com",
	}

	got := applyReplies(properties, []api.CalendarAttendee{
		{Token: token, Status: api.CalendarAttendeeStatus(statusAccepted)},
	})

	if !strings.Contains(got[1], "PARTSTAT=ACCEPTED") {
		t.Errorf("the reply did not reach the attendee: %s", got[1])
	}

	if strings.Contains(got[1], "NEEDS-ACTION") {
		t.Errorf("the stale status is still there: %s", got[1])
	}

	if !strings.Contains(got[1], "mailto:alice@example.com") {
		t.Errorf("the address was lost: %s", got[1])
	}
}

// An attendee Proton has no reply for keeps the status the card gave.
func TestUnknownTokenKeepsItsStatus(t *testing.T) {
	properties := []string{
		"ATTENDEE;PARTSTAT=TENTATIVE;X-PM-TOKEN=aaaa:mailto:alice@example.com",
	}

	got := applyReplies(properties, []api.CalendarAttendee{
		{Token: "bbbb", Status: api.CalendarAttendeeStatus(statusDeclined)},
	})

	if !strings.Contains(got[0], "PARTSTAT=TENTATIVE") {
		t.Errorf("status was changed on an attendee Proton said nothing about: %s", got[0])
	}
}

// An attendee with no PARTSTAT at all gets the reply added rather than
// dropped.
func TestReplyIsAddedWhenThereIsNoPartstat(t *testing.T) {
	const token = "abcd"

	got := applyReplies(
		[]string{"ATTENDEE;X-PM-TOKEN=" + token + ":mailto:alice@example.com"},
		[]api.CalendarAttendee{{Token: token, Status: api.CalendarAttendeeStatus(statusDeclined)}},
	)

	if !strings.Contains(got[0], "PARTSTAT=DECLINED") {
		t.Errorf("the reply was not added: %s", got[0])
	}
}

// A parameter value may be quoted and hold the very characters the split
// looks for, so a naive search cuts the line in the wrong place.
func TestSplitLineRespectsQuotedParameters(t *testing.T) {
	line := `ATTENDEE;CN="Doe, Jan: boss";PARTSTAT=ACCEPTED:mailto:jan@example.com`

	prop, params, value := splitLine(line)

	if prop != "ATTENDEE" {
		t.Errorf("name = %q", prop)
	}

	if len(params) != 2 {
		t.Fatalf("params = %q, want two", params)
	}

	if got := param(params, "CN"); got != "Doe, Jan: boss" {
		t.Errorf("CN = %q", got)
	}

	if value != "mailto:jan@example.com" {
		t.Errorf("value = %q", value)
	}

	// And setting a parameter leaves the quoted one intact.
	got := withParam(line, "PARTSTAT", "DECLINED")

	if !strings.Contains(got, `CN="Doe, Jan: boss"`) {
		t.Errorf("the quoted parameter was mangled: %s", got)
	}

	if strings.Contains(got, "ACCEPTED") {
		t.Errorf("the old value is still there: %s", got)
	}
}

package calendar

import (
	"strings"
	"testing"

	api "github.com/ProtonMail/go-proton-api"
	"github.com/emersion/go-ical"
)

const fullEvent = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//carbonate//test//EN
BEGIN:VEVENT
UID:test@carbonate.local
DTSTAMP:20260914T180000Z
DTSTART:20260916T140000Z
DTEND:20260916T150000Z
SUMMARY:Standup
LOCATION:Terminal
DESCRIPTION:Daily
STATUS:CONFIRMED
TRANSP:OPAQUE
X-CUSTOM-THING:keep me
END:VEVENT
END:VCALENDAR
`

func parse(t *testing.T, ics string) *ical.Event {
	t.Helper()

	event, err := parseEvent(ics)
	if err != nil {
		t.Fatalf("parseEvent: %v", err)
	}

	return event
}

// The whole point of the split is that nothing falls between the cards.
func TestSplitLosesNoProperty(t *testing.T) {
	event := parse(t, fullEvent)

	want := make(map[string]bool)
	for name := range event.Props {
		want[strings.ToUpper(name)] = true
	}

	known := append(append(append(append([]string{}, sharedSigned...), sharedEncrypted...), calendarSigned...), calendarEncrypted...)
	extra := unknownProps(event, known)

	got := make(map[string]bool)

	for _, set := range [][]string{sharedSigned, append(sharedEncrypted, extra...), calendarSigned, calendarEncrypted} {
		body := pick(event, set)
		if body == "" {
			continue
		}

		for _, line := range properties(body) {
			got[name(line)] = true
		}
	}

	for prop := range want {
		if !got[prop] {
			t.Errorf("property %q was dropped by the split", prop)
		}
	}
}

// An unrecognised property must be encrypted, not published in a signed card.
func TestUnknownPropertiesAreEncrypted(t *testing.T) {
	event := parse(t, fullEvent)

	known := append(append(append(append([]string{}, sharedSigned...), sharedEncrypted...), calendarSigned...), calendarEncrypted...)

	extra := unknownProps(event, known)
	if len(extra) == 0 {
		t.Fatal("X-CUSTOM-THING was not detected as unknown")
	}

	signed := pick(event, sharedSigned) + pick(event, calendarSigned)
	if strings.Contains(signed, "X-CUSTOM-THING") {
		t.Error("unknown property leaked into a signed, unencrypted card")
	}

	if !strings.Contains(pick(event, append(sharedEncrypted, extra...)), "X-CUSTOM-THING") {
		t.Error("unknown property did not reach the encrypted card")
	}
}

// Proton expects a card carrying nothing but UID and DTSTAMP to be omitted,
// not sent as an empty encrypted blob.
func TestPickOmitsCardWithoutSubstance(t *testing.T) {
	event := parse(t, `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//carbonate//test//EN
BEGIN:VEVENT
UID:test@carbonate.local
DTSTAMP:20260914T180000Z
DTSTART:20260916T140000Z
END:VEVENT
END:VCALENDAR
`)

	// This event has no COMMENT, so the encrypted calendar card would hold
	// only UID and DTSTAMP.
	if body := pick(event, calendarEncrypted); body != "" {
		t.Errorf("empty card was rendered instead of omitted:\n%s", body)
	}

	// The shared signed card does have substance and must survive.
	if body := pick(event, sharedSigned); body == "" {
		t.Error("shared signed card was omitted despite carrying DTSTART")
	}
}

// Every card carries UID and DTSTAMP so it can be understood alone.
func TestPickAlwaysCarriesIdentity(t *testing.T) {
	event := parse(t, fullEvent)

	for label, set := range map[string][]string{
		"shared signed":      sharedSigned,
		"shared encrypted":   sharedEncrypted,
		"calendar signed":    calendarSigned,
		"calendar encrypted": calendarEncrypted,
	} {
		body := pick(event, set)
		if body == "" {
			continue
		}

		for _, prop := range []string{"UID:", "DTSTAMP:"} {
			if !strings.Contains(body, prop) {
				t.Errorf("%s card is missing %s", label, prop)
			}
		}
	}
}

// Proton answers 500 to "SEQUENCE;VALUE=TEXT:0", which is what go-ical's
// SetText would produce.
func TestNormaliseSequenceEmitsBareValue(t *testing.T) {
	event := parse(t, fullEvent)

	if err := normaliseSequence(event, nil); err != nil {
		t.Fatalf("normaliseSequence: %v", err)
	}

	body := pick(event, sharedSigned)

	if !strings.Contains(body, "SEQUENCE:0") {
		t.Errorf("SEQUENCE was not emitted plainly:\n%s", body)
	}

	if strings.Contains(body, "VALUE=TEXT") {
		t.Errorf("SEQUENCE carries a VALUE parameter, which Proton rejects:\n%s", body)
	}
}

func TestNormaliseSequencePreservesExistingValue(t *testing.T) {
	event := parse(t, strings.Replace(fullEvent, "UID:test@carbonate.local", "UID:test@carbonate.local\nSEQUENCE:7", 1))

	if err := normaliseSequence(event, nil); err != nil {
		t.Fatalf("normaliseSequence: %v", err)
	}

	if p := event.Props.Get("SEQUENCE"); p == nil || p.Value != "7" {
		t.Errorf("SEQUENCE = %v, want 7", p)
	}
}

// Proton silently discards an update whose SEQUENCE does not increase, so a
// client that never bumps it would see its edits vanish without an error.
func TestNormaliseSequenceOutrunsStoredValue(t *testing.T) {
	stored := &api.CalendarEvent{
		SharedEvents: []api.CalendarEventPart{{
			Type: api.CalendarEventTypeSigned,
			Data: "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:test@carbonate.local\r\nSEQUENCE:4\r\nEND:VEVENT\r\nEND:VCALENDAR",
		}},
	}

	// The client sends SEQUENCE:0, as many do on every edit.
	event := parse(t, fullEvent)

	if err := normaliseSequence(event, stored); err != nil {
		t.Fatalf("normaliseSequence: %v", err)
	}

	if p := event.Props.Get("SEQUENCE"); p == nil || p.Value != "5" {
		t.Errorf("SEQUENCE = %v, want 5 (stored 4 + 1)", p)
	}
}

// A client that does bump it properly must not be dragged backwards.
func TestNormaliseSequenceKeepsHigherClientValue(t *testing.T) {
	stored := &api.CalendarEvent{
		SharedEvents: []api.CalendarEventPart{{
			Type: api.CalendarEventTypeSigned,
			Data: "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nSEQUENCE:2\r\nEND:VEVENT\r\nEND:VCALENDAR",
		}},
	}

	event := parse(t, strings.Replace(fullEvent, "UID:test@carbonate.local", "UID:test@carbonate.local\nSEQUENCE:9", 1))

	if err := normaliseSequence(event, stored); err != nil {
		t.Fatalf("normaliseSequence: %v", err)
	}

	if p := event.Props.Get("SEQUENCE"); p == nil || p.Value != "9" {
		t.Errorf("SEQUENCE = %v, want 9", p)
	}
}

func TestStoredSequenceDefaultsToZero(t *testing.T) {
	n, err := storedSequence(&api.CalendarEvent{})
	if err != nil {
		t.Fatalf("storedSequence: %v", err)
	}

	if n != 0 {
		t.Errorf("storedSequence with no parts = %d, want 0", n)
	}
}

func TestParseEventRejectsWrongCardinality(t *testing.T) {
	if _, err := parseEvent("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//x//EN\r\nEND:VCALENDAR\r\n"); err == nil {
		t.Error("an iCalendar object with no VEVENT was accepted")
	}

	two := strings.Replace(fullEvent, "END:VCALENDAR", strings.TrimPrefix(strings.TrimSuffix(fullEvent, "END:VCALENDAR\n"), "BEGIN:VCALENDAR\nVERSION:2.0\nPRODID:-//carbonate//test//EN\n")+"END:VCALENDAR", 1)
	if _, err := parseEvent(two); err == nil {
		t.Error("an iCalendar object with two VEVENTs was accepted")
	}
}

func TestParseEventRejectsGarbage(t *testing.T) {
	if _, err := parseEvent("this is not iCalendar"); err == nil {
		t.Error("garbage input was accepted")
	}
}

const meeting = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//carbonate//test//EN
BEGIN:VEVENT
UID:meeting@carbonate.local
DTSTAMP:20260914T220000Z
DTSTART:20260924T140000Z
SUMMARY:Standup
ORGANIZER;CN=Org:mailto:org@example.com
ATTENDEE;CN=Alice;PARTSTAT=NEEDS-ACTION:mailto:alice@example.com
ATTENDEE;CN=Bob;PARTSTAT=ACCEPTED:mailto:BOB@Example.COM
END:VEVENT
END:VCALENDAR
`

// Both ends derive the token from the same inputs, so it must depend on the
// event and the address and nothing else.
func TestAttendeeToken(t *testing.T) {
	a := attendeeToken("event@example.com", "mailto:alice@example.com")

	if len(a) != 40 {
		t.Errorf("token %q is not a 40-character SHA-1 hex digest", a)
	}

	if a != attendeeToken("event@example.com", "alice@example.com") {
		t.Error("the mailto: scheme changed the token")
	}

	if a != attendeeToken("event@example.com", "ALICE@EXAMPLE.COM") {
		t.Error("address case changed the token")
	}

	if a == attendeeToken("other@example.com", "alice@example.com") {
		t.Error("two different events produced the same token for one address")
	}

	if a == attendeeToken("event@example.com", "bob@example.com") {
		t.Error("two different addresses produced the same token")
	}
}

func TestNormaliseAddress(t *testing.T) {
	for in, want := range map[string]string{
		"mailto:Alice@Example.com": "alice@example.com",
		"  bob@example.com  ":      "bob@example.com",
		"MAILTO:x@y.z":             "mailto:x@y.z",
	} {
		if got := normaliseAddress(in); got != want {
			t.Errorf("normaliseAddress(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPartstat(t *testing.T) {
	cases := map[string]int{
		"ACCEPTED":     statusAccepted,
		"DECLINED":     statusDeclined,
		"TENTATIVE":    statusTentative,
		"NEEDS-ACTION": statusNeedsAction,
		"nonsense":     statusNeedsAction,
	}

	for value, want := range cases {
		prop := &ical.Prop{Name: "ATTENDEE", Params: ical.Params{}}
		prop.Params.Set("PARTSTAT", value)

		if got := partstat(prop); got != want {
			t.Errorf("partstat(%q) = %d, want %d", value, got, want)
		}
	}

	// An attendee with no parameters at all has simply not replied.
	if got := partstat(&ical.Prop{Name: "ATTENDEE"}); got != statusNeedsAction {
		t.Errorf("partstat with no params = %d, want %d", got, statusNeedsAction)
	}
}

// Attendees must land in their own encrypted part, never in a signed card
// where the server could read who was invited.
func TestAttendeesAreNotInSignedCards(t *testing.T) {
	event := parse(t, meeting)

	for _, set := range [][]string{sharedSigned, calendarSigned} {
		if body := pick(event, set); strings.Contains(body, "alice@example.com") {
			t.Errorf("an attendee leaked into a signed card:\n%s", body)
		}
	}

	if body := pick(event, attendeeEncrypted); !strings.Contains(body, "alice@example.com") {
		t.Errorf("the attendee part does not contain the attendee:\n%s", body)
	}
}

// ORGANIZER is deliberately in the signed part: Proton needs it, and it is not
// a secret from the people invited.
func TestOrganizerStaysInTheSharedSignedCard(t *testing.T) {
	event := parse(t, meeting)

	if body := pick(event, sharedSigned); !strings.Contains(body, "ORGANIZER") {
		t.Errorf("ORGANIZER is missing from the shared signed card:\n%s", body)
	}
}

// ATTENDEE must not also be treated as an unknown property, or every attendee
// would be duplicated into the shared encrypted card as well.
func TestAttendeeIsNotTreatedAsUnknown(t *testing.T) {
	event := parse(t, meeting)

	known := append(append(append(append(append([]string{}, sharedSigned...), sharedEncrypted...), calendarSigned...), calendarEncrypted...), attendeeEncrypted...)

	for _, name := range unknownProps(event, known) {
		if name == "ATTENDEE" {
			t.Error("ATTENDEE was classified as an unknown property")
		}
	}
}

package calendar

import (
	"context"
	"encoding/base64"
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
		body := pick(event, set, false)
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

	signed := pick(event, sharedSigned, false) + pick(event, calendarSigned, false)
	if strings.Contains(signed, "X-CUSTOM-THING") {
		t.Error("unknown property leaked into a signed, unencrypted card")
	}

	if !strings.Contains(pick(event, append(sharedEncrypted, extra...), false), "X-CUSTOM-THING") {
		t.Error("unknown property did not reach the encrypted card")
	}
}

const coloredEvent = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//carbonate//test//EN
BEGIN:VEVENT
UID:colored@carbonate.local
DTSTAMP:20260914T180000Z
DTSTART:20260916T140000Z
DTEND:20260916T150000Z
SUMMARY:Painted
COLOR:#EC3E7C
END:VEVENT
END:VCALENDAR
`

// A palette colour a client names is the colour the event is written with.
func TestColorOfReadsPaletteColor(t *testing.T) {
	if got := colorOf(parse(t, coloredEvent)); got != "#EC3E7C" {
		t.Errorf("colorOf = %q, want #EC3E7C", got)
	}
}

// A colour off the palette is moved to the nearest one Proton offers,
// rather than sent as it is and refused — or worse, stored nowhere.
func TestColorOfMovesOffPaletteColorToNearest(t *testing.T) {
	event := parse(t, strings.Replace(coloredEvent, "#EC3E7C", "#FF0000", 1))

	if got := colorOf(event); got != "#C44800" {
		t.Errorf("colorOf = %q, want #C44800, the palette's nearest to red", got)
	}
}

// A colour named rather than spelled in hex — which is what RFC 7986 asks
// for — is one we cannot place on the palette. It is left out of the write
// rather than guessed at, so an event coloured in Proton's own apps keeps
// the colour it has.
func TestColorOfIgnoresAColorItCannotRead(t *testing.T) {
	event := parse(t, strings.Replace(coloredEvent, "#EC3E7C", "turquoise", 1))

	if got := colorOf(event); got != "" {
		t.Errorf("colorOf = %q, want \"\"", got)
	}
}

// No colour named means no colour written: the field is left out of the
// write, and an update keeps the colour the event already has.
func TestColorOfUncoloredEvent(t *testing.T) {
	if got := colorOf(parse(t, fullEvent)); got != "" {
		t.Errorf("colorOf = %q, want \"\"", got)
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
	if body := pick(event, calendarEncrypted, false); body != "" {
		t.Errorf("empty card was rendered instead of omitted:\n%s", body)
	}

	// The shared signed card does have substance and must survive.
	if body := pick(event, sharedSigned, false); body == "" {
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
		body := pick(event, set, false)
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

	body := pick(event, sharedSigned, false)

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
	stored := &rawEvent{CalendarEvent: api.CalendarEvent{
		SharedEvents: []api.CalendarEventPart{{
			Type: api.CalendarEventTypeSigned,
			Data: "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:test@carbonate.local\r\nSEQUENCE:4\r\nEND:VEVENT\r\nEND:VCALENDAR",
		}},
	}}

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
	stored := &rawEvent{CalendarEvent: api.CalendarEvent{
		SharedEvents: []api.CalendarEventPart{{
			Type: api.CalendarEventTypeSigned,
			Data: "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nSEQUENCE:2\r\nEND:VEVENT\r\nEND:VCALENDAR",
		}},
	}}

	event := parse(t, strings.Replace(fullEvent, "UID:test@carbonate.local", "UID:test@carbonate.local\nSEQUENCE:9", 1))

	if err := normaliseSequence(event, stored); err != nil {
		t.Fatalf("normaliseSequence: %v", err)
	}

	if p := event.Props.Get("SEQUENCE"); p == nil || p.Value != "9" {
		t.Errorf("SEQUENCE = %v, want 9", p)
	}
}

func TestStoredSequenceDefaultsToZero(t *testing.T) {
	n, err := storedSequence(&rawEvent{})
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

		// A URI scheme is case-insensitive, and clients do shout it. This
		// used to come back with the scheme still attached, which then
		// travelled into the attendee's token and into the addresses Proton
		// is asked for keys.
		"MAILTO:x@y.z": "x@y.z",
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
		if body := pick(event, set, false); strings.Contains(body, "alice@example.com") {
			t.Errorf("an attendee leaked into a signed card:\n%s", body)
		}
	}

	if body := pick(event, attendeeEncrypted, false); !strings.Contains(body, "alice@example.com") {
		t.Errorf("the attendee part does not contain the attendee:\n%s", body)
	}
}

// ORGANIZER is deliberately in the signed part: Proton needs it, and it is not
// a secret from the people invited.
func TestOrganizerStaysInTheSharedSignedCard(t *testing.T) {
	event := parse(t, meeting)

	if body := pick(event, sharedSigned, false); !strings.Contains(body, "ORGANIZER") {
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

// A bare invitation: everything in it belongs to a signed card, so the
// shared encrypted card would hold nothing but UID and DTSTAMP.
const bareInvitation = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//carbonate//test//EN
BEGIN:VEVENT
UID:bare@carbonate.local
DTSTAMP:20260914T180000Z
DTSTART:20260916T140000Z
DTEND:20260916T150000Z
SEQUENCE:0
STATUS:CONFIRMED
TRANSP:OPAQUE
ORGANIZER:mailto:me@example.com
ATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:alice@example.com
END:VEVENT
END:VCALENDAR
`

// The shared part is sent whole even when there is nothing to put in it.
//
// Dropping the empty encrypted card used to drop the shared key packet with
// it, and the guest list is encrypted under exactly that key: the event went
// to Proton with a card nobody — the web app, another client, or carbonate
// on the next read — could ever open again.
func TestBareInvitationKeepsItsGuestListReadable(t *testing.T) {
	keys := calendarKeys(t)

	data, err := buildEvent(context.Background(), parse(t, bareInvitation), keys, nil, nil)
	if err != nil {
		t.Fatalf("buildEvent: %v", err)
	}

	if data.SharedKeyPacket == "" {
		t.Fatal("no shared key packet was sent, so nothing can open the attendee card")
	}

	if len(data.SharedEventContent) != 2 {
		t.Errorf("sent %d shared cards, want both the signed and the encrypted one", len(data.SharedEventContent))
	}

	if len(data.AttendeesEventContent) != 1 {
		t.Fatalf("sent %d attendee cards, want 1", len(data.AttendeesEventContent))
	}

	packet, err := base64.StdEncoding.DecodeString(data.SharedKeyPacket)
	if err != nil {
		t.Fatalf("decoding the shared key packet: %v", err)
	}

	sessionKey, err := keys.CalKR.DecryptSessionKey(packet)
	if err != nil {
		t.Fatalf("the shared key packet does not open with the calendar key: %v", err)
	}

	body, err := base64.StdEncoding.DecodeString(data.AttendeesEventContent[0].Data)
	if err != nil {
		t.Fatalf("decoding the attendee card: %v", err)
	}

	guests, err := sessionKey.Decrypt(body)
	if err != nil {
		t.Fatalf("the attendee card does not open with the shared session key: %v", err)
	}

	if !strings.Contains(guests.GetString(), "alice@example.com") {
		t.Errorf("the attendee card does not hold the guest:\n%s", guests.GetString())
	}
}

// Proton folds an address before hashing it into a token, and carbonate has
// to fold it the same way or the two name the same guest differently.
func TestCanonicalAddressFollowsProton(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"mailto:John.Doe@Example.com", "john.doe@example.com"},
		{"MAILTO:Jan@Example.com", "jan@example.com"},
		{"John.Doe+cal@proton.me", "johndoe@proton.me"},
		{"j_a-n@pm.me", "jan@pm.me"},
		{"John.Doe+cal@gmail.com", "johndoe@gmail.com"},
		{"John.Doe@googlemail.com", "johndoe@googlemail.com"},

		// A dot means something outside the domains Proton folds.
		{"john.doe+cal@fastmail.com", "john.doe+cal@fastmail.com"},

		// Not an address at all; left as it came.
		{"someone", "someone"},
	} {
		if got := canonicalAddress(tc.in); got != tc.want {
			t.Errorf("canonicalAddress(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Two spellings of one person must reach one token, because the token is the
// only name Proton files their replies under.
func TestTokenIgnoresSpelling(t *testing.T) {
	uid := "event@carbonate.local"

	if attendeeToken(uid, "John.Doe+cal@proton.me") != attendeeToken(uid, "johndoe@proton.me") {
		t.Error("two spellings of one Proton address produced different tokens")
	}

	if attendeeToken(uid, "a@example.com") == attendeeToken(uid, "b@example.com") {
		t.Error("two different addresses produced the same token")
	}
}

// A token already on the attendee is Proton's, and is kept.
//
// Recomputing it would orphan every reply filed under the old one, and
// carbonate cannot always rederive the address Proton made it from.
func TestExistingAttendeeTokenIsKept(t *testing.T) {
	keys := calendarKeys(t)

	const theirs = "0123456789abcdef0123456789abcdef01234567"

	ics := strings.Replace(
		bareInvitation,
		"ATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:alice@example.com",
		"ATTENDEE;PARTSTAT=ACCEPTED;X-PM-TOKEN="+theirs+":mailto:alice@example.com",
		1,
	)

	data, err := buildEvent(context.Background(), parse(t, ics), keys, nil, nil)
	if err != nil {
		t.Fatalf("buildEvent: %v", err)
	}

	if len(data.Attendees) != 1 {
		t.Fatalf("sent %d attendees, want 1", len(data.Attendees))
	}

	if data.Attendees[0].Token != theirs {
		t.Errorf("token = %q, want the one the event already carried, %q", data.Attendees[0].Token, theirs)
	}

	if data.Attendees[0].Status != statusAccepted {
		t.Errorf("status = %d, want the reply the event carried", data.Attendees[0].Status)
	}
}

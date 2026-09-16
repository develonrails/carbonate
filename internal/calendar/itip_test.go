package calendar

import (
	"strings"
	"testing"

	api "github.com/ProtonMail/go-proton-api"
)

// METHOD is what makes the difference between a copy of an event and a request
// to attend one. Without it a mail client files the attachment and does
// nothing.
func TestInvitationCarriesTheMethod(t *testing.T) {
	body, err := itip(parse(t, invitation), methodRequest, "me@example.com")
	if err != nil {
		t.Fatalf("itip: %v", err)
	}

	if !strings.Contains(body, "METHOD:REQUEST") {
		t.Errorf("the invitation does not name its method:\n%s", body)
	}

	if !strings.Contains(body, "Guest@Proton.me") {
		t.Errorf("the invitation does not name the guest:\n%s", body)
	}
}

// A cancellation that still reads as an invitation leaves the guest holding
// something they have already been told to ignore.
func TestCancellationSaysTheEventIsOff(t *testing.T) {
	body, err := itip(parse(t, invitation), methodCancel, "me@example.com")
	if err != nil {
		t.Fatalf("itip: %v", err)
	}

	if !strings.Contains(body, "METHOD:CANCEL") {
		t.Errorf("the cancellation does not name its method:\n%s", body)
	}

	if !strings.Contains(body, "STATUS:CANCELLED") {
		t.Errorf("the cancellation does not mark the event cancelled:\n%s", body)
	}
}

// The token is Proton's way of tracking a reply and means nothing to anyone
// else. Sending it out names our storage in someone else's mail client.
func TestInvitationDoesNotLeakTheProtonToken(t *testing.T) {
	event := parse(t, invitation)

	if _, err := buildAttendees(event, calendarKeys(t), "", ""); err != nil {
		t.Fatalf("buildAttendees: %v", err)
	}

	body, err := itip(event, methodRequest, "me@example.com")
	if err != nil {
		t.Fatalf("itip: %v", err)
	}

	if strings.Contains(body, "X-PM-TOKEN") {
		t.Errorf("the invitation carries Proton's attendee token:\n%s", body)
	}
}

// Proton refuses an event with attendees and no organiser, and a guest has no
// one to reply to either.
func TestInvitationAlwaysNamesAnOrganiser(t *testing.T) {
	const noOrganiser = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//carbonate//test//EN
BEGIN:VEVENT
UID:invite@carbonate.local
DTSTAMP:20260914T180000Z
DTSTART:20260916T140000Z
SUMMARY:Standup
ATTENDEE:mailto:guest@proton.me
END:VEVENT
END:VCALENDAR
`

	body, err := itip(parse(t, noOrganiser), methodRequest, "me@example.com")
	if err != nil {
		t.Fatalf("itip: %v", err)
	}

	if !strings.Contains(body, "ORGANIZER:mailto:me@example.com") {
		t.Errorf("the invitation names no organiser:\n%s", body)
	}
}

// A CalDAV client re-PUTs an unchanged event as a matter of course. Mailing
// everyone each time would make carbonate a nuisance to people who never
// installed it.
func TestUnchangedGuestListMailsNobody(t *testing.T) {
	event := parse(t, invitation)

	existing := &rawEvent{CalendarEvent: api.CalendarEvent{
		UID:       "invite@carbonate.local",
		Attendees: []api.CalendarAttendee{{Token: attendeeToken("invite@carbonate.local", "mailto:guest@proton.me")}},
	}}

	if got := newGuests(event, existing, "me@example.com"); len(got) != 0 {
		t.Errorf("would mail %v on a re-PUT of an unchanged guest list, want nobody", got)
	}
}

func TestNewlyInvitedGuestIsMailed(t *testing.T) {
	got := newGuests(parse(t, invitation), nil, "me@example.com")

	if len(got) != 1 || got[0] != guestAddress {
		t.Errorf("would mail %v, want [%s]", got, guestAddress)
	}
}

// Clients routinely list the organiser among the attendees.
func TestOrganiserIsNotMailedTheirOwnInvitation(t *testing.T) {
	const selfInvited = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//carbonate//test//EN
BEGIN:VEVENT
UID:invite@carbonate.local
DTSTAMP:20260914T180000Z
DTSTART:20260916T140000Z
SUMMARY:Standup
ORGANIZER:mailto:me@example.com
ATTENDEE:mailto:Me@Example.com
ATTENDEE:mailto:Guest@Proton.me
END:VEVENT
END:VCALENDAR
`

	got := newGuests(parse(t, selfInvited), nil, "me@example.com")

	if len(got) != 1 || got[0] != guestAddress {
		t.Errorf("would mail %v, want only [%s]", got, guestAddress)
	}
}

// An untitled event is legal, and still has to be announced as something.
func TestUntitledEventStillHasASubject(t *testing.T) {
	const untitled = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//carbonate//test//EN
BEGIN:VEVENT
UID:invite@carbonate.local
DTSTAMP:20260914T180000Z
DTSTART:20260916T140000Z
ATTENDEE:mailto:guest@proton.me
END:VEVENT
END:VCALENDAR
`

	event := parse(t, untitled)

	if got := subject(event, methodRequest); got != "Invitation: (no title)" {
		t.Errorf("subject = %q", got)
	}

	if got := subject(event, methodCancel); got != "Cancelled: (no title)" {
		t.Errorf("subject = %q", got)
	}
}

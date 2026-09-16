package calendar

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	api "github.com/ProtonMail/go-proton-api"
	"github.com/ProtonMail/gopenpgp/v2/crypto"

	"github.com/develonrails/carbonate/internal/proton"
)

const invitation = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//carbonate//test//EN
BEGIN:VEVENT
UID:invite@carbonate.local
DTSTAMP:20260914T180000Z
DTSTART:20260916T140000Z
DTEND:20260916T150000Z
SUMMARY:Standup
ORGANIZER:mailto:me@example.com
ATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:Guest@Proton.me
END:VEVENT
END:VCALENDAR
`

const guestAddress = "guest@proton.me"

// calendarKeys stands in for the keys Proton hands out for a calendar we are a
// member of.
func calendarKeys(t *testing.T) *proton.CalendarKeys {
	t.Helper()

	calKR, addrKR := keys(t)

	return &proton.CalendarKeys{MemberID: "member-id", Email: "me@example.com", CalKR: calKR, AddrKR: addrKR}
}

// guestKeys generates a guest's key twice over: the public keyring Proton
// would hand out for their address, and the private one only they hold.
func guestKeys(t *testing.T) (public, private *crypto.KeyRing) {
	t.Helper()

	key, err := crypto.GenerateKey("Guest", guestAddress, "x25519", 0)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	pub, err := key.ToPublic()
	if err != nil {
		t.Fatalf("ToPublic: %v", err)
	}

	if public, err = crypto.NewKeyRing(pub); err != nil {
		t.Fatalf("NewKeyRing: %v", err)
	}

	if private, err = crypto.NewKeyRing(key); err != nil {
		t.Fatalf("NewKeyRing: %v", err)
	}

	return public, private
}

// found is a lookup that answers with the same keyring for every address, as
// Proton does for an account it holds keys for.
func found(kr *crypto.KeyRing) addressKeys {
	return func(context.Context, string) (*crypto.KeyRing, error) {
		return kr, nil
	}
}

// build renders an event the way putOne does, minus the connection.
func build(t *testing.T, ics string, keys *proton.CalendarKeys, existing *rawEvent, lookup addressKeys) *eventData {
	t.Helper()

	data, err := buildEvent(context.Background(), parse(t, ics), keys, existing, lookup)
	if err != nil {
		t.Fatalf("buildEvent: %v", err)
	}

	return data
}

// The whole point of the key packet: without it a guest on Proton is on the
// list and still cannot read what they were invited to.
func TestProtonGuestCanOpenTheEvent(t *testing.T) {
	ck := calendarKeys(t)
	public, private := guestKeys(t)

	data := build(t, invitation, ck, nil, found(public))

	if len(data.AddedProtonAttendees) != 1 {
		t.Fatalf("got %d key packets for guests, want 1", len(data.AddedProtonAttendees))
	}

	if got := data.AddedProtonAttendees[0].Email; got != guestAddress {
		t.Errorf("addressed the key packet to %q, want %q", got, guestAddress)
	}

	packet, err := base64.StdEncoding.DecodeString(data.AddedProtonAttendees[0].AddressKeyPacket)
	if err != nil {
		t.Fatalf("decoding the key packet: %v", err)
	}

	sessionKey, err := private.DecryptSessionKey(packet)
	if err != nil {
		t.Fatalf("the guest cannot open their own key packet: %v", err)
	}

	if len(data.AttendeesEventContent) != 1 {
		t.Fatalf("got %d attendee cards, want 1", len(data.AttendeesEventContent))
	}

	body, err := base64.StdEncoding.DecodeString(data.AttendeesEventContent[0].Data)
	if err != nil {
		t.Fatalf("decoding the attendee card: %v", err)
	}

	plain, err := sessionKey.Decrypt(body)
	if err != nil {
		t.Fatalf("the key packet does not open the attendee card: %v", err)
	}

	if !strings.Contains(plain.GetString(), "Guest@Proton.me") {
		t.Errorf("the attendee card does not name the guest:\n%s", plain.GetString())
	}
}

// There is no key of an outsider's to wrap the session key to, and inventing
// one is not an option — they simply get no packet.
func TestGuestOutsideProtonGetsNoKeyPacket(t *testing.T) {
	ck := calendarKeys(t)

	data := build(t, invitation, ck, nil, found(nil))

	if len(data.AddedProtonAttendees) != 0 {
		t.Errorf("sent %d key packets for a guest Proton has no keys for, want none", len(data.AddedProtonAttendees))
	}

	if len(data.Attendees) != 1 {
		t.Errorf("got %d attendees in the clear list, want 1 — the guest is still invited", len(data.Attendees))
	}
}

// Re-wrapping the key for someone who already holds it is a round trip that
// buys nothing.
func TestAlreadyInvitedGuestIsNotSentTheKeyAgain(t *testing.T) {
	ck := calendarKeys(t)
	public, _ := guestKeys(t)

	existing := storedGuests(t, ck.CalKR, ck.AddrKR, "Guest@Proton.me")

	data := build(t, invitation, ck, existing, found(public))

	if data.SharedKeyPacket != "" {
		t.Fatal("expected the stored key packet to be reused, so that the unchanged-key case is the one under test")
	}

	if len(data.AddedProtonAttendees) != 0 {
		t.Errorf("sent %d key packets to a guest who was already invited, want none", len(data.AddedProtonAttendees))
	}
}

// An address Proton will not answer for is a reason to leave one guest in the
// dark, not to refuse the whole event.
func TestFailedLookupStillWritesTheEvent(t *testing.T) {
	ck := calendarKeys(t)

	data := build(t, invitation, ck, nil, func(context.Context, string) (*crypto.KeyRing, error) {
		return nil, errors.New("no such address")
	})

	if len(data.AddedProtonAttendees) != 0 {
		t.Errorf("sent %d key packets despite the lookup failing, want none", len(data.AddedProtonAttendees))
	}

	if len(data.Attendees) != 1 {
		t.Errorf("got %d attendees in the clear list, want 1 — the guest is still invited", len(data.Attendees))
	}
}

// storedGuests renders an event Proton already holds: the guest list encrypted
// where Proton keeps it, the tokens Proton keeps in the clear beside it, and
// the shared key packet both are read through.
func storedGuests(t *testing.T, calKR, addrKR *crypto.KeyRing, addresses ...string) *rawEvent {
	t.Helper()

	body := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:invite@carbonate.local\r\nDTSTAMP:20260914T180000Z\r\n"
	clear := make([]api.CalendarAttendee, 0, len(addresses))

	for _, address := range addresses {
		body += "ATTENDEE:mailto:" + address + "\r\n"
		clear = append(clear, api.CalendarAttendee{Token: attendeeToken("invite@carbonate.local", address)})
	}

	body += "END:VEVENT\r\nEND:VCALENDAR"

	part, keyPacket := encryptPart(t, calKR, addrKR, body)

	return &rawEvent{CalendarEvent: api.CalendarEvent{
		ID:              "event-id",
		UID:             "invite@carbonate.local",
		Attendees:       clear,
		SharedKeyPacket: keyPacket,
		AttendeesEvents: []api.CalendarEventPart{part},
	}}
}

// Proton wants an address to tell someone they are off the list, and the clear
// list beside the event holds only tokens — so the stored guest list has to be
// read back to find out who is leaving.
func TestDroppedGuestIsNamed(t *testing.T) {
	ck := calendarKeys(t)

	existing := storedGuests(t, ck.CalKR, ck.AddrKR, "Guest@Proton.me", "gone@example.com")

	data := build(t, invitation, ck, existing, found(nil))

	want := []string{"gone@example.com"}

	if len(data.RemovedAttendeeAddresses) != len(want) || data.RemovedAttendeeAddresses[0] != want[0] {
		t.Errorf("named %v as removed, want %v", data.RemovedAttendeeAddresses, want)
	}
}

// A guest list carbonate cannot read is a guest nobody is told about, which is
// the behaviour carbonate has always had. Refusing the write would be worse.
func TestUnreadableStoredGuestListStillWritesTheEvent(t *testing.T) {
	ck := calendarKeys(t)

	// Signed by an address key that is not ours, so verification fails.
	_, stranger := keys(t)
	existing := storedGuests(t, ck.CalKR, stranger, "gone@example.com")

	data := build(t, invitation, ck, existing, found(nil))

	if len(data.RemovedAttendeeAddresses) != 0 {
		t.Errorf("named %v as removed from a guest list it could not read, want none", data.RemovedAttendeeAddresses)
	}
}

// An event that never had a guest list has nobody to remove.
func TestEventWithoutStoredGuestsRemovesNobody(t *testing.T) {
	ck := calendarKeys(t)
	public, _ := guestKeys(t)

	data := build(t, invitation, ck, nil, found(public))

	if len(data.RemovedAttendeeAddresses) != 0 {
		t.Errorf("named %v as removed on a newly created event, want none", data.RemovedAttendeeAddresses)
	}
}

// An update that mints a fresh session key leaves every guest holding a packet
// that opens nothing, so they all need the new one — being on the list already
// is no help.
func TestReKeyedEventIsSharedWithEveryGuestAgain(t *testing.T) {
	ck := calendarKeys(t)
	public, _ := guestKeys(t)

	// No stored shared key packet, so the write has to mint one.
	existing := &rawEvent{CalendarEvent: api.CalendarEvent{
		ID:        "event-id",
		UID:       "invite@carbonate.local",
		Attendees: []api.CalendarAttendee{{Token: attendeeToken("invite@carbonate.local", "mailto:guest@proton.me")}},
	}}

	data := build(t, invitation, ck, existing, found(public))

	if data.SharedKeyPacket == "" {
		t.Fatal("expected the write to mint a shared key packet, so that the re-key case is the one under test")
	}

	if len(data.AddedProtonAttendees) != 1 {
		t.Errorf("got %d key packets after re-keying, want 1 — the guest cannot read the event without one", len(data.AddedProtonAttendees))
	}
}

// Clients routinely list the organiser among the attendees. We already open
// the event through the calendar key, so a packet addressed to ourselves is
// noise at best.
func TestOrganiserIsNotSentAKeyPacket(t *testing.T) {
	ck := calendarKeys(t)
	public, _ := guestKeys(t)

	const selfInvited = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//carbonate//test//EN
BEGIN:VEVENT
UID:invite@carbonate.local
DTSTAMP:20260914T180000Z
DTSTART:20260916T140000Z
DTEND:20260916T150000Z
SUMMARY:Standup
ORGANIZER:mailto:me@example.com
ATTENDEE;PARTSTAT=ACCEPTED:mailto:Me@Example.com
ATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:Guest@Proton.me
END:VEVENT
END:VCALENDAR
`

	data := build(t, selfInvited, ck, nil, found(public))

	if len(data.Attendees) != 2 {
		t.Fatalf("got %d attendees in the clear list, want 2", len(data.Attendees))
	}

	if len(data.AddedProtonAttendees) != 1 {
		t.Fatalf("got %d key packets, want 1 — only the guest needs one", len(data.AddedProtonAttendees))
	}

	if got := data.AddedProtonAttendees[0].Email; got != guestAddress {
		t.Errorf("addressed the key packet to %q, want %q", got, guestAddress)
	}
}

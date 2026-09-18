package calendar

import (
	"encoding/base64"
	"strings"
	"testing"

	api "github.com/ProtonMail/go-proton-api"
	"github.com/ProtonMail/gopenpgp/v2/crypto"
)

// keys builds a calendar keyring and an address keyring, standing in for the
// ones Proton hands out.
func keys(t *testing.T) (calKR, addrKR *crypto.KeyRing) {
	t.Helper()

	make := func(name string) *crypto.KeyRing {
		key, err := crypto.GenerateKey(name, name+"@example.com", "x25519", 0)
		if err != nil {
			t.Fatalf("GenerateKey: %v", err)
		}

		kr, err := crypto.NewKeyRing(key)
		if err != nil {
			t.Fatalf("NewKeyRing: %v", err)
		}

		return kr
	}

	return make("calendar"), make("address")
}

// encryptedPart renders a part the way Proton stores it: the data packet
// encrypted under a session key, signed over the plaintext.
func encryptPart(t *testing.T, calKR, addrKR *crypto.KeyRing, body string) (api.CalendarEventPart, string) {
	t.Helper()

	sessionKey, err := crypto.GenerateSessionKey()
	if err != nil {
		t.Fatalf("GenerateSessionKey: %v", err)
	}

	keyPacket, err := calKR.EncryptSessionKey(sessionKey)
	if err != nil {
		t.Fatalf("EncryptSessionKey: %v", err)
	}

	data, err := sessionKey.Encrypt(crypto.NewPlainMessageFromString(body))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	sig, err := addrKR.SignDetached(crypto.NewPlainMessageFromString(body))
	if err != nil {
		t.Fatalf("SignDetached: %v", err)
	}

	armored, err := sig.GetArmored()
	if err != nil {
		t.Fatalf("GetArmored: %v", err)
	}

	part := api.CalendarEventPart{
		Type:      api.CalendarEventTypeEncrypted | api.CalendarEventTypeSigned,
		Data:      base64.StdEncoding.EncodeToString(data),
		Signature: armored,
	}

	return part, base64.StdEncoding.EncodeToString(keyPacket)
}

func signedPart(t *testing.T, addrKR *crypto.KeyRing, body string) api.CalendarEventPart {
	t.Helper()

	sig, err := addrKR.SignDetached(crypto.NewPlainMessageFromString(body))
	if err != nil {
		t.Fatalf("SignDetached: %v", err)
	}

	armored, err := sig.GetArmored()
	if err != nil {
		t.Fatalf("GetArmored: %v", err)
	}

	return api.CalendarEventPart{
		Type:      api.CalendarEventTypeSigned,
		Data:      body,
		Signature: armored,
	}
}

// The read path is the mirror of the write path, so the properties Proton
// splits apart must come back as one event.
func TestDecodeReassemblesTheParts(t *testing.T) {
	calKR, addrKR := keys(t)

	shared := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:abc\r\nDTSTAMP:20260914T120000Z\r\nDTSTART:20260920T090000Z\r\nEND:VEVENT\r\nEND:VCALENDAR"
	secret := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:abc\r\nDTSTAMP:20260914T120000Z\r\nSUMMARY:Private title\r\nEND:VEVENT\r\nEND:VCALENDAR"

	encrypted, keyPacket := encryptPart(t, calKR, addrKR, secret)

	raw := rawEvent{CalendarEvent: api.CalendarEvent{
		ID:              "event-id",
		UID:             "abc",
		StartTime:       1790000000,
		EndTime:         1790003600,
		LastEditTime:    1789000000,
		SharedKeyPacket: keyPacket,
		SharedEvents:    []api.CalendarEventPart{signedPart(t, addrKR, shared), encrypted},
	}}

	event, err := decode(raw, calKR, addrKR)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	ics := event.ICS()

	for _, want := range []string{"UID:abc", "DTSTART:20260920T090000Z", "SUMMARY:Private title"} {
		if !strings.Contains(ics, want) {
			t.Errorf("reassembled event is missing %q:\n%s", want, ics)
		}
	}

	if event.Summary() != "Private title" {
		t.Errorf("Summary() = %q", event.Summary())
	}

	if event.Modified.Unix() != 1789000000 {
		t.Errorf("Modified = %v, want the LastEditTime", event.Modified)
	}
}

// A part signed by someone other than the calendar member is served, and
// flagged.
//
// carbonate holds one address's keys — ours — so a signature made by anyone
// else cannot be checked here at all. On a shared calendar that is every event
// another member added, and refusing those would hide events the web app
// shows. Worse, refusal used to fail the whole read, so one such event emptied
// the entire calendar in the client. The event is served, and says it could
// not be attributed.
func TestDecodeFlagsASignatureItCannotVerify(t *testing.T) {
	calKR, addrKR := keys(t)
	_, otherKR := keys(t)

	body := "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:abc\r\nSUMMARY:Theirs\r\nEND:VEVENT\r\nEND:VCALENDAR"

	raw := rawEvent{CalendarEvent: api.CalendarEvent{
		ID:           "event-id",
		UID:          "abc",
		SharedEvents: []api.CalendarEventPart{signedPart(t, otherKR, body)},
	}}

	event, err := decode(raw, calKR, addrKR)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if !event.Unverified {
		t.Error("an event signed by a key we do not hold was not flagged as unverified")
	}

	if event.Summary() != "Theirs" {
		t.Errorf("Summary() = %q, want the event to be served anyway", event.Summary())
	}
}

// Our own signature still verifies, or the flag above would mean nothing.
func TestDecodeAcceptsOurOwnSignature(t *testing.T) {
	calKR, addrKR := keys(t)

	body := "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:abc\r\nSUMMARY:Ours\r\nEND:VEVENT\r\nEND:VCALENDAR"

	raw := rawEvent{CalendarEvent: api.CalendarEvent{
		ID:           "event-id",
		UID:          "abc",
		SharedEvents: []api.CalendarEventPart{signedPart(t, addrKR, body)},
	}}

	event, err := decode(raw, calKR, addrKR)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if event.Unverified {
		t.Error("an event signed with our own address key was flagged as unverified")
	}
}

// Without the right calendar key the data is unreadable, and that must be an
// error rather than an event that silently lost its title.
func TestDecodeRejectsTheWrongCalendarKey(t *testing.T) {
	calKR, addrKR := keys(t)
	otherCalKR, _ := keys(t)

	body := "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:abc\r\nSUMMARY:x\r\nEND:VEVENT\r\nEND:VCALENDAR"
	encrypted, keyPacket := encryptPart(t, calKR, addrKR, body)

	raw := rawEvent{CalendarEvent: api.CalendarEvent{
		ID:              "event-id",
		UID:             "abc",
		SharedKeyPacket: keyPacket,
		SharedEvents:    []api.CalendarEventPart{encrypted},
	}}

	if _, err := decode(raw, otherCalKR, addrKR); err == nil {
		t.Error("an event was decoded with the wrong calendar key")
	}
}

// Proton reports a full-day event's bounds in UTC; the flag must survive so
// the renderer does not invent a local time.
func TestDecodeCarriesTheFullDayFlag(t *testing.T) {
	calKR, addrKR := keys(t)

	raw := rawEvent{CalendarEvent: api.CalendarEvent{
		ID:        "event-id",
		UID:       "abc",
		FullDay:   true,
		StartTime: 1789430400,
	}}

	event, err := decode(raw, calKR, addrKR)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if !event.FullDay {
		t.Error("FullDay was lost")
	}

	if !strings.Contains(event.When(), "all day") {
		t.Errorf("When() = %q", event.When())
	}
}

func TestDecodeCountsAttendees(t *testing.T) {
	calKR, addrKR := keys(t)

	raw := rawEvent{CalendarEvent: api.CalendarEvent{
		ID:        "event-id",
		UID:       "abc",
		Attendees: []api.CalendarAttendee{{Token: "a"}, {Token: "b"}},
	}}

	event, err := decode(raw, calKR, addrKR)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if event.Attendees != 2 {
		t.Errorf("Attendees = %d, want 2", event.Attendees)
	}
}

// One event that cannot be decrypted must cost that one event, and nothing
// else.
//
// This is the whole of issue #18 in one test. Reading a calendar used to stop
// at the first event it could not decode, so every CalDAV listing of that
// collection answered 500, and GNOME Calendar — which shows an empty calendar
// rather than an error — looked exactly as though Proton had sent nothing.
// The calendars with no such event downsynced fine, which is what made it look
// like a sync bug rather than one bad event.
func TestDecodeAllSkipsOnlyTheUnreadableEvent(t *testing.T) {
	calKR, addrKR := keys(t)
	otherCalKR, _ := keys(t)

	body := func(uid string) string {
		return "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:" + uid + "\r\nSUMMARY:" + uid + "\r\nEND:VEVENT\r\nEND:VCALENDAR"
	}

	// Encrypted to a calendar key we do not have: unreadable, for real.
	sealed, otherKeyPacket := encryptPart(t, otherCalKR, addrKR, body("locked"))

	raw := []rawEvent{
		{CalendarEvent: api.CalendarEvent{
			ID:           "first",
			UID:          "first",
			SharedEvents: []api.CalendarEventPart{signedPart(t, addrKR, body("first"))},
		}},
		{CalendarEvent: api.CalendarEvent{
			ID:              "locked",
			UID:             "locked",
			SharedKeyPacket: otherKeyPacket,
			SharedEvents:    []api.CalendarEventPart{sealed},
		}},
		{CalendarEvent: api.CalendarEvent{
			ID:           "last",
			UID:          "last",
			SharedEvents: []api.CalendarEventPart{signedPart(t, addrKR, body("last"))},
		}},
	}

	events, unreadable := decodeAll(raw, calKR, addrKR)

	if len(events) != 2 {
		t.Fatalf("decoded %d events, want the 2 that could be read", len(events))
	}

	if events[0].UID != "first" || events[1].UID != "last" {
		t.Errorf("decoded %q and %q, want first and last", events[0].UID, events[1].UID)
	}

	if len(unreadable) != 1 || unreadable[0] != "locked" {
		t.Errorf("unreadable = %v, want [locked]", unreadable)
	}
}

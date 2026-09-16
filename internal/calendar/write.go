package calendar

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	api "github.com/ProtonMail/go-proton-api"
	"github.com/ProtonMail/gopenpgp/v2/crypto"
	"github.com/emersion/go-ical"

	"github.com/develonrails/carbonate/internal/proton"
)

// Card types, as Proton numbers them.
const (
	cardSigned             = 2
	cardEncryptedAndSigned = 3
)

// Which iCalendar property belongs in which card.
//
// Proton splits an event so that attendees can read the scheduling skeleton
// without being able to read its content. The shared part travels with the
// event; the calendar part is the organiser's own view.
//
// Taken from protoxide (MIT), which derived it from Proton's web client.
var (
	sharedSigned    = []string{"UID", "DTSTAMP", "DTSTART", "DTEND", "RECURRENCE-ID", "RRULE", "EXDATE", "ORGANIZER", "SEQUENCE"}
	sharedEncrypted = []string{"UID", "DTSTAMP", "CREATED", "DESCRIPTION", "SUMMARY", "LOCATION"}

	calendarSigned    = []string{"UID", "DTSTAMP", "EXDATE", "STATUS", "TRANSP"}
	calendarEncrypted = []string{"UID", "DTSTAMP", "COMMENT"}

	// Attendees travel in their own encrypted part, under the shared session
	// key so that everyone invited can read them.
	attendeeEncrypted = []string{"UID", "DTSTAMP", "ATTENDEE"}
)

// required are the properties every card carries, so that a card can be
// understood on its own. A card holding nothing else is empty in substance.
var required = map[string]bool{"UID": true, "DTSTAMP": true}

type card struct {
	Type      int
	Data      string
	Signature string
}

type eventData struct {
	SharedKeyPacket       string     `json:",omitempty"`
	SharedEventContent    []card     `json:",omitempty"`
	CalendarKeyPacket     string     `json:",omitempty"`
	CalendarEventContent  []card     `json:",omitempty"`
	AttendeesEventContent []card     `json:",omitempty"`
	Attendees             []attendee `json:",omitempty"`

	// Notifications is always sent, never omitted: on an update, leaving it
	// out would keep whatever reminders were there before, so removing the
	// last alarm would silently fail.
	Notifications []notification `json:"Notifications"`
	Permissions   int            `json:",omitempty"`
	IsOrganizer   int            `json:",omitempty"`

	// AddedProtonAttendees hands the event's shared session key to guests who
	// hold a Proton account, and RemovedAttendeeAddresses names the guests who
	// have just been dropped. Both are keyed by address rather than by token:
	// Proton needs to know who, not merely that somebody changed.
	AddedProtonAttendees     []protonAttendee `json:",omitempty"`
	RemovedAttendeeAddresses []string         `json:",omitempty"`
}

// protonAttendee gives a guest on Proton their own copy of the event's shared
// session key, wrapped to their address key.
//
// Being on the guest list is not enough to read the event: the parts everyone
// invited is meant to see are encrypted under that session key, and nothing
// else hands a guest a copy of it.
type protonAttendee struct {
	Email            string
	AddressKeyPacket string
}

// attendee is the part of an invitation Proton keeps in the clear, so that it
// can track replies without being able to read who was invited.
type attendee struct {
	Token  string
	Status int
}

// Attendee reply states, as Proton numbers them.
const (
	statusNeedsAction = 0
	statusTentative   = 1
	statusDeclined    = 2
	statusAccepted    = 3
)

type createEntry struct {
	Overwrite int
	Event     *eventData
}

type updateEntry struct {
	ID    string
	Event *eventData
}

type deleteEntry struct {
	ID             string
	DeletionReason int
}

type syncReq struct {
	MemberID string
	Events   []any
}

// Put creates an event, or replaces the existing one with the same UID.
//
// This is CalDAV PUT semantics: the client owns the UID and does not know or
// care whether Proton has seen it before. It reports whether the event was
// created.
func Put(ctx context.Context, conn *proton.Conn, calendarID, ics string) (eventID string, created bool, err error) {
	events, err := parseEvents(ics)
	if err != nil {
		return "", false, err
	}

	// A recurring event and its exceptions arrive together, sharing a UID and
	// told apart by which occurrence each replaces (RFC 4791 §4.1). The
	// series is written first, so that an exception never lands before the
	// thing it is an exception to.
	var lastID string
	var anyCreated bool

	for _, event := range events {
		id, wasCreated, err := putOne(ctx, conn, calendarID, event)
		if err != nil {
			return "", false, err
		}

		lastID, anyCreated = id, anyCreated || wasCreated
	}

	return lastID, anyCreated, nil
}

func putOne(ctx context.Context, conn *proton.Conn, calendarID string, event *ical.Event) (eventID string, created bool, err error) {

	uid := ""
	if p := event.Props.Get("UID"); p != nil {
		uid = strings.TrimSpace(p.Value)
	}

	if uid == "" {
		return "", false, fmt.Errorf("event has no UID")
	}

	keys, err := conn.CalendarKeys(ctx, calendarID)
	if err != nil {
		return "", false, err
	}

	existing, err := findEvent(ctx, conn, calendarID, uid, recurrenceIDOf(event))
	if err != nil {
		return "", false, err
	}

	data, err := buildEvent(ctx, event, keys, existing, conn.PublicKeyRing)
	if err != nil {
		return "", false, err
	}

	var entry any
	if existing == nil {
		entry = createEntry{Event: data}
	} else {
		entry = updateEntry{ID: existing.ID, Event: data}
	}

	id, err := sync(ctx, conn, calendarID, keys.MemberID, entry, true)
	if err != nil {
		return "", false, err
	}

	// Only after the write lands. Mailing people about an event Proton then
	// refused would be worse than not mailing them at all.
	announce(ctx, conn, keys, event, newGuests(event, existing, keys.Email), data.RemovedAttendeeAddresses)

	return id, existing == nil, nil
}

// announce tells the guests whose invitation has changed, and reports rather
// than returns a failure.
//
// The event is written by this point. A mail server having a bad afternoon is
// not a reason to tell a CalDAV client that its PUT failed, when the thing it
// asked for has already happened — it would retry, and write the event again.
func announce(ctx context.Context, conn *proton.Conn, keys *proton.CalendarKeys, event *ical.Event, invited, dropped []string) {
	for _, group := range []struct {
		method    string
		addresses []string
	}{
		{methodRequest, invited},
		{methodCancel, dropped},
	} {
		if err := tellGuests(ctx, conn, keys, event, group.method, group.addresses); err != nil {
			fmt.Fprintf(os.Stderr, "carbonate: the event was saved, but %s could not be told: %v\n", strings.Join(group.addresses, ", "), err)
		}
	}
}

// Delete removes the event with the given UID. It reports whether an event
// was found to delete.
func Delete(ctx context.Context, conn *proton.Conn, calendarID, uid string) (bool, error) {
	keys, err := conn.CalendarKeys(ctx, calendarID)
	if err != nil {
		return false, err
	}

	events, err := fetchAll(ctx, conn, calendarID)
	if err != nil {
		return false, err
	}

	deleted := false

	// Every component sharing the UID goes, exceptions included. They are one
	// resource to a client, and an exception left behind is an occurrence of
	// a series that no longer exists.
	for i := range events {
		if events[i].UID != uid {
			continue
		}

		if _, err := sync(ctx, conn, calendarID, keys.MemberID, deleteEntry{ID: events[i].ID}, false); err != nil {
			return false, err
		}

		deleted = true
	}

	return deleted, nil
}

const (
	// codeOK is Proton's success code, reported in the body alongside a 200.
	codeOK = 1000

	// codeMultiple is what a batch endpoint answers at the top level: the
	// request was processed and the real verdicts are per entry. A delete
	// reports nothing per entry, so it answers 1001 with an empty list —
	// success, despite looking like a failure.
	codeMultiple = 1001
)

// sync posts a single entry to the calendar sync endpoint.
//
// wantEvent distinguishes the two response shapes: a create or update answers
// with a per-entry response carrying the stored event, while a delete answers
// with a top-level code and no entries at all.
func sync(ctx context.Context, conn *proton.Conn, calendarID, memberID string, entry any, wantEvent bool) (string, error) {
	req := syncReq{MemberID: memberID, Events: []any{entry}}

	var res struct {
		Code      int
		Responses []struct {
			Index    int
			Response struct {
				Code  int
				Error string
				Event *struct{ ID string }
			}
		}
	}

	if err := conn.Put(ctx, "/calendar/v1/"+calendarID+"/events/sync", req, &res); err != nil {
		return "", err
	}

	// The endpoint answers 200 even when an entry was rejected, so the entry's
	// own code is the real verdict.
	for _, r := range res.Responses {
		if r.Response.Code == codeOK {
			continue
		}

		if r.Response.Error != "" {
			return "", fmt.Errorf("Proton rejected the event: %s (code %d)", r.Response.Error, r.Response.Code)
		}

		return "", fmt.Errorf("Proton rejected the event (code %d)", r.Response.Code)
	}

	if !wantEvent {
		if res.Code != codeOK && res.Code != codeMultiple {
			return "", fmt.Errorf("Proton rejected the request (code %d)", res.Code)
		}

		return "", nil
	}

	if len(res.Responses) != 1 {
		return "", fmt.Errorf("expected one sync response, got %d", len(res.Responses))
	}

	event := res.Responses[0].Response.Event
	if event == nil {
		return "", fmt.Errorf("sync succeeded but returned no event")
	}

	return event.ID, nil
}

// findEvent locates an event by UID and, for an exception to a recurring
// series, by which occurrence it replaces.
//
// Matching on UID alone conflates a series with its exceptions: they share a
// UID by design, so a write meant for one occurrence would rewrite the whole
// series instead — which Proton refuses, since an event cannot be both.
func findEvent(ctx context.Context, conn *proton.Conn, calendarID, uid string, recurrenceID int64) (*rawEvent, error) {
	events, err := fetchAll(ctx, conn, calendarID)
	if err != nil {
		return nil, err
	}

	for i := range events {
		if events[i].UID == uid && events[i].RecurrenceID == recurrenceID {
			return &events[i], nil
		}
	}

	return nil, nil
}

// parseEvents extracts the components of an iCalendar object, series first.
//
// A recurring event and its exceptions share a UID, and the exception is
// meaningless before the series it modifies exists, so order matters.
func parseEvents(ics string) ([]*ical.Event, error) {
	cal, err := ical.NewDecoder(strings.NewReader(ics)).Decode()
	if err != nil {
		return nil, fmt.Errorf("parsing iCalendar: %w", err)
	}

	components := cal.Events()
	if len(components) == 0 {
		return nil, fmt.Errorf("iCalendar object contains no VEVENT")
	}

	series := make([]*ical.Event, 0, len(components))
	exceptions := make([]*ical.Event, 0, len(components))

	for i := range components {
		event := &components[i]

		if recurrenceIDOf(event) == 0 {
			series = append(series, event)

			continue
		}

		exceptions = append(exceptions, event)
	}

	return append(series, exceptions...), nil
}

// parseEvent extracts a single VEVENT, for callers that handle only one.
func parseEvent(ics string) (*ical.Event, error) {
	events, err := parseEvents(ics)
	if err != nil {
		return nil, err
	}

	if len(events) != 1 {
		return nil, fmt.Errorf("iCalendar object contains %d VEVENTs, want exactly one", len(events))
	}

	return events[0], nil
}

// addressKeys resolves an address to the keys Proton says to encrypt to, and
// returns nil for an address it holds no keys for.
//
// Taken as a function so that building an event stays testable without a live
// connection, and so that the one place that talks to Proton mid-write is
// named rather than reached for.
type addressKeys func(ctx context.Context, address string) (*crypto.KeyRing, error)

func buildEvent(ctx context.Context, event *ical.Event, keys *proton.CalendarKeys, existing *rawEvent, lookup addressKeys) (*eventData, error) {
	if err := normaliseSequence(event, existing); err != nil {
		return nil, err
	}

	ensureOrganizer(event, keys.Email)

	data := &eventData{Permissions: 1, IsOrganizer: 1}

	// Properties we do not recognise are encrypted rather than published:
	// hiding what we do not understand is the safe default.
	known := append(append(append(append(append([]string{}, sharedSigned...), sharedEncrypted...), calendarSigned...), calendarEncrypted...), attendeeEncrypted...)
	extra := unknownProps(event, known)

	// IsOrganizer stays 1: carbonate only writes events on calendars it owns.
	// go-proton-api does not decode the field from an existing event anyway.
	var sharedPacket, calendarPacket string
	if existing != nil {
		sharedPacket, calendarPacket = existing.SharedKeyPacket, existing.CalendarKeyPacket
	}

	shared, err := buildCards(event, sharedSigned, append(sharedEncrypted, extra...), keys, sharedPacket)
	if err != nil {
		return nil, err
	}

	data.SharedEventContent = shared.cards
	data.SharedKeyPacket = shared.keyPacket

	calendar, err := buildCards(event, calendarSigned, calendarEncrypted, keys, calendarPacket)
	if err != nil {
		return nil, err
	}

	data.CalendarEventContent = calendar.cards
	data.CalendarKeyPacket = calendar.keyPacket

	// Attendees share the event's session key, so they are built after the
	// shared part has settled which key that is.
	guests, err := buildAttendees(event, keys, data.SharedKeyPacket, sharedPacket)
	if err != nil {
		return nil, err
	}

	data.AttendeesEventContent = guests.cards
	data.Attendees = guests.clear
	data.Notifications = alarmsOf(event)

	if err := shareSessionKey(ctx, data, guests, existing, keys, lookup); err != nil {
		return nil, err
	}

	data.RemovedAttendeeAddresses = droppedAttendees(guests, existing, keys)

	return data, nil
}

// shareSessionKey hands the event's shared session key to every guest on
// Proton who was not already invited.
//
// A guest outside Proton gets nothing: there is no key of theirs to wrap it
// to, and they will have to hear about the event some other way. An address
// Proton cannot answer for is reported and skipped rather than failing the
// write — the guest is still recorded, exactly as they were before.
//
// Only the newly invited are handed a key, which is what the web client does:
// re-wrapping the key for someone who already holds it is pointless work and a
// pointless round trip. Who was already there is read from the tokens Proton
// keeps in the clear, so it costs nothing to know.
//
// The exception is an event that has just been re-keyed, where the packets
// guests already hold open nothing. Reusing the stored key packet is the usual
// path and avoids this entirely, but when there is none to reuse, everyone has
// to be handed the new key or the guests who were already there are quietly
// locked out.
//
// We are never handed a packet for ourselves. A client routinely lists the
// organiser among the attendees, and the organiser opens the event through the
// calendar key like any other member.
func shareSessionKey(ctx context.Context, data *eventData, guests attendeeParts, existing *rawEvent, keys *proton.CalendarKeys, lookup addressKeys) error {
	if lookup == nil || guests.sessionKey == nil {
		return nil
	}

	reKeyed := data.SharedKeyPacket != ""

	invited := make(map[string]bool)

	if existing != nil && !reKeyed {
		for _, a := range existing.Attendees {
			invited[a.Token] = true
		}
	}

	for _, guest := range guests.guests {
		if invited[guest.token] || guest.address == normaliseAddress(keys.Email) {
			continue
		}

		kr, err := lookup(ctx, guest.address)
		if err != nil {
			fmt.Fprintf(os.Stderr, "carbonate: could not look up keys for %s, who will not be able to open the event: %v\n", guest.address, err)

			continue
		}

		if kr == nil {
			continue
		}

		packet, err := kr.EncryptSessionKey(guests.sessionKey)
		if err != nil {
			return fmt.Errorf("encrypting the session key for %s: %w", guest.address, err)
		}

		data.AddedProtonAttendees = append(data.AddedProtonAttendees, protonAttendee{
			Email:            guest.address,
			AddressKeyPacket: base64.StdEncoding.EncodeToString(packet),
		})
	}

	return nil
}

// droppedAttendees names the guests the stored event had and this one does not.
//
// Proton wants addresses here, and the clear list it keeps beside an event
// holds only tokens, which are hashes — so the stored guest list has to be
// decrypted to find out who is leaving. Failing that is not worth refusing the
// write over: an uninformed guest is the behaviour carbonate has always had.
func droppedAttendees(guests attendeeParts, existing *rawEvent, keys *proton.CalendarKeys) []string {
	stored, err := storedAttendees(existing, keys)
	if err != nil {
		fmt.Fprintf(os.Stderr, "carbonate: could not read the stored guest list, so nobody will be told they were removed: %v\n", err)

		return nil
	}

	seen := make(map[string]bool, len(guests.guests))
	for _, guest := range guests.guests {
		seen[guest.address] = true
	}

	var out []string

	// seen doubles as the list of names already accounted for, so a guest
	// written down twice is only reported as leaving once.
	for _, address := range stored {
		if seen[address] {
			continue
		}

		seen[address] = true

		out = append(out, address)
	}

	return out
}

// storedAttendees returns the addresses invited to the event Proton already
// holds.
//
// They live only in the encrypted attendee part, under the shared session key
// — the same place carbonate wrote them.
func storedAttendees(existing *rawEvent, keys *proton.CalendarKeys) ([]string, error) {
	if existing == nil || len(existing.AttendeesEvents) == 0 {
		return nil, nil
	}

	keyPacket, err := base64.StdEncoding.DecodeString(existing.SharedKeyPacket)
	if err != nil {
		return nil, fmt.Errorf("decoding the shared key packet: %w", err)
	}

	var out []string

	for _, part := range existing.AttendeesEvents {
		if err := part.Decode(keys.CalKR, keys.AddrKR, keyPacket); err != nil {
			return nil, fmt.Errorf("decrypting the stored attendees: %w", err)
		}

		stored, err := parseEvent(part.Data)
		if err != nil {
			return nil, fmt.Errorf("parsing the stored attendees: %w", err)
		}

		for _, field := range stored.Props["ATTENDEE"] {
			out = append(out, normaliseAddress(field.Value))
		}
	}

	return out, nil
}

// guest is one invitation, under both the names it goes by: the address a
// client wrote down, and the token Proton knows them as.
type guest struct {
	address string
	token   string
}

// attendeeParts is everything an event's guest list contributes to a write.
type attendeeParts struct {
	cards []card
	clear []attendee

	// guests names who was invited. Proton never learns this from the clear
	// list, but carbonate needs it to hand a Proton guest the session key and
	// to say who has been dropped.
	guests []guest

	// sessionKey is the shared key the guest list was encrypted under, and so
	// the key a guest needs a copy of to open the event at all.
	sessionKey *crypto.SessionKey
}

// buildAttendees renders the attendee part and the clear list Proton keeps
// beside it.
//
// Every attendee is identified by a token rather than an address: Proton
// tracks replies without learning who was invited. The token is a hash of the
// event UID and the address, so both sides derive the same one.
func buildAttendees(event *ical.Event, keys *proton.CalendarKeys, newPacket, oldPacket string) (attendeeParts, error) {
	fields := event.Props["ATTENDEE"]
	if len(fields) == 0 {
		return attendeeParts{}, nil
	}

	uid := ""
	if p := event.Props.Get("UID"); p != nil {
		uid = strings.TrimSpace(p.Value)
	}

	out := attendeeParts{
		clear:  make([]attendee, 0, len(fields)),
		guests: make([]guest, 0, len(fields)),
	}

	// Index rather than range over a copy: the token has to be written back
	// into the event, and a copied Prop with a nil Params map would drop it.
	for i := range fields {
		field := &fields[i]

		token := attendeeToken(uid, field.Value)

		if field.Params == nil {
			field.Params = ical.Params{}
		}

		field.Params.Set("X-PM-TOKEN", token)

		out.clear = append(out.clear, attendee{Token: token, Status: partstat(field)})
		out.guests = append(out.guests, guest{address: normaliseAddress(field.Value), token: token})
	}

	body := pick(event, attendeeEncrypted)
	if body == "" {
		return attendeeParts{}, nil
	}

	signature, err := sign(keys.AddrKR, body)
	if err != nil {
		return attendeeParts{}, err
	}

	// Reuse whichever shared session key the event ended up with: a freshly
	// minted one on create, or the stored packet on update.
	packet := newPacket
	if packet == "" {
		packet = oldPacket
	}

	sessionKey, _, err := sessionKeyFor(keys, packet)
	if err != nil {
		return attendeeParts{}, err
	}

	encrypted, err := sessionKey.Encrypt(crypto.NewPlainMessageFromString(body))
	if err != nil {
		return attendeeParts{}, fmt.Errorf("encrypting attendees: %w", err)
	}

	out.sessionKey = sessionKey
	out.cards = []card{{
		Type:      cardEncryptedAndSigned,
		Data:      base64.StdEncoding.EncodeToString(encrypted),
		Signature: signature,
	}}

	return out, nil
}

// recurrenceIDOf returns the occurrence an event replaces, as the Unix time
// Proton records it by, or zero for an ordinary event.
func recurrenceIDOf(event *ical.Event) int64 {
	prop := event.Props.Get("RECURRENCE-ID")
	if prop == nil {
		return 0
	}

	when, err := time.Parse("20060102T150405Z", strings.TrimSpace(prop.Value))
	if err != nil {
		// Also accept a date-only value, which a full-day series uses.
		if when, err = time.Parse("20060102", strings.TrimSpace(prop.Value)); err != nil {
			return 0
		}
	}

	return when.Unix()
}

// ensureOrganizer names us as organiser when the event invites people and the
// client did not say who is hosting.
//
// Proton refuses such an event outright — "Shared event has attendees but does
// not have an organizer" — and a client that omits it would otherwise get an
// error it cannot act on. We are writing to our own calendar, so we are the
// organiser.
func ensureOrganizer(event *ical.Event, email string) {
	if len(event.Props["ATTENDEE"]) == 0 || email == "" {
		return
	}

	if p := event.Props.Get("ORGANIZER"); p != nil && strings.TrimSpace(p.Value) != "" {
		return
	}

	event.Props.Set(&ical.Prop{Name: "ORGANIZER", Value: "mailto:" + email})
}

// attendeeToken identifies an attendee without naming them.
//
// SHA-1 is not a security choice here — Proton uses it as an identifier, and
// both ends must agree — so it is used deliberately rather than by oversight.
func attendeeToken(uid, address string) string {
	sum := sha1.Sum([]byte(uid + normaliseAddress(address)))

	return hex.EncodeToString(sum[:])
}

// normaliseAddress strips the mailto: scheme and lowercases, so the same
// person yields the same token however a client wrote them down.
func normaliseAddress(address string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(address), "mailto:"))
}

// partstat maps an iCalendar reply state onto Proton's.
func partstat(field *ical.Prop) int {
	if field.Params == nil {
		return statusNeedsAction
	}

	switch strings.ToUpper(field.Params.Get("PARTSTAT")) {
	case "ACCEPTED":
		return statusAccepted
	case "DECLINED":
		return statusDeclined
	case "TENTATIVE":
		return statusTentative
	default:
		return statusNeedsAction
	}
}

type cardSet struct {
	cards     []card
	keyPacket string
}

// buildCards renders one part of the event.
//
// existingKeyPacket, when present, is reused rather than replaced: re-keying
// an event on every edit would needlessly churn the copy every attendee holds.
// A fresh key packet is only sent when there is none to reuse.
func buildCards(event *ical.Event, signedProps, encryptedProps []string, keys *proton.CalendarKeys, existingKeyPacket string) (cardSet, error) {
	var out cardSet

	if body := pick(event, signedProps); body != "" {
		signature, err := sign(keys.AddrKR, body)
		if err != nil {
			return cardSet{}, err
		}

		out.cards = append(out.cards, card{Type: cardSigned, Data: body, Signature: signature})
	}

	body := pick(event, encryptedProps)
	if body == "" {
		return out, nil
	}

	// The signature covers the plaintext, not the ciphertext — which is how
	// the read path verifies it, after decrypting.
	signature, err := sign(keys.AddrKR, body)
	if err != nil {
		return cardSet{}, err
	}

	sessionKey, keyPacket, err := sessionKeyFor(keys, existingKeyPacket)
	if err != nil {
		return cardSet{}, err
	}

	encrypted, err := sessionKey.Encrypt(crypto.NewPlainMessageFromString(body))
	if err != nil {
		return cardSet{}, fmt.Errorf("encrypting card: %w", err)
	}

	out.keyPacket = keyPacket
	out.cards = append(out.cards, card{
		Type:      cardEncryptedAndSigned,
		Data:      base64.StdEncoding.EncodeToString(encrypted),
		Signature: signature,
	})

	return out, nil
}

// sessionKeyFor reuses an existing key packet, or mints a new one. The
// returned key packet is empty when an existing one was reused, since Proton
// only wants it sent when it changes.
func sessionKeyFor(keys *proton.CalendarKeys, existingKeyPacket string) (*crypto.SessionKey, string, error) {
	if existingKeyPacket != "" {
		raw, err := base64.StdEncoding.DecodeString(existingKeyPacket)
		if err != nil {
			return nil, "", fmt.Errorf("decoding existing key packet: %w", err)
		}

		sessionKey, err := keys.CalKR.DecryptSessionKey(raw)
		if err != nil {
			return nil, "", fmt.Errorf("decrypting existing session key: %w", err)
		}

		return sessionKey, "", nil
	}

	sessionKey, err := crypto.GenerateSessionKey()
	if err != nil {
		return nil, "", fmt.Errorf("generating session key: %w", err)
	}

	keyPacket, err := keys.CalKR.EncryptSessionKey(sessionKey)
	if err != nil {
		return nil, "", fmt.Errorf("encrypting session key: %w", err)
	}

	return sessionKey, base64.StdEncoding.EncodeToString(keyPacket), nil
}

func sign(addrKR *crypto.KeyRing, body string) (string, error) {
	signature, err := addrKR.SignDetached(crypto.NewPlainMessageFromString(body))
	if err != nil {
		return "", fmt.Errorf("signing card: %w", err)
	}

	armored, err := signature.GetArmored()
	if err != nil {
		return "", fmt.Errorf("armouring signature: %w", err)
	}

	return armored, nil
}

// pick renders the named properties as a standalone iCalendar object, or ""
// when nothing beyond UID and DTSTAMP would be left — Proton expects such a
// card to be omitted rather than sent empty.
func pick(event *ical.Event, names []string) string {
	out := ical.NewEvent()
	substantive := false

	for _, name := range names {
		props, ok := event.Props[strings.ToUpper(name)]
		if !ok || len(props) == 0 {
			continue
		}

		out.Props[strings.ToUpper(name)] = props

		if !required[strings.ToUpper(name)] {
			substantive = true
		}
	}

	if !substantive {
		return ""
	}

	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, "-//carbonate//EN")
	cal.Children = append(cal.Children, out.Component)

	var buf strings.Builder
	if err := ical.NewEncoder(&buf).Encode(cal); err != nil {
		return ""
	}

	return buf.String()
}

func unknownProps(event *ical.Event, known []string) []string {
	seen := make(map[string]bool, len(known))
	for _, name := range known {
		seen[strings.ToUpper(name)] = true
	}

	var out []string

	for name := range event.Props {
		if !seen[strings.ToUpper(name)] {
			out = append(out, strings.ToUpper(name))
		}
	}

	return out
}

// normaliseSequence makes sure SEQUENCE is present, plainly encoded, and — on
// an update — strictly greater than the stored event's.
//
// Proton *silently* drops an edit whose SEQUENCE does not increase: no error,
// no changed event. Clients do not reliably bump it, so carbonate must. And
// the value has to be bare, because "SEQUENCE;VALUE=TEXT:1", which go-ical's
// SetText produces, earns a 500.
func normaliseSequence(event *ical.Event, existing *rawEvent) error {
	next := 0

	if p := event.Props.Get("SEQUENCE"); p != nil {
		if n, err := strconv.Atoi(strings.TrimSpace(p.Value)); err == nil {
			next = n
		}
	}

	if existing != nil {
		current, err := storedSequence(existing)
		if err != nil {
			return err
		}

		if current+1 > next {
			next = current + 1
		}
	}

	event.Props.Set(&ical.Prop{Name: "SEQUENCE", Value: strconv.Itoa(next)})

	return nil
}

// storedSequence reads SEQUENCE from the stored event.
//
// Proton keeps it in the signed shared card, which is cleartext, so this needs
// no calendar key.
func storedSequence(existing *rawEvent) (int, error) {
	for _, part := range existing.SharedEvents {
		if part.Type&api.CalendarEventTypeEncrypted != 0 || part.Data == "" {
			continue
		}

		for _, line := range properties(part.Data) {
			if name(line) != "SEQUENCE" {
				continue
			}

			value := strings.TrimPrefix(line, "SEQUENCE:")

			n, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return 0, fmt.Errorf("stored SEQUENCE %q is not a number: %w", value, err)
			}

			return n, nil
		}
	}

	return 0, nil
}

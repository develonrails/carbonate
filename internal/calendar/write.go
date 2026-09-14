package calendar

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

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
	SharedKeyPacket      string `json:",omitempty"`
	SharedEventContent   []card `json:",omitempty"`
	CalendarKeyPacket    string `json:",omitempty"`
	CalendarEventContent []card `json:",omitempty"`
	Permissions          int    `json:",omitempty"`
	IsOrganizer          int    `json:",omitempty"`
}

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
	event, err := parseEvent(ics)
	if err != nil {
		return "", false, err
	}

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

	existing, err := findByUID(ctx, conn, calendarID, uid)
	if err != nil {
		return "", false, err
	}

	data, err := buildEvent(event, keys, existing)
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

	return id, existing == nil, nil
}

// Delete removes the event with the given UID. It reports whether an event
// was found to delete.
func Delete(ctx context.Context, conn *proton.Conn, calendarID, uid string) (bool, error) {
	keys, err := conn.CalendarKeys(ctx, calendarID)
	if err != nil {
		return false, err
	}

	existing, err := findByUID(ctx, conn, calendarID, uid)
	if err != nil {
		return false, err
	}

	if existing == nil {
		return false, nil
	}

	if _, err := sync(ctx, conn, calendarID, keys.MemberID, deleteEntry{ID: existing.ID}, false); err != nil {
		return false, err
	}

	return true, nil
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

// findByUID locates an event by its iCalendar UID, returning nil when the
// calendar has no such event.
func findByUID(ctx context.Context, conn *proton.Conn, calendarID, uid string) (*api.CalendarEvent, error) {
	events, err := fetchAll(ctx, conn, calendarID)
	if err != nil {
		return nil, err
	}

	for i := range events {
		if events[i].UID == uid {
			return &events[i], nil
		}
	}

	return nil, nil
}

// parseEvent extracts the single VEVENT from an iCalendar object.
func parseEvent(ics string) (*ical.Event, error) {
	cal, err := ical.NewDecoder(strings.NewReader(ics)).Decode()
	if err != nil {
		return nil, fmt.Errorf("parsing iCalendar: %w", err)
	}

	events := cal.Events()
	switch len(events) {
	case 1:
		return &events[0], nil
	case 0:
		return nil, fmt.Errorf("iCalendar object contains no VEVENT")
	default:
		return nil, fmt.Errorf("iCalendar object contains %d VEVENTs, want exactly one", len(events))
	}
}

func buildEvent(event *ical.Event, keys *proton.CalendarKeys, existing *api.CalendarEvent) (*eventData, error) {
	if err := normaliseSequence(event, existing); err != nil {
		return nil, err
	}

	data := &eventData{Permissions: 1, IsOrganizer: 1}

	// Properties we do not recognise are encrypted rather than published:
	// hiding what we do not understand is the safe default.
	known := append(append(append(append([]string{}, sharedSigned...), sharedEncrypted...), calendarSigned...), calendarEncrypted...)
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

	return data, nil
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
func normaliseSequence(event *ical.Event, existing *api.CalendarEvent) error {
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
func storedSequence(existing *api.CalendarEvent) (int, error) {
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

package calendar

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

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

type syncReq struct {
	MemberID string
	Events   []any
}

// Create writes a new event to a calendar and returns its Proton event ID.
//
// ics is an iCalendar object as a CalDAV client would PUT it.
func Create(ctx context.Context, conn *proton.Conn, calendarID, ics string) (string, error) {
	event, err := parseEvent(ics)
	if err != nil {
		return "", err
	}

	keys, err := conn.CalendarKeys(ctx, calendarID)
	if err != nil {
		return "", err
	}

	data, err := buildEvent(event, keys)
	if err != nil {
		return "", err
	}

	req := syncReq{
		MemberID: keys.MemberID,
		Events:   []any{createEntry{Event: data}},
	}

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

	if len(res.Responses) != 1 {
		return "", fmt.Errorf("expected one sync response, got %d", len(res.Responses))
	}

	// The sync endpoint answers 200 even when an entry was rejected; the real
	// verdict is per-entry.
	inner := res.Responses[0].Response
	if inner.Event == nil {
		if inner.Error != "" {
			return "", fmt.Errorf("Proton rejected the event: %s (code %d)", inner.Error, inner.Code)
		}

		return "", fmt.Errorf("Proton rejected the event (code %d)", inner.Code)
	}

	return inner.Event.ID, nil
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

func buildEvent(event *ical.Event, keys *proton.CalendarKeys) (*eventData, error) {
	normaliseSequence(event)

	data := &eventData{Permissions: 1, IsOrganizer: 1}

	// Properties we do not recognise are encrypted rather than published:
	// hiding what we do not understand is the safe default.
	known := append(append(append(append([]string{}, sharedSigned...), sharedEncrypted...), calendarSigned...), calendarEncrypted...)
	extra := unknownProps(event, known)

	shared, err := buildCards(event, sharedSigned, append(sharedEncrypted, extra...), keys)
	if err != nil {
		return nil, err
	}

	data.SharedEventContent = shared.cards
	data.SharedKeyPacket = shared.keyPacket

	calendar, err := buildCards(event, calendarSigned, calendarEncrypted, keys)
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

func buildCards(event *ical.Event, signedProps, encryptedProps []string, keys *proton.CalendarKeys) (cardSet, error) {
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

	sessionKey, err := crypto.GenerateSessionKey()
	if err != nil {
		return cardSet{}, fmt.Errorf("generating session key: %w", err)
	}

	keyPacket, err := keys.CalKR.EncryptSessionKey(sessionKey)
	if err != nil {
		return cardSet{}, fmt.Errorf("encrypting session key: %w", err)
	}

	encrypted, err := sessionKey.Encrypt(crypto.NewPlainMessageFromString(body))
	if err != nil {
		return cardSet{}, fmt.Errorf("encrypting card: %w", err)
	}

	out.keyPacket = base64.StdEncoding.EncodeToString(keyPacket)
	out.cards = append(out.cards, card{
		Type:      cardEncryptedAndSigned,
		Data:      base64.StdEncoding.EncodeToString(encrypted),
		Signature: signature,
	})

	return out, nil
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

// normaliseSequence makes sure SEQUENCE is present and plainly encoded.
//
// Proton rejects "SEQUENCE;VALUE=TEXT:0" — which is what go-ical's SetText
// produces — with a 500, and silently drops an update whose SEQUENCE does not
// increase.
func normaliseSequence(event *ical.Event) {
	value := "0"

	if p := event.Props.Get("SEQUENCE"); p != nil {
		if n, err := strconv.Atoi(strings.TrimSpace(p.Value)); err == nil {
			value = strconv.Itoa(n)
		}
	}

	event.Props.Set(&ical.Prop{Name: "SEQUENCE", Value: value})
}

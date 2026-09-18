// Package calendar reads Proton Calendar events and reassembles them into
// iCalendar.
//
// Proton splits an event across four parts — SharedEvents, CalendarEvents,
// AttendeesEvents and PersonalEvents — each of which may be encrypted, signed,
// both, or neither. Reading means decrypting every part and concatenating the
// properties back into one VEVENT.
package calendar

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	api "github.com/ProtonMail/go-proton-api"
	"github.com/ProtonMail/gopenpgp/v2/crypto"

	"github.com/develonrails/carbonate/internal/proton"
)

// Event is a decrypted Proton calendar event.
type Event struct {
	ID    string
	UID   string
	Start time.Time
	End   time.Time

	// Modified is when Proton last saw the event change. It drives the CalDAV
	// ETag, so a client can skip events it already has.
	Modified time.Time

	FullDay   bool
	Attendees int

	// Properties are the decrypted iCalendar lines gathered from every part,
	// in the order Proton stores them.
	Properties []string

	// Alarms are the reminders Proton keeps outside the encrypted parts.
	Alarms []notification

	// RecurrenceID identifies which occurrence of a series this event
	// replaces. Zero for an ordinary event or for the series itself.
	RecurrenceID int64

	// Unverified means a signature on the event was made by a key carbonate
	// does not hold, so who wrote it cannot be proved. The contents still
	// decrypted, and are served.
	Unverified bool
}

// rawEvent is an event as Proton actually sends it.
//
// go-proton-api's struct is embedded rather than replaced: it decodes the
// parts and key packets correctly, and its CalendarEventPart.Decode does the
// crypto. What it lacks is Notifications, and encoding/json flattens the
// embedded struct so both come from one response.
type rawEvent struct {
	api.CalendarEvent

	Notifications []notification

	// RecurrenceID marks an event as an exception to the series sharing its
	// UID. go-proton-api does not decode it, and without it every occurrence
	// of a series looks like the same event.
	RecurrenceID int64
}

// multiValued lists iCalendar properties that may legitimately appear more
// than once in a VEVENT. Everything else is taken to be single-valued, so a
// repeat coming from another part is a duplicate rather than extra data.
var multiValued = map[string]bool{
	"ATTENDEE":       true,
	"ATTACH":         true,
	"CATEGORIES":     true,
	"COMMENT":        true,
	"CONTACT":        true,
	"EXDATE":         true,
	"RDATE":          true,
	"RELATED-TO":     true,
	"RESOURCES":      true,
	"REQUEST-STATUS": true,
}

// name returns the property name of an iCalendar line: everything before the
// first ";" (parameters) or ":" (value).
func name(line string) string {
	if i := strings.IndexAny(line, ";:"); i >= 0 {
		return strings.ToUpper(line[:i])
	}

	return strings.ToUpper(line)
}

// ICS reassembles the parts into a single valid iCalendar object.
//
// Every part arrives as its own complete VCALENDAR wrapping a fragment of the
// VEVENT, so the wrappers are dropped and the fragments merged. X- properties
// are kept as they come; carbonate does not know which of them repeat.
func (e Event) ICS() string {
	var body []string
	seen := make(map[string]bool)

	for _, line := range e.Properties {
		switch name(line) {
		case "BEGIN", "END", "VERSION", "PRODID", "CALSCALE", "METHOD":
			continue
		}

		key := name(line)
		if !multiValued[key] && !strings.HasPrefix(key, "X-") {
			if seen[key] {
				continue
			}

			seen[key] = true
		}

		body = append(body, line)
	}

	out := []string{
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"PRODID:-//carbonate//EN",
	}
	out = append(out, e.component()...)
	out = append(out, "END:VCALENDAR")

	return strings.Join(out, "\r\n") + "\r\n"
}

// component renders the VEVENT alone, for placing inside a calendar object
// that may hold several.
func (e Event) component() []string {
	var body []string

	seen := make(map[string]bool)

	for _, line := range e.Properties {
		switch name(line) {
		case "BEGIN", "END", "VERSION", "PRODID", "CALSCALE", "METHOD":
			continue
		}

		key := name(line)
		if !multiValued[key] && !strings.HasPrefix(key, "X-") {
			if seen[key] {
				continue
			}

			seen[key] = true
		}

		body = append(body, line)
	}

	out := append([]string{"BEGIN:VEVENT"}, body...)

	// Reminders live outside the encrypted parts, so they are put back here
	// rather than arriving with the rest of the properties.
	out = append(out, alarmLines(e.Alarms, e.Summary())...)

	return append(out, "END:VEVENT")
}

// Merge renders a series and its exceptions as one iCalendar object.
//
// RFC 4791 §4.1 puts every component sharing a UID in a single resource, and
// a client that met the same UID at two addresses would have no way to tell
// which was which.
func Merge(events []Event) string {
	if len(events) == 1 {
		return events[0].ICS()
	}

	out := []string{
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"PRODID:-//carbonate//EN",
	}

	for _, e := range events {
		out = append(out, e.component()...)
	}

	out = append(out, "END:VCALENDAR")

	return strings.Join(out, "\r\n") + "\r\n"
}

// Summary returns the event's SUMMARY property, or "" if it has none.
func (e Event) Summary() string {
	for _, p := range e.Properties {
		if after, ok := strings.CutPrefix(p, "SUMMARY:"); ok {
			return after
		}
	}

	return ""
}

// When renders the event's time for display. A full-day event has no
// meaningful clock time: Proton stores its start as midnight UTC, which would
// otherwise be shown shifted into the local zone.
func (e Event) When() string {
	if e.FullDay {
		return e.Start.UTC().Format("2006-01-02") + " (all day)"
	}

	return e.Start.Local().Format("2006-01-02 15:04")
}

// Events returns every event in a calendar that can be decrypted, and the ids
// of those that cannot.
//
// An event that cannot be decrypted is skipped rather than failing the
// listing. The two are not close: a CalDAV client that gets an error shows an
// empty calendar, so one unreadable event would hide every readable one behind
// it — the whole calendar gone for the sake of the one event actually at
// fault. Skipping costs that one event and keeps the rest reachable.
//
// The skipped ids come back rather than disappearing, because an event that
// silently goes missing is the kind of fault nobody reports until long after
// it started.
func Events(ctx context.Context, conn *proton.Conn, calendarID string) ([]Event, []string, error) {
	keys, err := conn.CalendarKeys(ctx, calendarID)
	if err != nil {
		return nil, nil, err
	}

	raw, err := fetchAll(ctx, conn, calendarID)
	if err != nil {
		return nil, nil, err
	}

	events, unreadable := decodeAll(raw, keys.CalKR, keys.AddrKR)

	return events, unreadable, nil
}

// decodeAll decodes what it can and names what it cannot.
//
// Separate from Events so the trade it makes can be tested without an
// account: that one unreadable event costs one event, and nothing else.
func decodeAll(raw []rawEvent, calKR, addrKR *crypto.KeyRing) ([]Event, []string) {
	events := make([]Event, 0, len(raw))

	var unreadable []string

	for _, r := range raw {
		event, err := decode(r, calKR, addrKR)
		if err != nil {
			unreadable = append(unreadable, r.ID)

			continue
		}

		events = append(events, event)
	}

	return events, unreadable
}

// pageSize is deliberately below go-proton-api's own maxPageSize of 150, which
// the calendar endpoint rejects with "Invalid page size parameter". That makes
// GetAllCalendarEvents unusable, so carbonate pages by hand.
const pageSize = 100

func fetchAll(ctx context.Context, conn *proton.Conn, calendarID string) ([]rawEvent, error) {
	var all []rawEvent

	for page := 0; ; page++ {
		var res struct {
			Events []rawEvent
		}

		path := fmt.Sprintf("/calendar/v1/%s/events?Page=%d&PageSize=%d", calendarID, page, pageSize)

		if err := conn.Get(ctx, path, &res); err != nil {
			return nil, fmt.Errorf("fetching events: %w", err)
		}

		all = append(all, res.Events...)

		if len(res.Events) < pageSize {
			return all, nil
		}
	}
}

// Calendar is a Proton calendar with the details the API actually returns.
type Calendar struct {
	ID    string
	Name  string
	Color string
}

// List returns the account's calendars.
//
// It bypasses go-proton-api's GetCalendars, whose Calendar struct still expects
// a top-level Name that the API no longer sends. The name lives on the member
// record instead, and it is plaintext — unlike almost everything else in Proton
// Calendar, it is not encrypted.
func List(ctx context.Context, conn *proton.Conn) ([]Calendar, error) {
	var res struct {
		Calendars []struct {
			ID      string
			Members []struct {
				Email string
				Name  string
				Color string
			}
		}
	}

	if err := conn.Get(ctx, "/calendar/v1", &res); err != nil {
		return nil, err
	}

	addresses, err := conn.Client.GetAddresses(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetching addresses: %w", err)
	}

	mine := make(map[string]bool, len(addresses))
	for _, a := range addresses {
		mine[strings.ToLower(a.Email)] = true
	}

	out := make([]Calendar, 0, len(res.Calendars))

	for _, c := range res.Calendars {
		cal := Calendar{ID: c.ID}

		// A shared calendar has a member record per participant; ours carries
		// the name we chose for it.
		for _, m := range c.Members {
			if mine[strings.ToLower(m.Email)] {
				cal.Name, cal.Color = m.Name, m.Color
				break
			}
		}

		out = append(out, cal)
	}

	return out, nil
}

func decode(raw rawEvent, calKR, addrKR *crypto.KeyRing) (Event, error) {
	event := Event{
		Alarms:       raw.Notifications,
		RecurrenceID: raw.RecurrenceID,
		ID:           raw.ID,
		UID:          raw.UID,
		Start:        time.Unix(raw.StartTime, 0),
		End:          time.Unix(raw.EndTime, 0),
		Modified:     time.Unix(raw.LastEditTime, 0),
		FullDay:      bool(raw.FullDay),
		Attendees:    len(raw.Attendees),
	}

	// Shared and attendee parts are encrypted under the shared key packet;
	// organiser-only parts under the calendar one. Personal parts carry their
	// own armoured message and need neither.
	groups := []struct {
		parts     []api.CalendarEventPart
		keyPacket string
	}{
		{raw.SharedEvents, raw.SharedKeyPacket},
		{raw.CalendarEvents, raw.CalendarKeyPacket},
		{raw.AttendeesEvents, raw.SharedKeyPacket},
		{raw.PersonalEvents, ""},
	}

	for _, group := range groups {
		var keyPacket []byte

		if group.keyPacket != "" {
			var err error

			if keyPacket, err = base64.StdEncoding.DecodeString(group.keyPacket); err != nil {
				return Event{}, fmt.Errorf("decoding key packet: %w", err)
			}
		}

		for _, part := range group.parts {
			data, ok, err := decodePart(part, calKR, addrKR, keyPacket)
			if err != nil {
				return Event{}, err
			}

			if !ok {
				event.Unverified = true
			}

			event.Properties = append(event.Properties, properties(data)...)
		}
	}

	return event, nil
}

// decodePart decrypts a part and judges its signature separately, returning
// the plaintext and whether the signature was made by a key we hold.
//
// go-proton-api answers both in one call that fails on either, and they are
// not the same question. Decryption is whether the contents can be read;
// a signature is who wrote them. addrKR holds one address's keys — ours — so
// anything written by anyone else cannot be verified here, and on a shared
// calendar that is most of it: every event another member added is signed with
// their address key, which is not ours to have.
//
// Treating the second as fatal meant refusing to serve events that had
// decrypted perfectly well, and that the Proton web app shows. Worse, one such
// event failed the whole read, so it took every other event in the calendar
// with it. Clearing the signed bit leaves Decode decrypting only, and the
// signature is judged here.
func decodePart(part api.CalendarEventPart, calKR, addrKR *crypto.KeyRing, keyPacket []byte) (string, bool, error) {
	signature := part.Signature
	signed := part.Type&api.CalendarEventTypeSigned != 0

	part.Type &^= api.CalendarEventTypeSigned

	if err := part.Decode(calKR, addrKR, keyPacket); err != nil {
		return "", false, err
	}

	if !signed {
		return part.Data, true, nil
	}

	sig, err := crypto.NewPGPSignatureFromArmored(signature)
	if err != nil {
		return part.Data, false, nil
	}

	verified := addrKR.VerifyDetached(crypto.NewPlainMessageFromString(part.Data), sig, crypto.GetUnixTime()) == nil

	return part.Data, verified, nil
}

// properties splits a decrypted part into iCalendar lines, unfolding the
// continuation lines that RFC 5545 wraps at 75 octets.
func properties(data string) []string {
	var out []string

	for _, line := range strings.Split(strings.ReplaceAll(data, "\r\n", "\n"), "\n") {
		if line == "" {
			continue
		}

		// A leading space or tab continues the previous line.
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			if len(out) > 0 {
				out[len(out)-1] += line[1:]
				continue
			}
		}

		out = append(out, line)
	}

	return out
}

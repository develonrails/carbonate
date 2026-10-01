package caldav

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// Creating and changing calendar collections.
//
// go-webdav 0.7.0 answers MKCALENDAR with "unsupported method" and PROPPATCH
// with "not implemented", so both are served here, before it sees them. They
// are the two requests a client makes when someone adds a calendar, renames
// one, or picks it a colour: Calendar.app and BusyCal both send MKCALENDAR,
// and neither falls back to the extended MKCOL that go-webdav does route.

const (
	nsDAV    = "DAV:"
	nsCalDAV = "urn:ietf:params:xml:ns:caldav"
	nsApple  = "http://apple.com/ns/ical/"

	// defaultCalendarName is given to a calendar created without one. Proton
	// refuses an empty name.
	defaultCalendarName = "Untitled"
)

// segmentPattern is what a client may name a calendar's address. It becomes
// a path segment and a key in a file, so it is held to what is safe in both.
var segmentPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~-]{0,127}$`)

// collectionProps is what a client says about a calendar when it creates or
// changes one. A nil name or colour was not mentioned.
type collectionProps struct {
	name  *string
	color *string

	// components are the kinds of object the client wants the calendar to
	// hold, if it said.
	components []string

	// other are properties carbonate has nowhere to keep.
	other []xml.Name
}

// holdsEvents reports whether the calendar asked for can hold events. A
// request that does not say is taken to want them.
func (p collectionProps) holdsEvents() bool {
	if len(p.components) == 0 {
		return true
	}

	for _, c := range p.components {
		if strings.EqualFold(c, "VEVENT") {
			return true
		}
	}

	return false
}

// parseProps reads the properties out of a MKCALENDAR, extended MKCOL or
// PROPPATCH body. All three wrap them the same way: a prop element, inside a
// set to give a value or a remove to take one away.
func parseProps(body io.Reader) (set collectionProps, removed []xml.Name, err error) {
	dec := xml.NewDecoder(body)

	var stack []xml.Name

	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return set, removed, nil
		}

		if err != nil {
			return set, removed, err
		}

		switch t := tok.(type) {
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}

		case xml.StartElement:
			n := len(stack)

			if n == 0 || stack[n-1] != (xml.Name{Space: nsDAV, Local: "prop"}) {
				stack = append(stack, t.Name)

				continue
			}

			// A child of prop is a property. Each is read or skipped whole,
			// so the stack never grows past prop.
			if n > 1 && stack[n-2].Local == "remove" {
				removed = append(removed, t.Name)

				if err := dec.Skip(); err != nil {
					return set, removed, err
				}

				continue
			}

			if err := set.read(dec, t); err != nil {
				return set, removed, err
			}
		}
	}
}

func (p *collectionProps) read(dec *xml.Decoder, start xml.StartElement) error {
	switch start.Name {
	case xml.Name{Space: nsDAV, Local: "displayname"}:
		var value string
		if err := dec.DecodeElement(&value, &start); err != nil {
			return err
		}

		value = strings.TrimSpace(value)
		p.name = &value

	case xml.Name{Space: nsApple, Local: "calendar-color"}:
		var value string
		if err := dec.DecodeElement(&value, &start); err != nil {
			return err
		}

		value = strings.TrimSpace(value)
		p.color = &value

	case xml.Name{Space: nsCalDAV, Local: "supported-calendar-component-set"}:
		var value struct {
			Comp []struct {
				Name string `xml:"name,attr"`
			} `xml:"comp"`
		}

		if err := dec.DecodeElement(&value, &start); err != nil {
			return err
		}

		for _, c := range value.Comp {
			p.components = append(p.components, c.Name)
		}

	default:
		p.other = append(p.other, start.Name)

		return dec.Skip()
	}

	return nil
}

// serveMkcalendar answers MKCALENDAR (RFC 4791, 5.3.1).
func (b *Backend) serveMkcalendar(w http.ResponseWriter, r *http.Request) {
	props, _, err := parseProps(r.Body)
	if err != nil {
		http.Error(w, "caldav: cannot read the MKCALENDAR body: "+err.Error(), http.StatusBadRequest)

		return
	}

	if err := b.create(r.Context(), r.URL.Path, props); err != nil {
		// Anything create did not refuse itself is Proton's doing.
		code := http.StatusBadGateway

		var refused *refusal
		if errors.As(err, &refused) {
			code = refused.code
		}

		http.Error(w, err.Error(), code)

		return
	}

	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusCreated)
}

// refusal is a request create turns down itself, with the status that says
// why. go-webdav keeps its own HTTP error type to itself, so MKCALENDAR, which
// is answered without it, needs one of its own.
type refusal struct {
	code int
	err  error
}

func (r *refusal) Error() string { return r.err.Error() }
func (r *refusal) Unwrap() error { return r.err }

func refuse(code int, format string, args ...any) error {
	return &refusal{code: code, err: fmt.Errorf(format, args...)}
}

// create makes a Proton calendar for the collection a client asked for at p.
//
// The client chooses the address, and expects to find the calendar there
// afterwards — so the address is remembered against the calendar Proton made,
// rather than the calendar turning up somewhere else on the next listing.
func (b *Backend) create(ctx context.Context, p string, props collectionProps) error {
	if !isCalendarPath(p) {
		return refuse(http.StatusForbidden, "a calendar can only be made directly under %s", homeSetPath)
	}

	segment := calendarSegment(p)
	if !segmentPattern.MatchString(segment) {
		return refuse(http.StatusForbidden, "%q cannot be used as a calendar address", segment)
	}

	// Proton Calendar holds events and nothing else. macOS asks every CalDAV
	// account for a list to keep reminders in, and making one would leave a
	// calendar in Proton that can never hold what it was made for.
	if !props.holdsEvents() {
		return refuse(http.StatusForbidden, "Proton Calendar holds events only, not %s", strings.Join(props.components, ", "))
	}

	// One at a time: a client that repeats the request while the first is
	// still on its way to Proton must find the calendar, not make a second.
	b.creating.Lock()
	defer b.creating.Unlock()

	b.mu.RLock()
	kept := b.paths
	b.mu.RUnlock()

	// MKCALENDAR is only for an address nothing lives at (RFC 4791, 5.3.1.2).
	// Listing first also means the answer is Proton's, not a stale map's.
	if _, err := b.ListCalendars(ctx); err != nil {
		return err
	}

	b.mu.RLock()
	_, taken := b.tokens[segment]
	b.mu.RUnlock()

	if taken {
		return refuse(http.StatusMethodNotAllowed, "a calendar already lives at %s", p)
	}

	name := defaultCalendarName
	if props.name != nil && *props.name != "" {
		name = *props.name
	}

	color := ""
	if props.color != nil {
		color = *props.color
	}

	id, err := b.store.CreateCalendar(ctx, name, color)
	if err != nil {
		return err
	}

	if err := kept.set(id, segment); err != nil && b.out != nil {
		// The calendar exists and is served for as long as this process
		// lives. Only a restart would move it, so say so rather than fail.
		fmt.Fprintf(b.out, "carbonate: calendar %s: its address could not be saved, and will change on restart: %v\n", short(id), err)
	}

	b.mu.Lock()
	b.tokens[segment] = id
	b.mu.Unlock()

	return nil
}

// servePropPatch answers PROPPATCH on a calendar collection, reporting
// whether it did. Anything else is left to go-webdav.
//
// A name and a colour are the two things about a calendar Proton keeps, so
// they are the two that can be set. Everything else is refused by name, in a
// multistatus, which is what a client asking about several properties at once
// needs in order to tell which of them went through.
func (b *Backend) servePropPatch(w http.ResponseWriter, r *http.Request) bool {
	if !isCalendarPath(r.URL.Path) {
		return false
	}

	id, err := b.calendarID(r.Context(), r.URL.Path)
	if err != nil {
		return false
	}

	props, removed, err := parseProps(r.Body)
	if err != nil {
		http.Error(w, "caldav: cannot read the PROPPATCH body: "+err.Error(), http.StatusBadRequest)

		return true
	}

	// An empty name is not a rename Proton accepts, so it is refused with the
	// properties carbonate cannot keep.
	var done, refused []xml.Name

	name := props.name
	if name != nil && *name == "" {
		name = nil
		refused = append(refused, xml.Name{Space: nsDAV, Local: "displayname"})
	}

	if name != nil || props.color != nil {
		if err := b.store.UpdateCalendar(r.Context(), id, name, props.color); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)

			return true
		}
	}

	if name != nil {
		done = append(done, xml.Name{Space: nsDAV, Local: "displayname"})
	}

	if props.color != nil {
		done = append(done, xml.Name{Space: nsApple, Local: "calendar-color"})
	}

	refused = append(refused, props.other...)
	refused = append(refused, removed...)

	var out strings.Builder

	out.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	out.WriteString(`<multistatus xmlns="DAV:"><response><href>` + html.EscapeString(r.URL.Path) + `</href>`)
	writePropstat(&out, done, "HTTP/1.1 200 OK")
	writePropstat(&out, refused, "HTTP/1.1 403 Forbidden")
	out.WriteString(`</response></multistatus>`)

	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	io.WriteString(w, out.String())

	return true
}

func writePropstat(out *strings.Builder, names []xml.Name, status string) {
	if len(names) == 0 {
		return
	}

	out.WriteString(`<propstat><prop>`)

	for _, n := range names {
		fmt.Fprintf(out, `<%s xmlns="%s"/>`, n.Local, html.EscapeString(n.Space))
	}

	out.WriteString(`</prop><status>` + status + `</status></propstat>`)
}

// paths remembers the address a client chose for each calendar it created.
//
// A calendar Proton already had is served at an address derived from its ID.
// One made through carbonate is served where the client put it, and has to
// stay there: a client that finds its new calendar gone from that address
// takes it for deleted, along with anything not yet synced into it.
type paths struct {
	mu   sync.Mutex
	file string

	// byID maps a Proton calendar ID to its path segment.
	byID map[string]string

	// changes counts the addresses added, so that a listing taken before one
	// was added can tell it must not forget it.
	changes int
}

// loadPaths reads the addresses kept in file. An empty name keeps them in
// memory only, and a file that is not there yet is simply empty.
func loadPaths(file string) (*paths, error) {
	p := &paths{file: file, byID: make(map[string]string)}

	if file == "" {
		return p, nil
	}

	raw, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return p, nil
	}

	if err != nil {
		return p, fmt.Errorf("reading calendar addresses: %w", err)
	}

	if err := json.Unmarshal(raw, &p.byID); err != nil {
		return p, fmt.Errorf("reading calendar addresses from %s: %w", file, err)
	}

	return p, nil
}

func (p *paths) segment(id string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	segment, ok := p.byID[id]

	return segment, ok
}

func (p *paths) set(id, segment string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.byID[id] = segment
	p.changes++

	return p.save()
}

// version identifies the set of addresses as it stands now.
func (p *paths) version() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.changes
}

// keep forgets the calendars that are no longer there, so that an address
// freed by deleting a calendar in Proton does not stay taken for ever.
//
// ids is a listing, and version is what version returned before it was
// taken. If an address has been added since, the listing may predate the
// calendar it belongs to, and nothing is forgotten on its word.
func (p *paths) keep(ids map[string]bool, version int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.changes != version {
		return
	}

	changed := false

	for id := range p.byID {
		if !ids[id] {
			delete(p.byID, id)
			changed = true
		}
	}

	if changed {
		// Losing this write costs nothing: the same calendars are forgotten
		// again on the next listing.
		_ = p.save()
	}
}

// save writes the addresses out, replacing the file whole so that a crash
// mid-write cannot leave half of one behind.
func (p *paths) save() error {
	if p.file == "" {
		return nil
	}

	raw, err := json.MarshalIndent(p.byID, "", "  ")
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(p.file), ".calendar-paths-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()

		return err
	}

	if err := tmp.Close(); err != nil {
		return err
	}

	return os.Rename(tmp.Name(), p.file)
}

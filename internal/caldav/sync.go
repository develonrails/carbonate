package caldav

import (
	"context"
	"html"
	"strings"

	"github.com/develonrails/carbonate/internal/calendar"
)

// syncResult is the answer to a sync-collection report: what a client must
// fetch again, what it must forget, and where it has got to.
type syncResult struct {
	Changed []changedMember
	Removed []string
	Token   string

	// Resync means the question cannot be answered incrementally and the
	// client should read the whole collection. RFC 6578 provides for this.
	Resync bool
}

type changedMember struct {
	Path string
	ETag string
	ICS  string
}

// remember records which Proton ID each event is served under.
//
// This is what makes a deletion reportable. Proton's event loop names a
// removed event by its own ID, while the client knows it by a path built from
// the iCalendar UID — which has gone with the event. The mapping has to be
// kept from before it was deleted, so it is filled in on every read.
func (b *Backend) remember(calendarID string, events []calendar.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()

	known, ok := b.uids[calendarID]
	if !ok {
		known = make(map[string]string)
		b.uids[calendarID] = known
	}

	for _, e := range events {
		known[e.ID] = e.UID
	}
}

func (b *Backend) rememberedUID(calendarID, eventID string) (string, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	uid, ok := b.uids[calendarID][eventID]

	return uid, ok
}

// sync answers a sync-collection report for the collection at p.
func (b *Backend) sync(ctx context.Context, p, token string, wantData bool) (syncResult, error) {
	id, err := b.calendarID(ctx, p)
	if err != nil {
		return syncResult{}, err
	}

	changedIDs, newToken, resync, err := b.store.Changes(ctx, id, token)
	if err != nil {
		return syncResult{}, err
	}

	// Reading everything is always a correct answer, and the client is told
	// so rather than left to infer it from a suspiciously short list.
	if resync {
		return b.fullSync(ctx, p, id, wantData)
	}

	events, err := b.events(ctx, id)
	if err != nil {
		return syncResult{}, err
	}

	b.remember(id, events)

	current := make(map[string]calendar.Event, len(events))
	for _, e := range events {
		current[e.ID] = e
	}

	_, groups := byUID(events)

	segment := calendarSegment(p)
	result := syncResult{Token: newToken}

	reported := make(map[string]bool)

	for _, eventID := range changedIDs {
		if e, ok := current[eventID]; ok {
			// One resource holds every component sharing a UID, so a change
			// to any of them is one change to that resource.
			if reported[e.UID] {
				continue
			}

			reported[e.UID] = true

			result.Changed = append(result.Changed, member(segment, groups[e.UID], wantData))

			continue
		}

		// Gone. Only reportable if we saw it before it went.
		uid, ok := b.rememberedUID(id, eventID)
		if !ok {
			// Something was removed that this process never knew about, so
			// the client may hold a copy we cannot name. Reading everything
			// is the honest answer.
			return b.fullSync(ctx, p, id, wantData)
		}

		result.Removed = append(result.Removed, objectPath(segment, uid))
	}

	return result, nil
}

// fullSync reports every member, which RFC 6578 allows in place of a delta.
func (b *Backend) fullSync(ctx context.Context, p, calendarID string, wantData bool) (syncResult, error) {
	// Where a client starting from nothing should sync from. See Cursor:
	// deriving something cleverer than the loop's latest ID skips the first
	// change that follows, and the client never learns of it.
	token, err := b.store.Cursor(ctx, calendarID)
	if err != nil {
		return syncResult{}, err
	}

	events, err := b.events(ctx, calendarID)
	if err != nil {
		return syncResult{}, err
	}

	b.remember(calendarID, events)

	segment := calendarSegment(p)
	result := syncResult{Token: token, Resync: true}

	order, groups := byUID(events)

	for _, uid := range order {
		result.Changed = append(result.Changed, member(segment, groups[uid], wantData))
	}

	return result, nil
}

func member(segment string, events []calendar.Event, wantData bool) changedMember {
	m := changedMember{Path: objectPath(segment, events[0].UID), ETag: etag(events)}

	if wantData {
		m.ICS = calendar.Merge(events)
	}

	return m
}

// multistatus renders a sync-collection answer.
//
// go-webdav 0.7.0 does not know this report, so the XML is produced here, in
// the same place that supplies the properties it also lacks.
func (r syncResult) multistatus() string {
	var b strings.Builder

	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	b.WriteString(`<multistatus xmlns="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">`)

	for _, m := range r.Changed {
		b.WriteString(`<response><href>`)
		b.WriteString(html.EscapeString(m.Path))
		b.WriteString(`</href><propstat><prop><getetag>`)
		b.WriteString(html.EscapeString(`"` + m.ETag + `"`))
		b.WriteString(`</getetag>`)

		if m.ICS != "" {
			b.WriteString(`<C:calendar-data>`)
			b.WriteString(html.EscapeString(m.ICS))
			b.WriteString(`</C:calendar-data>`)
		}

		b.WriteString(`</prop><status>HTTP/1.1 200 OK</status></propstat></response>`)
	}

	// A removed member is reported as a response with a bare 404 status, which
	// is how RFC 6578 says "forget this one".
	for _, path := range r.Removed {
		b.WriteString(`<response><href>`)
		b.WriteString(html.EscapeString(path))
		b.WriteString(`</href><status>HTTP/1.1 404 Not Found</status></response>`)
	}

	b.WriteString(`<sync-token>`)
	b.WriteString(html.EscapeString(syncTokenPrefix + r.Token))
	b.WriteString(`</sync-token>`)
	b.WriteString(`</multistatus>`)

	return b.String()
}

// syncTokenPrefix makes the token a URI, as RFC 6578 requires, and marks it as
// ours so that one issued by something else is not mistaken for a cursor.
const syncTokenPrefix = "urn:carbonate:"

// parseSyncToken returns the Proton cursor inside a client's sync token.
//
// An empty or foreign token means "start from nothing", which produces a full
// listing rather than a wrong delta.
func parseSyncToken(token string) string {
	return strings.TrimPrefix(strings.TrimSpace(token), syncTokenPrefix)
}

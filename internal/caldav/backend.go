// Package caldav exposes Proton Calendar over CalDAV.
//
// It implements go-webdav's caldav.Backend on top of internal/calendar, which
// already speaks the shapes CalDAV needs: events are put by UID and returned
// as whole iCalendar objects.
package caldav

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"

	"github.com/develonrails/carbonate/internal/cache"
	"github.com/develonrails/carbonate/internal/calendar"
	"github.com/develonrails/carbonate/internal/proton"
)

// Paths served.
//
// go-webdav derives a resource's kind from its path depth, so the layout is
// not free: principal at depth 1, home set at 2, calendars at 3, objects at 4.
// Requests to "/" and /.well-known/caldav are redirected to the principal, so
// a client only needs the bare address.
const (
	principalPath = "/principal/"
	homeSetPath   = "/principal/calendars/"
)

// Backend serves one Proton account.
type Backend struct {
	conn  *proton.Conn
	cache *cache.Events[calendar.Event]

	// tokens maps a URL-safe path segment to a Proton calendar ID. Proton IDs
	// are base64 with padding, which does not belong in a URL path.
	//
	// names maps a client-chosen resource path to the UID of the event stored
	// there. CalDAV lets the client name the resource, and that name need not
	// be the UID — Proton only knows the UID, so the two must be tied
	// together or a later GET or DELETE cannot find the event again.
	mu     sync.RWMutex
	tokens map[string]string
	names  map[string]string
}

// New returns a backend caching events for ttl.
func New(conn *proton.Conn, ttl time.Duration) *Backend {
	return &Backend{
		conn:   conn,
		cache:  cache.New[calendar.Event](ttl),
		tokens: make(map[string]string),
		names:  make(map[string]string),
	}
}

// Handler returns an http.Handler serving CalDAV for this backend.
func (b *Backend) Handler() http.Handler {
	return compat(&caldav.Handler{Backend: b})
}

func (b *Backend) CurrentUserPrincipal(ctx context.Context) (string, error) {
	return principalPath, nil
}

func (b *Backend) CalendarHomeSetPath(ctx context.Context) (string, error) {
	return homeSetPath, nil
}

// token derives a stable, URL-safe path segment from a Proton ID.
func token(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:16])
}

func (b *Backend) ListCalendars(ctx context.Context) ([]caldav.Calendar, error) {
	calendars, err := calendar.List(ctx, b.conn)
	if err != nil {
		return nil, err
	}

	out := make([]caldav.Calendar, 0, len(calendars))

	b.mu.Lock()
	for _, c := range calendars {
		t := token(c.ID)
		b.tokens[t] = c.ID

		name := c.Name
		if name == "" {
			name = "Proton Calendar"
		}

		out = append(out, caldav.Calendar{
			Path:                  homeSetPath + t + "/",
			Name:                  name,
			SupportedComponentSet: []string{"VEVENT"},
		})
	}
	b.mu.Unlock()

	return out, nil
}

func (b *Backend) GetCalendar(ctx context.Context, p string) (*caldav.Calendar, error) {
	calendars, err := b.ListCalendars(ctx)
	if err != nil {
		return nil, err
	}

	want := strings.TrimSuffix(p, "/") + "/"

	for i := range calendars {
		if calendars[i].Path == want {
			return &calendars[i], nil
		}
	}

	return nil, webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("no such calendar: %s", p))
}

func (b *Backend) CreateCalendar(ctx context.Context, c *caldav.Calendar) error {
	return webdav.NewHTTPError(http.StatusForbidden, fmt.Errorf("carbonate cannot create calendars; make it in the Proton web app"))
}

// calendarID resolves the calendar segment of a path to a Proton ID,
// refreshing the mapping if the client asked for one we have not listed yet.
func (b *Backend) calendarID(ctx context.Context, p string) (string, error) {
	segment := calendarSegment(p)
	if segment == "" {
		return "", webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("no calendar in path %s", p))
	}

	b.mu.RLock()
	id, ok := b.tokens[segment]
	b.mu.RUnlock()

	if ok {
		return id, nil
	}

	if _, err := b.ListCalendars(ctx); err != nil {
		return "", err
	}

	b.mu.RLock()
	id, ok = b.tokens[segment]
	b.mu.RUnlock()

	if !ok {
		return "", webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("no such calendar: %s", segment))
	}

	return id, nil
}

// calendarSegment returns the calendar token from "/calendars/<token>/..." .
func calendarSegment(p string) string {
	trimmed := strings.TrimPrefix(path.Clean(p), strings.TrimSuffix(homeSetPath, "/"))
	trimmed = strings.TrimPrefix(trimmed, "/")

	if i := strings.Index(trimmed, "/"); i >= 0 {
		return trimmed[:i]
	}

	return trimmed
}

// objectUID returns the UID of the event stored at an object path.
//
// A resource written by a client is remembered by the name it chose. Anything
// else is assumed to be named after its UID, which is what carbonate itself
// advertises when listing.
func (b *Backend) objectUID(p string) (string, error) {
	clean := path.Clean(p)

	b.mu.RLock()
	uid, ok := b.names[clean]
	b.mu.RUnlock()

	if ok {
		return uid, nil
	}

	base := path.Base(clean)

	name := strings.TrimSuffix(base, ".ics")
	if name == base {
		return "", webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("not a calendar object: %s", p))
	}

	unescaped, err := url.PathUnescape(name)
	if err != nil {
		return "", webdav.NewHTTPError(http.StatusBadRequest, fmt.Errorf("malformed object path %s: %w", p, err))
	}

	return unescaped, nil
}

func objectPath(calendarToken, uid string) string {
	return homeSetPath + calendarToken + "/" + url.PathEscape(uid) + ".ics"
}

func (b *Backend) events(ctx context.Context, calendarID string) ([]calendar.Event, error) {
	return b.cache.Get(ctx, calendarID, func(ctx context.Context) ([]calendar.Event, error) {
		return calendar.Events(ctx, b.conn, calendarID)
	})
}

// object converts a decrypted event into a CalDAV resource.
func object(calendarToken string, e calendar.Event) (caldav.CalendarObject, error) {
	parsed, err := ical.NewDecoder(strings.NewReader(e.ICS())).Decode()
	if err != nil {
		return caldav.CalendarObject{}, fmt.Errorf("re-parsing event %s: %w", e.UID, err)
	}

	return caldav.CalendarObject{
		Path:    objectPath(calendarToken, e.UID),
		ModTime: e.Modified,
		ETag:    etag(e),
		Data:    parsed,
	}, nil
}

// etag changes whenever Proton reports the event as edited, which is what
// lets a client skip unchanged objects.
func etag(e calendar.Event) string {
	return strconv.FormatInt(e.Modified.Unix(), 10) + "-" + token(e.ID)[:8]
}

func (b *Backend) ListCalendarObjects(ctx context.Context, p string, req *caldav.CalendarCompRequest) ([]caldav.CalendarObject, error) {
	id, err := b.calendarID(ctx, p)
	if err != nil {
		return nil, err
	}

	events, err := b.events(ctx, id)
	if err != nil {
		return nil, err
	}

	segment := calendarSegment(p)
	out := make([]caldav.CalendarObject, 0, len(events))

	for _, e := range events {
		obj, err := object(segment, e)
		if err != nil {
			return nil, err
		}

		out = append(out, obj)
	}

	return out, nil
}

func (b *Backend) GetCalendarObject(ctx context.Context, p string, req *caldav.CalendarCompRequest) (*caldav.CalendarObject, error) {
	uid, err := b.objectUID(p)
	if err != nil {
		return nil, err
	}

	id, err := b.calendarID(ctx, p)
	if err != nil {
		return nil, err
	}

	events, err := b.events(ctx, id)
	if err != nil {
		return nil, err
	}

	for _, e := range events {
		if e.UID != uid {
			continue
		}

		obj, err := object(calendarSegment(p), e)
		if err != nil {
			return nil, err
		}

		// Answer at the path the client asked for, not the canonical one.
		obj.Path = path.Clean(p)

		return &obj, nil
	}

	return nil, webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("no event with UID %s", uid))
}

func (b *Backend) QueryCalendarObjects(ctx context.Context, p string, query *caldav.CalendarQuery) ([]caldav.CalendarObject, error) {
	objects, err := b.ListCalendarObjects(ctx, p, &query.CompRequest)
	if err != nil {
		return nil, err
	}

	return caldav.Filter(query, objects)
}

func (b *Backend) PutCalendarObject(ctx context.Context, p string, cal *ical.Calendar, opts *caldav.PutCalendarObjectOptions) (*caldav.CalendarObject, error) {
	id, err := b.calendarID(ctx, p)
	if err != nil {
		return nil, err
	}

	var buf strings.Builder
	if err := ical.NewEncoder(&buf).Encode(cal); err != nil {
		return nil, fmt.Errorf("encoding submitted event: %w", err)
	}

	uid := ""
	for _, e := range cal.Events() {
		if prop := e.Props.Get("UID"); prop != nil {
			uid = strings.TrimSpace(prop.Value)
		}
	}

	if uid == "" {
		return nil, webdav.NewHTTPError(http.StatusBadRequest, fmt.Errorf("event has no UID"))
	}

	if _, _, err := calendar.Put(ctx, b.conn, id, buf.String()); err != nil {
		return nil, err
	}

	// Remember where the client put it, so a later GET or DELETE at that same
	// path still resolves to this event.
	b.mu.Lock()
	b.names[path.Clean(p)] = uid
	b.mu.Unlock()

	// Our own write is the one moment we know the cache is stale.
	b.cache.Invalidate(id)

	return b.GetCalendarObject(ctx, p, nil)
}

func (b *Backend) DeleteCalendarObject(ctx context.Context, p string) error {
	id, err := b.calendarID(ctx, p)
	if err != nil {
		return err
	}

	uid, err := b.objectUID(p)
	if err != nil {
		return err
	}

	deleted, err := calendar.Delete(ctx, b.conn, id, uid)
	if err != nil {
		return err
	}

	b.mu.Lock()
	delete(b.names, path.Clean(p))
	b.mu.Unlock()

	b.cache.Invalidate(id)

	if !deleted {
		return webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("no event with UID %s", uid))
	}

	return nil
}

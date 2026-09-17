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

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"

	"github.com/develonrails/carbonate/internal/cache"
	"github.com/develonrails/carbonate/internal/calendar"
	"github.com/develonrails/carbonate/internal/davcompat"
)

// Paths served.
//
// go-webdav derives a resource's kind from its path depth, so the layout is
// not free: principal at depth 1, home set at 2, calendars at 3, objects at 4.
// Requests to "/" and /.well-known/caldav are redirected to the principal, so
// a client only needs the bare address.
const (
	prefix        = "/caldav"
	principalPath = prefix + "/principal/"
	homeSetPath   = principalPath + "calendars/"
)

// Backend serves one Proton account.
type Backend struct {
	store Store
	cache *cache.Entries[calendar.Event]

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

	// uids remembers which UID each Proton event ID was served under, so that
	// a deletion — which Proton reports by ID alone — can still be named to a
	// client that knows the event by its path.
	uids map[string]map[string]string
}

// New returns a backend reading and writing through store.
//
// Cached events are kept until Proton says the calendar has changed, so a
// change made elsewhere shows up on the next poll rather than after a timer.
func New(store Store) *Backend {
	return &Backend{
		store:  store,
		cache:  cache.New[calendar.Event](),
		tokens: make(map[string]string),
		names:  make(map[string]string),
		uids:   make(map[string]map[string]string),
	}
}

// Handler returns an http.Handler serving CalDAV for this backend.
func (b *Backend) Handler() http.Handler {
	return davcompat.Wrap(&caldav.Handler{Backend: b, Prefix: prefix}, davcompat.Options{
		SupportedReports: supportedReportSet,
		CTag:             b.ctag,
		ServeSync: func(w http.ResponseWriter, r *http.Request) bool {
			return serveSync(w, r, b.sync)
		},
	})
}

// ctag returns a token that changes whenever anything in the calendar does.
//
// Proton's calendar event loop keeps exactly such a token, and fetching it is
// one cheap request — which is the point: a ctag computed from the events
// themselves would mean listing them, the very work it exists to avoid.
func (b *Backend) ctag(ctx context.Context, p string) (string, error) {
	id, err := b.calendarID(ctx, p)
	if err != nil {
		return "", err
	}

	token, err := b.store.ChangeToken(ctx, id)
	if err != nil {
		return "", err
	}

	if token == "" {
		return "", fmt.Errorf("calendar %s reported no change token", id)
	}

	return token, nil
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
	calendars, err := b.store.Calendars(ctx)
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

// calendarSegment returns the calendar token from a path under the home set,
// or "" for anything that does not live there.
//
// Returning the wrong segment rather than nothing would send a request to some
// other calendar, so a path that does not match is rejected outright.
func calendarSegment(p string) string {
	clean := path.Clean(p) + "/"

	if !strings.HasPrefix(clean, homeSetPath) {
		return ""
	}

	rest := strings.TrimPrefix(clean, homeSetPath)

	if i := strings.Index(rest, "/"); i >= 0 {
		return rest[:i]
	}

	return rest
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

// events returns a calendar's events, refetching only when Proton's change
// token says something has moved.
//
// A client polls constantly, and decrypting a calendar on every poll would
// make the bridge unusable. Asking for the token is one cheap request, and it
// is the same one the ctag is built from.
func (b *Backend) events(ctx context.Context, calendarID string) ([]calendar.Event, error) {
	token, err := b.store.ChangeToken(ctx, calendarID)
	if err != nil {
		// Without a token there is no way to tell stale from current, so
		// serve fresh data rather than risk serving old.
		token = ""
	}

	return b.cache.Get(ctx, calendarID, token, func(ctx context.Context) ([]calendar.Event, error) {
		return b.store.Events(ctx, calendarID)
	})
}

// group collects the events sharing a UID into one CalDAV resource.
//
// A recurring event and its exceptions share a UID, and RFC 4791 §4.1 puts
// every such component in a single resource. Serving them separately would
// put two resources at one address.
func group(calendarToken string, events []calendar.Event) (caldav.CalendarObject, error) {
	parsed, err := ical.NewDecoder(strings.NewReader(calendar.Merge(events))).Decode()
	if err != nil {
		return caldav.CalendarObject{}, fmt.Errorf("re-parsing event %s: %w", events[0].UID, err)
	}

	modified := events[0].Modified
	for _, e := range events[1:] {
		if e.Modified.After(modified) {
			modified = e.Modified
		}
	}

	return caldav.CalendarObject{
		Path:    objectPath(calendarToken, events[0].UID),
		ModTime: modified,
		ETag:    etag(events),
		Data:    parsed,
	}, nil
}

// byUID groups events into resources, keeping the order they arrived in so
// that a listing is stable between calls.
func byUID(events []calendar.Event) ([]string, map[string][]calendar.Event) {
	order := make([]string, 0, len(events))
	groups := make(map[string][]calendar.Event, len(events))

	for _, e := range events {
		if _, seen := groups[e.UID]; !seen {
			order = append(order, e.UID)
		}

		groups[e.UID] = append(groups[e.UID], e)
	}

	return order, groups
}

// etag changes whenever any component of a resource is edited, which is what
// lets a client skip the ones it already has.
//
// It covers every component: editing one occurrence of a series must change
// the tag of the resource the whole series is served in.
func etag(events []calendar.Event) string {
	latest := int64(0)

	var ids strings.Builder

	for _, e := range events {
		if when := e.Modified.Unix(); when > latest {
			latest = when
		}

		ids.WriteString(e.ID)
	}

	return strconv.FormatInt(latest, 10) + "-" + token(ids.String())[:8]
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

	b.remember(id, events)

	segment := calendarSegment(p)
	order, groups := byUID(events)

	out := make([]caldav.CalendarObject, 0, len(order))

	for _, uid := range order {
		obj, err := group(segment, groups[uid])
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

	var matching []calendar.Event

	for _, e := range events {
		if e.UID == uid {
			matching = append(matching, e)
		}
	}

	if len(matching) == 0 {
		return nil, webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("no event with UID %s", uid))
	}

	obj, err := group(calendarSegment(p), matching)
	if err != nil {
		return nil, err
	}

	// Answer at the path the client asked for, not the canonical one.
	obj.Path = path.Clean(p)

	return &obj, nil
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

	_, created, err := b.store.Put(ctx, id, buf.String())
	if err != nil {
		return nil, err
	}

	if !created {
		davcompat.MarkReplaced(ctx)
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

	deleted, err := b.store.Delete(ctx, id, uid)
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

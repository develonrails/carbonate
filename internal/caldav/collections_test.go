package caldav

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// What Calendar.app sends when someone adds a calendar.
const appleMkcalendar = `<?xml version="1.0" encoding="UTF-8"?>
<B:mkcalendar xmlns:B="urn:ietf:params:xml:ns:caldav">
  <A:set xmlns:A="DAV:">
    <A:prop>
      <B:supported-calendar-component-set><B:comp name="VEVENT"/></B:supported-calendar-component-set>
      <D:calendar-color xmlns:D="http://apple.com/ns/ical/" symbolic-color="blue">#1BADF8FF</D:calendar-color>
      <A:displayname>Perso</A:displayname>
      <D:calendar-order xmlns:D="http://apple.com/ns/ical/">2</D:calendar-order>
      <B:calendar-free-busy-set><YES/></B:calendar-free-busy-set>
    </A:prop>
  </A:set>
</B:mkcalendar>`

// What macOS sends, unasked, to every CalDAV account: a list for reminders.
const remindersMkcalendar = `<?xml version="1.0" encoding="UTF-8"?>
<B:mkcalendar xmlns:B="urn:ietf:params:xml:ns:caldav">
  <A:set xmlns:A="DAV:">
    <A:prop>
      <B:supported-calendar-component-set><B:comp name="VTODO"/></B:supported-calendar-component-set>
      <A:displayname>Reminders</A:displayname>
    </A:prop>
  </A:set>
</B:mkcalendar>`

func request(b *Backend, method, target, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")

	b.Handler().ServeHTTP(rec, req)

	return rec
}

func pathOf(t *testing.T, b *Backend, name string) string {
	t.Helper()

	calendars, err := b.ListCalendars(context.Background())
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}

	for _, c := range calendars {
		if c.Name == name {
			return c.Path
		}
	}

	t.Fatalf("no calendar named %q among %d", name, len(calendars))

	return ""
}

func TestMkcalendarCreatesTheCalendarWhereTheClientPutIt(t *testing.T) {
	fake := newFake()
	b := backendWith(fake)

	target := homeSetPath + "E1A96B3B-D37A-46E3-9B84-5DB4ADBDF76E/"

	rec := request(b, "MKCALENDAR", target, appleMkcalendar)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body)
	}

	if len(fake.calendars) != 2 {
		t.Fatalf("the store holds %d calendars, want 2", len(fake.calendars))
	}

	made := fake.calendars[1]
	if made.Name != "Perso" || made.Color != "#1BADF8FF" {
		t.Errorf("created %q in %q, want Perso in #1BADF8FF", made.Name, made.Color)
	}

	// The client will look for the calendar at the address it chose.
	if got := pathOf(t, b, "Perso"); got != target {
		t.Errorf("the calendar is listed at %q, want %q", got, target)
	}

	// And will write events there.
	id, err := b.calendarID(context.Background(), target+"event.ics")
	if err != nil {
		t.Fatalf("calendarID: %v", err)
	}

	if id != made.ID {
		t.Errorf("the address resolves to %q, want %q", id, made.ID)
	}
}

// A calendar that moved on restart would look deleted to the client that made
// it, which would then drop whatever it had not yet synced.
func TestCreatedCalendarKeepsItsAddressAcrossARestart(t *testing.T) {
	fake := newFake()
	file := filepath.Join(t.TempDir(), "calendar-paths.json")
	target := homeSetPath + "chosen-by-the-client/"

	first := backendWith(fake)
	if err := first.RememberPaths(file); err != nil {
		t.Fatalf("RememberPaths: %v", err)
	}

	if rec := request(first, "MKCALENDAR", target, appleMkcalendar); rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}

	second := backendWith(fake)
	if err := second.RememberPaths(file); err != nil {
		t.Fatalf("RememberPaths: %v", err)
	}

	if got := pathOf(t, second, "Perso"); got != target {
		t.Errorf("after a restart the calendar is at %q, want %q", got, target)
	}

	// The calendar Proton already had is untouched by any of this.
	if got := pathOf(t, second, "Work"); got != homeSetPath+token("cal-1")+"/" {
		t.Errorf("the existing calendar moved to %q", got)
	}
}

// Proton Calendar has nowhere to keep a reminder, so a list made for them
// would be a calendar that can never hold what it was made for.
func TestMkcalendarRefusesAListForReminders(t *testing.T) {
	fake := newFake()

	rec := request(backendWith(fake), "MKCALENDAR", homeSetPath+"reminders/", remindersMkcalendar)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}

	if len(fake.calendars) != 1 {
		t.Errorf("the store holds %d calendars; the refused one was made anyway", len(fake.calendars))
	}
}

func TestMkcalendarRefusesAnAddressInUse(t *testing.T) {
	fake := newFake()
	b := backendWith(fake)

	existing := calendarPath(t, b)

	rec := request(b, "MKCALENDAR", existing, appleMkcalendar)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}

	if len(fake.calendars) != 1 {
		t.Errorf("the store holds %d calendars, want 1", len(fake.calendars))
	}
}

// A client that repeats the request must find the calendar, not make another.
func TestMkcalendarTwiceMakesOneCalendar(t *testing.T) {
	fake := newFake()
	b := backendWith(fake)
	target := homeSetPath + "once/"

	if rec := request(b, "MKCALENDAR", target, appleMkcalendar); rec.Code != http.StatusCreated {
		t.Fatalf("first status = %d", rec.Code)
	}

	if rec := request(b, "MKCALENDAR", target, appleMkcalendar); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("second status = %d, want 405", rec.Code)
	}

	if len(fake.calendars) != 2 {
		t.Errorf("the store holds %d calendars, want 2", len(fake.calendars))
	}
}

func TestMkcalendarRefusesAnAddressOutsideTheHomeSet(t *testing.T) {
	fake := newFake()
	b := backendWith(fake)

	for _, target := range []string{
		principalPath + "elsewhere/",
		homeSetPath + "a/b/",
		homeSetPath + "%2e%2e/",
	} {
		if rec := request(b, "MKCALENDAR", target, appleMkcalendar); rec.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403", target, rec.Code)
		}
	}

	if len(fake.calendars) != 1 {
		t.Errorf("the store holds %d calendars, want 1", len(fake.calendars))
	}
}

// The extended MKCOL that go-webdav does route takes the same road as
// MKCALENDAR, so it has to make the same calendar and refuse the same things.
func TestExtendedMkcolTakesTheSameRoad(t *testing.T) {
	fake := newFake()
	b := backendWith(fake)
	target := homeSetPath + "via-mkcol/"

	body := `<?xml version="1.0" encoding="UTF-8"?>
<D:mkcol xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:set><D:prop>
    <D:resourcetype><D:collection/><C:calendar/></D:resourcetype>
    <D:displayname>Famille</D:displayname>
  </D:prop></D:set>
</D:mkcol>`

	if rec := request(b, "MKCOL", target, body); rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}

	if got := pathOf(t, b, "Famille"); got != target {
		t.Errorf("the calendar is listed at %q, want %q", got, target)
	}

	if rec := request(b, "MKCOL", target, body); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("a second MKCOL at the same address gave %d, want 405", rec.Code)
	}

	if len(fake.calendars) != 2 {
		t.Errorf("the store holds %d calendars, want 2", len(fake.calendars))
	}
}

// With no body there is no name, and Proton refuses a calendar without one.
func TestMkcalendarWithoutABodyStillNamesTheCalendar(t *testing.T) {
	fake := newFake()

	if rec := request(backendWith(fake), "MKCALENDAR", homeSetPath+"bare/", ""); rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}

	if fake.calendars[1].Name == "" {
		t.Error("the calendar was created without a name")
	}
}

func TestProppatchRenamesAndRecolours(t *testing.T) {
	fake := newFake()
	b := backendWith(fake)

	body := `<?xml version="1.0" encoding="UTF-8"?>
<A:propertyupdate xmlns:A="DAV:">
  <A:set><A:prop>
    <A:displayname>Travail</A:displayname>
    <D:calendar-color xmlns:D="http://apple.com/ns/ical/">#FF2968FF</D:calendar-color>
  </A:prop></A:set>
</A:propertyupdate>`

	rec := request(b, "PROPPATCH", calendarPath(t, b), body)
	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("status = %d, want 207: %s", rec.Code, rec.Body)
	}

	if got := fake.calendars[0]; got.Name != "Travail" || got.Color != "#FF2968FF" {
		t.Errorf("the calendar is %q in %q", got.Name, got.Color)
	}

	if strings.Contains(rec.Body.String(), "403") {
		t.Errorf("something was refused: %s", rec.Body)
	}
}

// Calendar.app sets calendar-order on every calendar it finds. carbonate has
// nowhere to keep it, and has to say so in a form the client will read: it
// refuses to parse an error that arrives as plain text.
func TestProppatchRefusesWhatItCannotKeepByName(t *testing.T) {
	fake := newFake()
	b := backendWith(fake)

	body := `<?xml version="1.0" encoding="UTF-8"?>
<A:propertyupdate xmlns:A="DAV:">
  <A:set><A:prop>
    <D:calendar-order xmlns:D="http://apple.com/ns/ical/">0</D:calendar-order>
  </A:prop></A:set>
</A:propertyupdate>`

	rec := request(b, "PROPPATCH", calendarPath(t, b), body)
	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("status = %d, want 207", rec.Code)
	}

	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "xml") {
		t.Errorf("Content-Type = %q, want XML", ct)
	}

	got := rec.Body.String()
	if !strings.Contains(got, `<calendar-order xmlns="http://apple.com/ns/ical/"/>`) || !strings.Contains(got, "403 Forbidden") {
		t.Errorf("the refusal does not name the property: %s", got)
	}

	if fake.calendars[0].Name != "Work" {
		t.Errorf("the calendar was renamed to %q by a request that named no name", fake.calendars[0].Name)
	}
}

func TestProppatchRefusesAnEmptyName(t *testing.T) {
	fake := newFake()
	b := backendWith(fake)

	body := `<A:propertyupdate xmlns:A="DAV:"><A:set><A:prop><A:displayname></A:displayname></A:prop></A:set></A:propertyupdate>`

	rec := request(b, "PROPPATCH", calendarPath(t, b), body)
	if rec.Code != http.StatusMultiStatus || !strings.Contains(rec.Body.String(), "403 Forbidden") {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}

	if fake.calendars[0].Name != "Work" {
		t.Errorf("the calendar lost its name: %q", fake.calendars[0].Name)
	}
}

// A client told no colour picks one itself and writes it back, repainting the
// calendar in Proton. So the colour Proton holds has to be reported.
func TestPropfindReportsTheColourProtonHolds(t *testing.T) {
	fake := newFake()
	fake.calendars[0].Color = "#EC3E7C"
	b := backendWith(fake)

	body := `<?xml version="1.0" encoding="UTF-8"?>
<A:propfind xmlns:A="DAV:"><A:prop>
  <A:displayname/>
  <D:calendar-color xmlns:D="http://apple.com/ns/ical/"/>
</A:prop></A:propfind>`

	for _, target := range []string{homeSetPath, calendarPath(t, b)} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("PROPFIND", target, strings.NewReader(body))
		req.Header.Set("Content-Type", "text/xml; charset=utf-8")
		req.Header.Set("Depth", "1")

		b.Handler().ServeHTTP(rec, req)

		if rec.Code != http.StatusMultiStatus {
			t.Fatalf("%s: status = %d: %s", target, rec.Code, rec.Body)
		}

		got := rec.Body.String()

		if !strings.Contains(got, `<calendar-color xmlns="http://apple.com/ns/ical/">#EC3E7CFF</calendar-color>`) {
			t.Errorf("%s: the colour is not reported: %s", target, got)
		}

		// Once, for the calendar: not for the home set above it, nor for
		// the events inside it.
		if n := strings.Count(got, "calendar-color"); n != 2 {
			t.Errorf("%s: calendar-color appears %d times, want one element: %s", target, n, got)
		}
	}
}

// An address freed by deleting a calendar in Proton must not stay taken.
func TestAddressOfADeletedCalendarIsForgotten(t *testing.T) {
	fake := newFake()
	b := backendWith(fake)
	target := homeSetPath + "short-lived/"

	if rec := request(b, "MKCALENDAR", target, appleMkcalendar); rec.Code != http.StatusCreated {
		t.Fatalf("status = %d", rec.Code)
	}

	gone := fake.calendars[1].ID
	fake.calendars = fake.calendars[:1]

	if _, err := b.ListCalendars(context.Background()); err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}

	if _, kept := b.paths.segment(gone); kept {
		t.Error("the address of a calendar that no longer exists is still remembered")
	}
}

// A listing that began before a calendar was created does not know about it,
// and must not be allowed to forget its address.
func TestAListingTakenBeforeACreationDoesNotForgetIt(t *testing.T) {
	p := &paths{byID: make(map[string]string)}

	before := p.version()

	if err := p.set("new", "chosen"); err != nil {
		t.Fatalf("set: %v", err)
	}

	p.keep(map[string]bool{"old": true}, before)

	if _, kept := p.segment("new"); !kept {
		t.Error("a stale listing made carbonate forget a calendar it had just created")
	}
}

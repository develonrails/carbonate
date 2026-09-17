package davcompat

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// This is the shape go-webdav emits: one element holding two privileges. A
// client reading only the first child concludes the calendar is read-only.
const malformed = `<?xml version="1.0" encoding="UTF-8"?>` +
	`<multistatus xmlns="DAV:"><response xmlns="DAV:"><propstat xmlns="DAV:"><prop xmlns="DAV:">` +
	`<current-user-privilege-set xmlns="DAV:"><privilege xmlns="DAV:">` +
	`<read xmlns="DAV:"></read><write xmlns="DAV:"></write>` +
	`</privilege></current-user-privilege-set></prop></propstat></response></multistatus>`

func TestSplitPrivilegesGivesEachItsOwnElement(t *testing.T) {
	got := string(splitPrivileges([]byte(malformed)))

	if n := strings.Count(got, "<privilege"); n != 2 {
		t.Errorf("found %d privilege elements, want 2:\n%s", n, got)
	}

	for _, want := range []string{
		`<privilege xmlns="DAV:"><read xmlns="DAV:"></read></privilege>`,
		`<privilege xmlns="DAV:"><write xmlns="DAV:"></write></privilege>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output is missing %s:\n%s", want, got)
		}
	}
}

// A privilege element that is already correct must be left alone.
func TestSplitPrivilegesLeavesSingleAlone(t *testing.T) {
	in := `<current-user-privilege-set><privilege><read></read></privilege></current-user-privilege-set>`

	if got := string(splitPrivileges([]byte(in))); got != in {
		t.Errorf("a well-formed privilege was rewritten:\ngot  %s\nwant %s", got, in)
	}
}

func TestSplitPrivilegesHandlesSelfClosingChildren(t *testing.T) {
	in := `<privilege><read/><write/></privilege>`

	got := string(splitPrivileges([]byte(in)))

	if n := strings.Count(got, "<privilege"); n != 2 {
		t.Errorf("self-closing children were not split: %s", got)
	}
}

func TestSplitPrivilegesIgnoresUnrelatedXML(t *testing.T) {
	in := `<multistatus><response><href>/x/</href></response></multistatus>`

	if got := string(splitPrivileges([]byte(in))); got != in {
		t.Errorf("unrelated XML was modified:\ngot  %s\nwant %s", got, in)
	}
}

func TestAllowWithWrites(t *testing.T) {
	got := allowWithWrites("OPTIONS, PROPFIND, REPORT, DELETE, MKCOL")

	for _, want := range []string{"PUT", "GET", "HEAD", "PROPFIND", "DELETE"} {
		if !strings.Contains(got, want) {
			t.Errorf("Allow %q is missing %s", got, want)
		}
	}
}

// Adding a method twice would produce a malformed header.
func TestAllowWithWritesDoesNotDuplicate(t *testing.T) {
	got := allowWithWrites("OPTIONS, PUT, GET")

	if n := strings.Count(got, "PUT"); n != 1 {
		t.Errorf("PUT appears %d times in %q, want 1", n, got)
	}
}

func TestAllowWithWritesFromEmpty(t *testing.T) {
	if got := allowWithWrites(""); !strings.Contains(got, "PUT") {
		t.Errorf("Allow from empty = %q, want it to include PUT", got)
	}
}

// The middleware buffers the body, so it must still send status, headers and
// content through unchanged for responses it does not rewrite.
func TestCompatPassesResponsesThrough(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"abc"`)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte("hello"))
	})

	rec := httptest.NewRecorder()
	Wrap(inner, Options{}).ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/x", nil))

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusCreated {
		t.Errorf("status = %d, want 201", res.StatusCode)
	}

	if res.Header.Get("ETag") != `"abc"` {
		t.Errorf("ETag = %q, want \"abc\"", res.Header.Get("ETag"))
	}

	body, _ := io.ReadAll(res.Body)
	if string(body) != "hello" {
		t.Errorf("body = %q, want %q", body, "hello")
	}
}

// A handler that never calls WriteHeader still means 200.
func TestCompatDefaultsToOK(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("body"))
	})

	rec := httptest.NewRecorder()
	Wrap(inner, Options{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if rec.Result().StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Result().StatusCode)
	}
}

func TestCompatRewritesXMLBody(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml; charset=utf-8")
		w.WriteHeader(http.StatusMultiStatus)
		w.Write([]byte(malformed))
	})

	rec := httptest.NewRecorder()
	Wrap(inner, Options{}).ServeHTTP(rec, httptest.NewRequest("PROPFIND", "/x", nil))

	body, _ := io.ReadAll(rec.Result().Body)

	if n := strings.Count(string(body), "<privilege"); n != 2 {
		t.Errorf("privileges were not split in the served body:\n%s", body)
	}
}

// A non-XML body must not be touched, even if it happens to contain markup.
func TestCompatLeavesNonXMLAlone(t *testing.T) {
	ics := "BEGIN:VCALENDAR\r\nSUMMARY:<privilege><read/><write/></privilege>\r\nEND:VCALENDAR\r\n"

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/calendar")
		w.Write([]byte(ics))
	})

	rec := httptest.NewRecorder()
	Wrap(inner, Options{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x.ics", nil))

	body, _ := io.ReadAll(rec.Result().Body)
	if string(body) != ics {
		t.Errorf("calendar data was rewritten:\ngot  %q\nwant %q", body, ics)
	}
}

func TestCompatAddsPutToOptions(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", "OPTIONS, PROPFIND, REPORT, DELETE, MKCOL")
		w.WriteHeader(http.StatusNoContent)
	})

	rec := httptest.NewRecorder()
	Wrap(inner, Options{}).ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/x/", nil))

	if allow := rec.Result().Header.Get("Allow"); !strings.Contains(allow, "PUT") {
		t.Errorf("Allow = %q, want it to include PUT", allow)
	}
}

func TestStripCTagRemovesTheProperty(t *testing.T) {
	out, found := stripProp([]byte(mixedRequest), "getctag")

	if !found {
		t.Fatal("getctag was not detected")
	}

	if strings.Contains(string(out), "getctag") {
		t.Errorf("getctag survived the strip:\n%s", out)
	}

	// The other requested properties must be left alone.
	for _, want := range []string{"displayname", "resourcetype"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("stripping getctag also removed %s:\n%s", want, out)
		}
	}
}

// go-webdav will not answer a request for no properties at all, so a prop
// element emptied by the strip needs something harmless putting back.
func TestStripCTagFillsAnEmptiedProp(t *testing.T) {
	out, found := stripProp([]byte(ctagRequest), "getctag")

	if !found {
		t.Fatal("getctag was not detected")
	}

	out = fillEmptyProp(out)

	if !strings.Contains(string(out), "resourcetype") {
		t.Errorf("emptied prop was not given a replacement property:\n%s", out)
	}
}

func TestStripCTagLeavesOtherRequestsAlone(t *testing.T) {
	in := `<?xml version="1.0"?><d:propfind xmlns:d="DAV:"><d:prop><d:getetag/></d:prop></d:propfind>`

	out, found := stripProp([]byte(in), "getctag")

	if found {
		t.Error("getctag was reported in a request that does not ask for it")
	}

	if string(out) != in {
		t.Errorf("an unrelated request was rewritten:\ngot  %s\nwant %s", out, in)
	}
}

// getetag must not be mistaken for getctag; they differ by one letter and one
// would silently shadow the other.
func TestStripCTagDoesNotMatchGetETag(t *testing.T) {
	if _, found := stripProp([]byte(`<d:prop><d:getetag/></d:prop>`), "getctag"); found {
		t.Error("getetag was treated as getctag")
	}
}

func TestInjectCTagAddsItToTheCollection(t *testing.T) {
	got := string(injectProp([]byte(twoResponses), "/caldav/principal/calendars/abc/", `<getctag xmlns="http://calendarserver.org/ns/">token-123</getctag>`))

	if !strings.Contains(got, `<getctag xmlns="http://calendarserver.org/ns/">token-123</getctag>`) {
		t.Errorf("ctag was not injected:\n%s", got)
	}
}

// Only the collection has a ctag. Putting one on each event would have a
// client believe every event is an unchanging collection of its own.
func TestInjectCTagLeavesMembersAlone(t *testing.T) {
	got := string(injectProp([]byte(twoResponses), "/caldav/principal/calendars/abc/", `<getctag xmlns="http://calendarserver.org/ns/">token-123</getctag>`))

	// Count opening tags: the closing tag repeats the name.
	if n := strings.Count(got, "<getctag"); n != 1 {
		t.Errorf("getctag appears %d times, want 1:\n%s", n, got)
	}

	// The event's own response must be untouched.
	if !strings.Contains(got, `<getetag xmlns="DAV:">"x"</getetag>`) {
		t.Errorf("the member response was damaged:\n%s", got)
	}
}

func TestInjectCTagMatchesRegardlessOfTrailingSlash(t *testing.T) {
	for _, p := range []string{
		"/caldav/principal/calendars/abc/",
		"/caldav/principal/calendars/abc",
	} {
		got := string(injectProp([]byte(twoResponses), p, `<getctag xmlns="http://calendarserver.org/ns/">token-123</getctag>`))

		if !strings.Contains(got, "token-123") {
			t.Errorf("no ctag injected for path %q", p)
		}
	}
}

func TestInjectCTagIgnoresAForeignPath(t *testing.T) {
	got := string(injectProp([]byte(twoResponses), "/caldav/principal/calendars/other/", `<getctag xmlns="http://calendarserver.org/ns/">token-123</getctag>`))

	if strings.Contains(got, "<getctag") {
		t.Errorf("a ctag was injected into an unrelated collection:\n%s", got)
	}
}

// Evolution Data Server — and so GNOME Calendar — reads the collection's
// properties from the first propstat it is shown and stops there. A ctag in a
// propstat of its own is a ctag it never sees, and a calendar that never
// refreshes.
func TestInjectedPropertiesJoinTheSuccessfulPropstat(t *testing.T) {
	got := string(injectProp([]byte(twoResponses), "/caldav/principal/calendars/abc/", `<getctag xmlns="http://calendarserver.org/ns/">token-123</getctag>`))

	first := got[:strings.Index(got, "</propstat>")]

	if !strings.Contains(first, "<getctag") {
		t.Errorf("the ctag was not in the first propstat:\n%s", got)
	}

	// The property joined a propstat rather than bringing its own.
	if n := strings.Count(got, "<propstat"); n != strings.Count(twoResponses, "<propstat") {
		t.Errorf("propstat count changed from %d to %d:\n%s", strings.Count(twoResponses, "<propstat"), n, got)
	}
}

// A resource for which nothing succeeded has no prop list to join, so one is
// started — ahead of the propstat that failed, which a client may stop at.
func TestInjectedPropertyLeadsWhenNothingSucceeded(t *testing.T) {
	failed := `<?xml version="1.0" encoding="UTF-8"?>` +
		`<multistatus xmlns="DAV:"><response xmlns="DAV:"><href>/caldav/principal/calendars/abc/</href>` +
		`<propstat xmlns="DAV:"><prop xmlns="DAV:"><displayname xmlns="DAV:"></displayname></prop><status>HTTP/1.1 404 Not Found</status></propstat>` +
		`</response></multistatus>`

	got := string(injectProp([]byte(failed), "/caldav/principal/calendars/abc/", `<getctag xmlns="http://calendarserver.org/ns/">token-123</getctag>`))

	if !strings.Contains(got[:strings.Index(got, "</propstat>")], "<getctag") {
		t.Errorf("the ctag did not lead:\n%s", got)
	}

	if !strings.Contains(got, "404 Not Found") {
		t.Errorf("the failing propstat was lost:\n%s", got)
	}
}

const ctagRequest = `<?xml version="1.0"?>
<d:propfind xmlns:d="DAV:" xmlns:cs="http://calendarserver.org/ns/"><d:prop><cs:getctag/></d:prop></d:propfind>`

const mixedRequest = `<?xml version="1.0"?>
<d:propfind xmlns:d="DAV:" xmlns:cs="http://calendarserver.org/ns/"><d:prop><d:displayname/><cs:getctag/><d:resourcetype/></d:prop></d:propfind>`

const twoResponses = `<?xml version="1.0" encoding="UTF-8"?>` +
	`<multistatus xmlns="DAV:">` +
	`<response xmlns="DAV:"><href>/caldav/principal/calendars/abc/</href>` +
	`<propstat xmlns="DAV:"><prop xmlns="DAV:"><resourcetype xmlns="DAV:"></resourcetype></prop><status>HTTP/1.1 200 OK</status></propstat>` +
	`</response>` +
	`<response xmlns="DAV:"><href>/caldav/principal/calendars/abc/event.ics</href>` +
	`<propstat xmlns="DAV:"><prop xmlns="DAV:"><getetag xmlns="DAV:">"x"</getetag></prop><status>HTTP/1.1 200 OK</status></propstat>` +
	`</response>` +
	`</multistatus>`

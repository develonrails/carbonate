package caldav

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const ctagRequest = `<?xml version="1.0"?>
<d:propfind xmlns:d="DAV:" xmlns:cs="http://calendarserver.org/ns/"><d:prop><cs:getctag/></d:prop></d:propfind>`

const mixedRequest = `<?xml version="1.0"?>
<d:propfind xmlns:d="DAV:" xmlns:cs="http://calendarserver.org/ns/"><d:prop><d:displayname/><cs:getctag/><d:resourcetype/></d:prop></d:propfind>`

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

const twoResponses = `<?xml version="1.0" encoding="UTF-8"?>` +
	`<multistatus xmlns="DAV:">` +
	`<response xmlns="DAV:"><href>/caldav/principal/calendars/abc/</href>` +
	`<propstat xmlns="DAV:"><prop xmlns="DAV:"><resourcetype xmlns="DAV:"></resourcetype></prop><status>HTTP/1.1 200 OK</status></propstat>` +
	`</response>` +
	`<response xmlns="DAV:"><href>/caldav/principal/calendars/abc/event.ics</href>` +
	`<propstat xmlns="DAV:"><prop xmlns="DAV:"><getetag xmlns="DAV:">"x"</getetag></prop><status>HTTP/1.1 200 OK</status></propstat>` +
	`</response>` +
	`</multistatus>`

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

// A token is opaque and may contain characters that would break the XML.
// A change token is opaque and may contain characters that would break the
// XML around it.
func TestCTagValueIsEscaped(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		w.Write([]byte(twoResponses))
	})

	req := httptest.NewRequest("PROPFIND", "/caldav/principal/calendars/abc/", strings.NewReader(ctagRequest))
	rec := httptest.NewRecorder()

	compat(inner, func(context.Context, string) (string, error) { return `a<b&c"d`, nil }, nil).ServeHTTP(rec, req)

	body, _ := io.ReadAll(rec.Result().Body)

	if strings.Contains(string(body), `a<b&c`) {
		t.Errorf("the token was not escaped:\n%s", body)
	}

	if !strings.Contains(string(body), "&lt;") {
		t.Errorf("expected escaped entities in:\n%s", body)
	}
}

// Everything advertised must be something carbonate actually serves: a client
// that asks for a report it was promised and gets a rejection has no recourse.
func TestSupportedReportsMatchWhatIsServed(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		w.Write([]byte(twoResponses))
	})

	request := `<?xml version="1.0"?><d:propfind xmlns:d="DAV:"><d:prop><d:supported-report-set/></d:prop></d:propfind>`

	req := httptest.NewRequest("PROPFIND", "/caldav/principal/calendars/abc/", strings.NewReader(request))
	rec := httptest.NewRecorder()

	compat(inner, nil, nil).ServeHTTP(rec, req)

	body, _ := io.ReadAll(rec.Result().Body)

	for _, want := range []string{"calendar-query", "calendar-multiget", "sync-collection"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("%s was not advertised:\n%s", want, body)
		}
	}
}

// A failure to read the change token must not fail the whole PROPFIND: the
// client can still sync, just less efficiently.
func TestCompatSurvivesACTagError(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml; charset=utf-8")
		w.WriteHeader(http.StatusMultiStatus)
		w.Write([]byte(twoResponses))
	})

	failing := func(context.Context, string) (string, error) {
		return "", errors.New("Proton is unreachable")
	}

	req := httptest.NewRequest("PROPFIND", "/caldav/principal/calendars/abc/", strings.NewReader(ctagRequest))
	rec := httptest.NewRecorder()

	compat(inner, failing, nil).ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusMultiStatus {
		t.Errorf("status = %d, want 207", res.StatusCode)
	}

	body, _ := io.ReadAll(res.Body)
	if strings.Contains(string(body), "<getctag") {
		t.Errorf("a ctag was served despite the lookup failing:\n%s", body)
	}
}

// The request reaching go-webdav must no longer mention getctag, or it would
// answer 404 for it and the response would carry two verdicts.
func TestCompatStripsTheRequestBeforeForwarding(t *testing.T) {
	var forwarded string

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		forwarded = string(body)

		w.Header().Set("Content-Type", "text/xml")
		w.Write([]byte(twoResponses))
	})

	req := httptest.NewRequest("PROPFIND", "/caldav/principal/calendars/abc/", strings.NewReader(mixedRequest))
	rec := httptest.NewRecorder()

	compat(inner, func(context.Context, string) (string, error) { return "t", nil }, nil).ServeHTTP(rec, req)

	if strings.Contains(forwarded, "getctag") {
		t.Errorf("getctag was forwarded to go-webdav:\n%s", forwarded)
	}

	if !strings.Contains(forwarded, "displayname") {
		t.Errorf("other properties were lost on the way:\n%s", forwarded)
	}
}

// Only PROPFIND carries a prop list; other methods must pass through untouched.
func TestCompatDoesNotTouchOtherMethods(t *testing.T) {
	var forwarded string

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		forwarded = string(body)
	})

	req := httptest.NewRequest(http.MethodPut, "/caldav/principal/calendars/abc/x.ics", strings.NewReader("BEGIN:VCALENDAR"))
	rec := httptest.NewRecorder()

	compat(inner, nil, nil).ServeHTTP(rec, req)

	if forwarded != "BEGIN:VCALENDAR" {
		t.Errorf("a PUT body was rewritten: %q", forwarded)
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

// Everything carbonate supplies must land together, or a client that stops
// after the first propstat learns half of it.
func TestSuppliedPropertiesArriveTogether(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		w.Write([]byte(twoResponses))
	})

	request := `<?xml version="1.0"?><d:propfind xmlns:d="DAV:" xmlns:cs="http://calendarserver.org/ns/">` +
		`<d:prop><cs:getctag/><d:supported-report-set/></d:prop></d:propfind>`

	req := httptest.NewRequest("PROPFIND", "/caldav/principal/calendars/abc/", strings.NewReader(request))
	rec := httptest.NewRecorder()

	compat(inner, func(context.Context, string) (string, error) { return "token-123", nil }, nil).ServeHTTP(rec, req)

	body, _ := io.ReadAll(rec.Result().Body)

	first := string(body)[:strings.Index(string(body), "</propstat>")]

	for _, want := range []string{"getctag", "sync-collection"} {
		if !strings.Contains(first, want) {
			t.Errorf("%s was not in the first propstat:\n%s", want, body)
		}
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

package caldav

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/develonrails/carbonate/internal/davcompat"
)

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

	wrapped(inner, func(context.Context, string) (string, error) { return `a<b&c"d`, nil }).ServeHTTP(rec, req)

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

	wrapped(inner, nil).ServeHTTP(rec, req)

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

	wrapped(inner, failing).ServeHTTP(rec, req)

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

	wrapped(inner, func(context.Context, string) (string, error) { return "t", nil }).ServeHTTP(rec, req)

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

	wrapped(inner, nil).ServeHTTP(rec, req)

	if forwarded != "BEGIN:VCALENDAR" {
		t.Errorf("a PUT body was rewritten: %q", forwarded)
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

	wrapped(inner, func(context.Context, string) (string, error) { return "token-123", nil }).ServeHTTP(rec, req)

	body, _ := io.ReadAll(rec.Result().Body)

	first := string(body)[:strings.Index(string(body), "</propstat>")]

	for _, want := range []string{"getctag", "sync-collection"} {
		if !strings.Contains(first, want) {
			t.Errorf("%s was not in the first propstat:\n%s", want, body)
		}
	}
}

// wrapped builds the handler the way the backend does, so these exercise the
// calendar's own options rather than the shared corrections.
func wrapped(inner http.Handler, ctag davcompat.CTagFunc) http.Handler {
	return davcompat.Wrap(inner, davcompat.Options{
		SupportedReports: supportedReportSet,
		CTag:             ctag,
	})
}

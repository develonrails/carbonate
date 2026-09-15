package caldav

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
	compat(inner, nil, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/x", nil))

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
	compat(inner, nil, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

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
	compat(inner, nil, nil).ServeHTTP(rec, httptest.NewRequest("PROPFIND", "/x", nil))

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
	compat(inner, nil, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x.ics", nil))

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
	compat(inner, nil, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/x/", nil))

	if allow := rec.Result().Header.Get("Allow"); !strings.Contains(allow, "PUT") {
		t.Errorf("Allow = %q, want it to include PUT", allow)
	}
}

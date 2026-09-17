package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLoggedRecordsARequest(t *testing.T) {
	var log bytes.Buffer

	handler := Logged(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMultiStatus)
	}), &log)

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("PROPFIND", "/caldav/principal/", nil))

	for _, want := range []string{"PROPFIND", "/caldav/principal/", "207"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("log is missing %q: %s", want, log.String())
		}
	}
}

// The reason a request failed is the whole point of the log. A 500 without it
// says no more than the silence it replaced.
func TestLoggedIncludesTheReasonForAFailure(t *testing.T) {
	var log bytes.Buffer

	handler := Logged(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "decoding event abc: no key packet", http.StatusInternalServerError)
	}), &log)

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("REPORT", "/caldav/principal/calendars/x/", nil))

	if !strings.Contains(log.String(), "no key packet") {
		t.Errorf("the failure was logged without its reason: %s", log.String())
	}
}

// A successful response is the calendar itself, and nobody debugging a sync
// wants it scrolling past.
func TestLoggedOmitsSuccessfulBodies(t *testing.T) {
	var log bytes.Buffer

	handler := Logged(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("BEGIN:VCALENDAR"))
	}), &log)

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/caldav/principal/calendars/x/e.ics", nil))

	if strings.Contains(log.String(), "VCALENDAR") {
		t.Errorf("a successful body was logged: %s", log.String())
	}
}

// The client must get exactly what the handler wrote, logging or not.
func TestLoggedPassesTheResponseThrough(t *testing.T) {
	var log bytes.Buffer

	handler := Logged(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}), &log)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("REPORT", "/caldav/", nil))

	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "boom") {
		t.Errorf("the response was altered: %d %q", rec.Code, rec.Body.String())
	}
}

// Without -log the bridge stays as it was, rather than writing somewhere
// nobody is looking.
func TestLoggedWithoutAWriterIsTheHandlerItself(t *testing.T) {
	inner := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})

	if got := Logged(inner, nil); got == nil {
		t.Fatal("Logged returned nothing")
	}

	rec := httptest.NewRecorder()
	Logged(inner, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

// Every line carries the time, whichever part of the bridge wrote it. Lines
// that stamp themselves and lines that do not, mixed in one log, read as a
// fault in the log.
func TestStampedPrefixesEveryLine(t *testing.T) {
	var out bytes.Buffer

	w := Stamped(&out)

	w.Write([]byte("first\n"))
	w.Write([]byte("second\n"))

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %q", len(lines), out.String())
	}

	for _, line := range lines {
		if len(line) < len(stampFormat) {
			t.Fatalf("line has no stamp: %q", line)
		}

		if _, err := time.Parse(stampFormat, line[:len(stampFormat)]); err != nil {
			t.Errorf("line does not begin with a time: %q", line)
		}
	}
}

// A writer may hand over a line in pieces, or several at once. Only the start
// of a line gets a stamp.
func TestStampedHandlesPartialAndMultipleLines(t *testing.T) {
	var out bytes.Buffer

	w := Stamped(&out)

	w.Write([]byte("split "))
	w.Write([]byte("across writes\n"))
	w.Write([]byte("two\nat once\n"))

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3: %q", len(lines), out.String())
	}

	if !strings.HasSuffix(lines[0], "split across writes") {
		t.Errorf("a line split across writes was stamped twice: %q", lines[0])
	}

	for _, line := range lines[1:] {
		if _, err := time.Parse(stampFormat, line[:len(stampFormat)]); err != nil {
			t.Errorf("line does not begin with a time: %q", line)
		}
	}
}

// The caller wrote what it meant to; reporting the stamped length would look
// like a short write and make an io.Writer contract failure.
func TestStampedReportsTheCallersLength(t *testing.T) {
	var out bytes.Buffer

	p := []byte("a line\n")

	n, err := Stamped(&out).Write(p)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	if n != len(p) {
		t.Errorf("wrote %d, want %d", n, len(p))
	}
}

// Logging off must stay off rather than become a writer to nowhere.
func TestStampedPassesNilThrough(t *testing.T) {
	if Stamped(nil) != nil {
		t.Error("a nil writer was wrapped")
	}
}

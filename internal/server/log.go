package server

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Logged writes one line per request, so that a failure is visible to whoever
// is running the bridge and not only to the client that received it.
//
// Without this, a broken sync is silent from the server's side: a DAV client
// meeting a 500 reports it as a calendar that will not update, or as nothing
// at all, and the reason stays on the other side of the connection. The body
// of a failed response is included because that is the part worth reading — a
// 500 with no reason is no better than silence.
//
// Successful responses are logged without their bodies. They are the calendar,
// and nobody debugging a sync wants it scrolling past.
func Logged(next http.Handler, out io.Writer) http.Handler {
	if out == nil {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		rec := &logRecorder{ResponseWriter: w}

		next.ServeHTTP(rec, r)

		if rec.status == 0 {
			rec.status = http.StatusOK
		}

		line := fmt.Sprintf("%-9s %s %d %s", r.Method, r.URL.Path, rec.status, took(time.Since(started)))

		if reason := strings.TrimSpace(rec.failure.String()); reason != "" {
			line += ": " + firstLine(reason)
		}

		fmt.Fprintln(out, line)
	})
}

// took renders a duration short enough to sit at the end of a log line.
func took(d time.Duration) string {
	if d < time.Millisecond {
		return "<1ms"
	}

	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}

	return fmt.Sprintf("%.1fs", d.Seconds())
}

// firstLine keeps a log line to one line, however many the error had.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}

	return s
}

// maxFailure bounds how much of a failed response is kept. Enough for the
// reason, not enough to bury the log under an XML error document.
const maxFailure = 400

// logRecorder notes the status, and keeps the body only when the request
// failed.
type logRecorder struct {
	http.ResponseWriter

	status  int
	failure bytes.Buffer
}

func (r *logRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *logRecorder) Write(p []byte) (int, error) {
	if r.status >= http.StatusBadRequest && r.failure.Len() < maxFailure {
		r.failure.Write(p[:min(len(p), maxFailure-r.failure.Len())])
	}

	return r.ResponseWriter.Write(p)
}

// stamped puts the time in front of every line written through it.
//
// The alternative is for each place that logs to stamp its own lines, which
// is three places today and the next one added will forget. Doing it where
// the lines converge means nothing can.
type stamped struct {
	to io.Writer

	mu sync.Mutex

	// atLineStart tracks whether the next byte begins a line, since a writer
	// may hand over a line in pieces or several lines at once.
	atLineStart bool
}

// Stamped returns a writer that prefixes each line with the time. A nil
// writer is passed through, so that logging stays off.
func Stamped(to io.Writer) io.Writer {
	if to == nil {
		return nil
	}

	return &stamped{to: to, atLineStart: true}
}

func (s *stamped) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().Format(stampFormat)

	var out bytes.Buffer

	for _, b := range p {
		// An empty line gets no stamp: it is spacing, not an event.
		if s.atLineStart && b != '\n' {
			out.WriteString(now)
			out.WriteByte(' ')

			s.atLineStart = false
		}

		out.WriteByte(b)

		if b == '\n' {
			s.atLineStart = true
		}
	}

	if _, err := s.to.Write(out.Bytes()); err != nil {
		return 0, err
	}

	// Report the caller's length, not the stamped one: it wrote what it meant
	// to, and a short write here would look like a failure.
	return len(p), nil
}

// stampFormat is the time of day. The date is left out: a bridge is watched
// while it runs, and a log line that repeats today's date on every row is
// mostly noise.
const stampFormat = "15:04:05"

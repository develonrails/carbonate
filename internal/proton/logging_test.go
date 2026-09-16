package proton_test

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/develonrails/carbonate/internal/proton"
)

// stderrDuring captures what is written to stderr while fn runs.
//
// It replaces the variable rather than the file descriptor because that is
// what the HTTP client reads: its default logger is built over os.Stderr when
// the client is created, which happens inside fn.
func stderrDuring(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe: %v", err)
	}

	real := os.Stderr
	os.Stderr = w

	done := make(chan string, 1)

	go func() {
		out, _ := io.ReadAll(r)
		done <- string(out)
	}()

	fn()

	os.Stderr = real

	if err := w.Close(); err != nil {
		t.Fatalf("closing the pipe: %v", err)
	}

	return <-done
}

// A failed request is carbonate's to explain. The library narrating it as well
// means one problem arrives as three lines, two of them addressed to nobody.
func TestFailedRequestIsNotNarratedByTheLibrary(t *testing.T) {
	newTestServer(t)

	out := stderrDuring(t, func() {
		if _, err := proton.Login(context.Background(), testUsername, []byte("wrong"), &stubPrompter{}); err == nil {
			t.Error("Login with a wrong password succeeded")
		}
	})

	if out != "" {
		t.Errorf("a failed login wrote to stderr on its own:\n%s", out)
	}
}

// Silence is the default, not the only option: the flag that already exists
// for diagnosing API drift is where the commentary belongs.
func TestDebugRestoresTheLibrarysLogging(t *testing.T) {
	newTestServer(t)
	t.Setenv("CARBONATE_DEBUG", "1")

	out := stderrDuring(t, func() {
		if _, err := proton.Login(context.Background(), testUsername, []byte("wrong"), &stubPrompter{}); err == nil {
			t.Error("Login with a wrong password succeeded")
		}
	})

	if !strings.Contains(out, "RESTY") {
		t.Errorf("CARBONATE_DEBUG did not restore the library's logging, got:\n%s", out)
	}
}

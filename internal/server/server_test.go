package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func reached() (http.Handler, *bool) {
	got := false

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = true

		w.Write([]byte("ok"))
	}), &got
}

func TestAuthenticatedAcceptsCorrectCredentials(t *testing.T) {
	inner, got := reached()

	req := httptest.NewRequest(http.MethodGet, "/caldav/", nil)
	req.SetBasicAuth("user@proton.me", "bridge-password")

	rec := httptest.NewRecorder()
	Authenticated("user@proton.me", "bridge-password", inner).ServeHTTP(rec, req)

	if !*got {
		t.Error("a correct password did not reach the handler")
	}

	if rec.Result().StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Result().StatusCode)
	}
}

// The listener is on loopback without TLS, so this is what stops any other
// process on the machine from reading the calendar.
func TestAuthenticatedRejectsBadCredentials(t *testing.T) {
	cases := map[string]struct{ user, pass string }{
		"wrong password": {"user@proton.me", "guess"},
		"wrong user":     {"someone@else", "bridge-password"},
		"empty password": {"user@proton.me", ""},
		"both empty":     {"", ""},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			inner, got := reached()

			req := httptest.NewRequest(http.MethodGet, "/caldav/", nil)
			req.SetBasicAuth(c.user, c.pass)

			rec := httptest.NewRecorder()
			Authenticated("user@proton.me", "bridge-password", inner).ServeHTTP(rec, req)

			if *got {
				t.Error("the handler was reached despite bad credentials")
			}

			if rec.Result().StatusCode != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rec.Result().StatusCode)
			}
		})
	}
}

func TestAuthenticatedRejectsMissingCredentials(t *testing.T) {
	inner, got := reached()

	rec := httptest.NewRecorder()
	Authenticated("user@proton.me", "bridge-password", inner).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/caldav/", nil))

	if *got {
		t.Error("an unauthenticated request reached the handler")
	}

	res := rec.Result()

	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", res.StatusCode)
	}

	// Without this header a client has no way to know it should ask for a
	// password, and simply reports a failure.
	if auth := res.Header.Get("WWW-Authenticate"); !strings.Contains(auth, "Basic") {
		t.Errorf("WWW-Authenticate = %q, want a Basic challenge", auth)
	}
}

func TestEqualComparesFully(t *testing.T) {
	if !Equal("same", "same") {
		t.Error("equal strings compared unequal")
	}

	for _, pair := range [][2]string{
		{"secret", "secrez"},
		{"secret", "secre"},
		{"secret", "secrets"},
		{"", "x"},
		{"x", ""},
	} {
		if Equal(pair[0], pair[1]) {
			t.Errorf("Equal(%q, %q) was true", pair[0], pair[1])
		}
	}
}

func TestIndexNamesBothCollections(t *testing.T) {
	rec := httptest.NewRecorder()
	Index(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	body, _ := io.ReadAll(rec.Result().Body)

	for _, want := range []string{"/caldav/", "/carddav/"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the index does not mention %s:\n%s", want, body)
		}
	}
}

// Anything not served must be a clear 404 rather than the index text, or a
// client hunting for a collection would take the reply as success.
func TestIndexIsNotACatchAll(t *testing.T) {
	rec := httptest.NewRecorder()
	Index(rec, httptest.NewRequest(http.MethodGet, "/somewhere-else", nil))

	if rec.Result().StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Result().StatusCode)
	}
}

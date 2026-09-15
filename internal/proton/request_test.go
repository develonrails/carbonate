package proton

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// newConn returns a Conn pointed at a test server, with credentials set as
// Resume would have left them.
func newConn(t *testing.T, handler http.HandlerFunc) *Conn {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	t.Setenv("CARBONATE_API_URL", server.URL)

	conn := &Conn{}
	conn.setCredentials("test-uid", "test-token")

	return conn
}

func TestGetDecodesJSON(t *testing.T) {
	conn := newConn(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"Code": 1000, "Name": "value"})
	})

	var res struct {
		Code int
		Name string
	}

	if err := conn.Get(context.Background(), "/thing", &res); err != nil {
		t.Fatalf("Get: %v", err)
	}

	if res.Name != "value" {
		t.Errorf("Name = %q, want %q", res.Name, "value")
	}
}

func TestRequestsCarryIdentity(t *testing.T) {
	var uid, auth, version string

	conn := newConn(t, func(w http.ResponseWriter, r *http.Request) {
		uid = r.Header.Get("x-pm-uid")
		auth = r.Header.Get("Authorization")
		version = r.Header.Get("x-pm-appversion")

		w.Write([]byte(`{}`))
	})

	if err := conn.Get(context.Background(), "/thing", &struct{}{}); err != nil {
		t.Fatalf("Get: %v", err)
	}

	if uid != "test-uid" {
		t.Errorf("x-pm-uid = %q", uid)
	}

	if auth != "Bearer test-token" {
		t.Errorf("Authorization = %q", auth)
	}

	// Proton rejects clients whose version it does not recognise, so this
	// header is never optional.
	if version == "" {
		t.Error("x-pm-appversion was not sent")
	}
}

func TestPutSendsJSONBody(t *testing.T) {
	var got, contentType string

	conn := newConn(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = string(body)
		contentType = r.Header.Get("Content-Type")

		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}

		w.Write([]byte(`{"Code":1000}`))
	})

	if err := conn.Put(context.Background(), "/thing", map[string]string{"Key": "value"}, nil); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if !strings.Contains(got, `"Key":"value"`) {
		t.Errorf("body = %q", got)
	}

	if contentType != "application/json" {
		t.Errorf("Content-Type = %q", contentType)
	}
}

// Proton explains a rejection in the body, not the status line, so the body
// has to reach the caller or the error is useless.
func TestErrorIncludesProtonsReason(t *testing.T) {
	conn := newConn(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"Code":2061,"Error":"Not a valid ID"}`))
	})

	err := conn.Get(context.Background(), "/thing", &struct{}{})
	if err == nil {
		t.Fatal("a 400 was reported as success")
	}

	if !strings.Contains(err.Error(), "Not a valid ID") {
		t.Errorf("error %q does not carry Proton's reason", err)
	}
}

// Raw requests hold a snapshot of the access token. When it expires the
// library refreshes its own, and this must pick the new one up rather than
// failing for the rest of the daemon's life.
func TestUnauthorisedIsRetriedAfterRefresh(t *testing.T) {
	var calls atomic.Int32

	conn := newConn(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"Code":401}`))

			return
		}

		w.Write([]byte(`{"Code":1000}`))
	})

	// With no library client there is nothing to refresh through, so the
	// attempt is reported rather than the 401 being passed off as success.
	err := conn.Get(context.Background(), "/thing", &struct{}{})
	if err == nil {
		t.Fatal("a 401 was reported as success")
	}

	if !strings.Contains(err.Error(), "refresh") {
		t.Errorf("error %q does not mention the refresh attempt", err)
	}

	if calls.Load() != 1 {
		t.Errorf("made %d requests, want 1 before giving up", calls.Load())
	}
}

// When there is a client to refresh through, the request is tried again and
// the second answer is the one that counts.
func TestUnauthorisedSucceedsOnSecondAttempt(t *testing.T) {
	var calls atomic.Int32

	handler := func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.Write([]byte(`{"Code":1000,"Name":"after refresh"}`))
		}
	}

	server := httptest.NewServer(http.HandlerFunc(handler))
	defer server.Close()

	t.Setenv("CARBONATE_API_URL", server.URL)

	conn := &Conn{}
	conn.setCredentials("uid", "token")

	// Stand in for go-proton-api's refresh by swapping the credentials the way
	// the auth handler would.
	conn.refresh = func(context.Context) error {
		conn.setCredentials("uid", "fresh-token")

		return nil
	}

	var res struct{ Name string }

	if err := conn.Get(context.Background(), "/thing", &res); err != nil {
		t.Fatalf("Get: %v", err)
	}

	if res.Name != "after refresh" {
		t.Errorf("Name = %q, want the retried answer", res.Name)
	}

	if calls.Load() != 2 {
		t.Errorf("made %d requests, want 2", calls.Load())
	}
}

func TestGetWithNilOutIgnoresBody(t *testing.T) {
	conn := newConn(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`not json at all`))
	})

	if err := conn.Put(context.Background(), "/thing", nil, nil); err != nil {
		t.Fatalf("Put with no result: %v", err)
	}
}

func TestMalformedJSONIsAnError(t *testing.T) {
	conn := newConn(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{oh dear`))
	})

	if err := conn.Get(context.Background(), "/thing", &struct{}{}); err == nil {
		t.Error("malformed JSON was accepted")
	}
}

func TestNewUnauthSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/auth/v4/sessions" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}

		json.NewEncoder(w).Encode(map[string]string{"UID": "u", "AccessToken": "a"})
	}))
	defer server.Close()

	sess, err := newUnauthSession(context.Background(), server.URL, "test@1.0.0")
	if err != nil {
		t.Fatalf("newUnauthSession: %v", err)
	}

	if sess == nil || sess.UID != "u" || sess.AccessToken != "a" {
		t.Errorf("session = %+v", sess)
	}
}

// The fake Proton server used in tests predates anonymous sessions. A server
// without the endpoint simply does not need one.
func TestNewUnauthSessionToleratesMissingEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()

	sess, err := newUnauthSession(context.Background(), server.URL, "test@1.0.0")
	if err != nil {
		t.Fatalf("a missing endpoint was treated as a failure: %v", err)
	}

	if sess != nil {
		t.Errorf("session = %+v, want nil", sess)
	}
}

// Any other failure is real and must not be swallowed.
func TestNewUnauthSessionReportsRealFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	if _, err := newUnauthSession(context.Background(), server.URL, "test@1.0.0"); err == nil {
		t.Error("a 500 was treated as a server that needs no session")
	}
}

func TestNewUnauthSessionRejectsIncompleteResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"UID": "u"})
	}))
	defer server.Close()

	if _, err := newUnauthSession(context.Background(), server.URL, "test@1.0.0"); err == nil {
		t.Error("a session with no access token was accepted")
	}
}

func TestAppVersionOverride(t *testing.T) {
	if appVersion() != defaultAppVersion {
		t.Errorf("appVersion() = %q, want the default", appVersion())
	}

	t.Setenv("CARBONATE_APP_VERSION", "custom@9.9.9")

	if got := appVersion(); got != "custom@9.9.9" {
		t.Errorf("appVersion() = %q, want the override", got)
	}
}

func TestHostURLFallsBackToProduction(t *testing.T) {
	if hostURL() == "" {
		t.Error("hostURL() is empty with no override")
	}

	t.Setenv("CARBONATE_API_URL", "http://example.invalid")

	if got := hostURL(); got != "http://example.invalid" {
		t.Errorf("hostURL() = %q, want the override", got)
	}
}

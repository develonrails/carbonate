package proton_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/develonrails/carbonate/internal/proton"
)

// Resuming while the network is still coming up — the case at login — must
// read as offline, whether the host does not resolve yet or nothing answers.
func TestResumeUnreachableIsOffline(t *testing.T) {
	newTestServer(t)

	sess := login(t)

	// A server that is gone leaves a port nothing listens on.
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()

	for name, url := range map[string]string{
		"no DNS":  "http://carbonate.invalid",
		"refused": gone.URL,
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("CARBONATE_API_URL", url)

			conn, err := proton.Resume(context.Background(), &fakeStore{sess: sess})
			if err == nil {
				conn.Close()
				t.Fatal("Resume succeeded with nothing to reach")
			}

			if !proton.Offline(err) {
				t.Errorf("Offline(%v) = false, want true", err)
			}
		})
	}
}

// Proton answering no is not being offline: retrying would only ask again.
func TestResumeRefusedIsNotOffline(t *testing.T) {
	s, userID := newTestServer(t)

	sess := login(t)

	if err := s.RevokeUser(userID); err != nil {
		t.Fatalf("RevokeUser: %v", err)
	}

	conn, err := proton.Resume(context.Background(), &fakeStore{sess: sess})
	if err == nil {
		conn.Close()
		t.Fatal("Resume succeeded after the session was revoked")
	}

	if proton.Offline(err) {
		t.Errorf("Offline(%v) = true, want false", err)
	}
}

func TestOfflineNil(t *testing.T) {
	if proton.Offline(nil) {
		t.Error("Offline(nil) = true")
	}
}

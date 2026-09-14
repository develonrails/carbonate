package proton_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ProtonMail/go-proton-api/server"

	"github.com/develonrails/carbonate/internal/proton"
	"github.com/develonrails/carbonate/internal/session"
)

const (
	testUsername = "alice"
	testPassword = "password"
)

// newTestServer starts an in-process fake Proton API with one user and points
// carbonate at it for the duration of the test.
func newTestServer(t *testing.T) (*server.Server, string) {
	t.Helper()

	// Plain HTTP: the fake server's TLS cert is self-signed and go-proton-api
	// verifies certificates.
	s := server.New(server.WithTLS(false))
	t.Cleanup(s.Close)

	userID, _, err := s.CreateUser(testUsername, []byte(testPassword))
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	t.Setenv("CARBONATE_API_URL", s.GetHostURL())

	return s, userID
}

// stubPrompter answers interactive prompts without a terminal. It records what
// it was asked, so tests can assert that login did not prompt unnecessarily.
type stubPrompter struct {
	password []byte
	line     string

	passwordPrompts []string
	linePrompts     []string
}

func (p *stubPrompter) Password(prompt string) ([]byte, error) {
	p.passwordPrompts = append(p.passwordPrompts, prompt)
	return p.password, nil
}

func (p *stubPrompter) Line(prompt string) (string, error) {
	p.linePrompts = append(p.linePrompts, prompt)
	return p.line, nil
}

func TestLogin(t *testing.T) {
	_, userID := newTestServer(t)

	p := &stubPrompter{}

	sess, err := proton.Login(context.Background(), testUsername, []byte(testPassword), p)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	if sess.Username != testUsername {
		t.Errorf("Username = %q, want %q", sess.Username, testUsername)
	}

	if sess.UserID != userID {
		t.Errorf("UserID = %q, want %q", sess.UserID, userID)
	}

	if sess.UID == "" || sess.RefreshToken == "" {
		t.Error("session is missing UID or refresh token")
	}

	if string(sess.MailboxPassword) != testPassword {
		t.Errorf("MailboxPassword = %q, want %q", sess.MailboxPassword, testPassword)
	}

	// Single-password account with no 2FA: nothing should have been asked.
	if len(p.passwordPrompts) != 0 || len(p.linePrompts) != 0 {
		t.Errorf("unexpected prompts: passwords=%v lines=%v", p.passwordPrompts, p.linePrompts)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	newTestServer(t)

	_, err := proton.Login(context.Background(), testUsername, []byte("wrong"), &stubPrompter{})
	if err == nil {
		t.Fatal("Login with a wrong password succeeded")
	}
}

func TestLoginUnknownUser(t *testing.T) {
	newTestServer(t)

	_, err := proton.Login(context.Background(), "nobody", []byte(testPassword), &stubPrompter{})
	if err == nil {
		t.Fatal("Login as an unknown user succeeded")
	}
}

func TestResume(t *testing.T) {
	newTestServer(t)

	sess := login(t)

	conn, err := proton.Resume(context.Background(), sess, func(string, string) error { return nil })
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	defer conn.Close()

	if conn.UserKR == nil || conn.UserKR.CountDecryptionEntities() == 0 {
		t.Error("Resume returned no usable decryption keys")
	}

	user, err := conn.Client.GetUser(context.Background())
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}

	if user.ID != sess.UserID {
		t.Errorf("resumed as user %q, want %q", user.ID, sess.UserID)
	}
}

// Proton rotates the refresh token on every refresh and discards the old one.
// If we fail to persist the new token the user is locked out, so Resume must
// hand it over before doing anything else that could fail.
func TestResumePersistsRotatedToken(t *testing.T) {
	newTestServer(t)

	sess := login(t)
	original := sess.RefreshToken

	var gotUID, gotToken string
	var calls int

	conn, err := proton.Resume(context.Background(), sess, func(uid, token string) error {
		calls++
		gotUID, gotToken = uid, token
		return nil
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	defer conn.Close()

	if calls == 0 {
		t.Fatal("Resume never persisted a token")
	}

	if gotUID == "" || gotToken == "" {
		t.Error("persisted an empty UID or token")
	}

	if gotToken == original {
		t.Error("persisted the old refresh token; rotation was not captured")
	}
}

// A failure to persist must abort the connection rather than continue with a
// token we cannot write down.
func TestResumeFailsWhenPersistFails(t *testing.T) {
	newTestServer(t)

	sess := login(t)
	sentinel := errors.New("disk full")

	conn, err := proton.Resume(context.Background(), sess, func(string, string) error { return sentinel })
	if err == nil {
		conn.Close()
		t.Fatal("Resume succeeded despite a failing persist callback")
	}

	if !errors.Is(err, sentinel) {
		t.Errorf("error = %v, want it to wrap %v", err, sentinel)
	}
}

func TestResumeWithBadRefreshToken(t *testing.T) {
	newTestServer(t)

	sess := login(t)
	sess.RefreshToken = "not-a-real-token"

	conn, err := proton.Resume(context.Background(), sess, func(string, string) error { return nil })
	if err == nil {
		conn.Close()
		t.Fatal("Resume succeeded with a bogus refresh token")
	}
}

// A wrong mailbox password cannot be detected by the API: keys are unlocked
// locally, and go-proton-api silently skips keys it cannot open. Resume must
// notice the resulting empty keyring.
func TestResumeWrongMailboxPassword(t *testing.T) {
	newTestServer(t)

	sess := login(t)
	sess.MailboxPassword = []byte("not-the-mailbox-password")

	conn, err := proton.Resume(context.Background(), sess, func(string, string) error { return nil })
	if err == nil {
		conn.Close()
		t.Fatal("Resume succeeded with a wrong mailbox password")
	}

	if !errors.Is(err, proton.ErrWrongMailboxPassword) {
		t.Errorf("error = %v, want ErrWrongMailboxPassword", err)
	}
}

func TestResumeAfterRevocation(t *testing.T) {
	s, userID := newTestServer(t)

	sess := login(t)

	if err := s.RevokeUser(userID); err != nil {
		t.Fatalf("RevokeUser: %v", err)
	}

	conn, err := proton.Resume(context.Background(), sess, func(string, string) error { return nil })
	if err == nil {
		conn.Close()
		t.Fatal("Resume succeeded after the session was revoked")
	}
}

func login(t *testing.T) *session.Session {
	t.Helper()

	sess, err := proton.Login(context.Background(), testUsername, []byte(testPassword), &stubPrompter{})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	return sess
}

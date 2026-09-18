package protontest

import (
	"context"
	"testing"

	"github.com/develonrails/carbonate/internal/proton"
	"github.com/develonrails/carbonate/internal/session"
)

// Username and Password are the account Connect creates. Tests that only want
// a working connection need not care what they are.
const (
	Username = "carbonate-test"
	Password = "carbonate-test-password"
)

// Connect creates an account on the fake Proton, logs in, and returns a
// connection with the keys unlocked.
func (s *Server) Connect(t *testing.T) *proton.Conn {
	t.Helper()

	s.CreateUser(t, Username, Password)

	ctx := context.Background()

	sess, err := proton.Login(ctx, Username, []byte(Password), silent{})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	conn, err := proton.Resume(ctx, &memoryStore{sess: sess})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}

	t.Cleanup(conn.Close)

	return conn
}

// silent answers the prompts a test account never triggers. A prompt reached
// by accident should fail the login rather than hang or silently succeed.
type silent struct{}

func (silent) Password(string) ([]byte, error) { return []byte(Password), nil }
func (silent) Line(string) (string, error)     { return "", errUnexpectedPrompt }
func (silent) Verify(string) (string, error)   { return "", errUnexpectedPrompt }

// memoryStore keeps the session for the length of a test.
type memoryStore struct {
	sess *session.Session
}

func (m *memoryStore) Session() *session.Session { return m.sess }

func (m *memoryStore) Update(uid, refreshToken string) error {
	m.sess.UID, m.sess.RefreshToken = uid, refreshToken

	return nil
}

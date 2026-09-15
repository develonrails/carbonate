package proton

import (
	"errors"
	"strings"
	"testing"

	api "github.com/ProtonMail/go-proton-api"

	"github.com/develonrails/carbonate/internal/session"
)

// recordingPrompter answers prompts and remembers what it was asked, so a
// test can tell whether the user was troubled for something unnecessary.
type recordingPrompter struct {
	password []byte
	asked    []string
}

func (p *recordingPrompter) Password(prompt string) ([]byte, error) {
	p.asked = append(p.asked, prompt)

	return p.password, nil
}

func (p *recordingPrompter) Line(prompt string) (string, error) {
	p.asked = append(p.asked, prompt)

	return "", nil
}

// Most accounts use one password for both jobs, and asking for a second one
// would be a question the user cannot answer.
func TestMailboxPasswordReusedInOnePasswordMode(t *testing.T) {
	p := &recordingPrompter{password: []byte("should not be used")}

	got, err := mailboxPasswordFor(api.Auth{PasswordMode: api.OnePasswordMode}, []byte("login"), p)
	if err != nil {
		t.Fatalf("mailboxPasswordFor: %v", err)
	}

	if string(got) != "login" {
		t.Errorf("mailbox password = %q, want the login password", got)
	}

	if len(p.asked) != 0 {
		t.Errorf("the user was asked for a password they do not have: %v", p.asked)
	}
}

// In two-password mode the login password cannot decrypt anything, so it must
// be asked for separately rather than assumed.
func TestMailboxPasswordAskedInTwoPasswordMode(t *testing.T) {
	p := &recordingPrompter{password: []byte("mailbox-secret")}

	got, err := mailboxPasswordFor(api.Auth{PasswordMode: api.TwoPasswordMode}, []byte("login"), p)
	if err != nil {
		t.Fatalf("mailboxPasswordFor: %v", err)
	}

	if string(got) != "mailbox-secret" {
		t.Errorf("mailbox password = %q, want the one that was asked for", got)
	}

	if len(p.asked) != 1 {
		t.Fatalf("asked %d times, want once", len(p.asked))
	}

	// The prompt has to name what it wants, or the user types the wrong one.
	if !strings.Contains(strings.ToLower(p.asked[0]), "mailbox") {
		t.Errorf("prompt %q does not say which password is wanted", p.asked[0])
	}
}

// A prompter that cannot answer must stop the login rather than let it carry
// on to fail at the unlock, where the cause is no longer visible.
func TestMailboxPasswordPropagatesAFailedPrompt(t *testing.T) {
	sentinel := errors.New("no way to ask")

	_, err := mailboxPasswordFor(api.Auth{PasswordMode: api.TwoPasswordMode}, []byte("login"), failingPrompter{err: sentinel})
	if !errors.Is(err, sentinel) {
		t.Errorf("error = %v, want it to carry the prompter's failure", err)
	}
}

type failingPrompter struct{ err error }

func (p failingPrompter) Password(string) ([]byte, error) { return nil, p.err }
func (p failingPrompter) Line(string) (string, error)     { return "", p.err }

// Proton answers a request the session may no longer make with a bare
// complaint about scope, which reads as a permissions fault. It is really an
// accounting one: too many sessions were left behind.
func TestExplainScopeNamesTheRealCause(t *testing.T) {
	err := explainScope(errors.New("403 GET /core/v4/keys/salts: Access token does not have sufficient scope (Code=9101, Status=403)"))

	if !errors.Is(err, ErrSessionLostScope) {
		t.Errorf("error = %v, want it to wrap ErrSessionLostScope", err)
	}

	if !strings.Contains(err.Error(), "log in again") {
		t.Errorf("error %q does not say what to do about it", err)
	}
}

// Every other failure must pass through untouched, or a real permissions
// problem would be reported as a session that needs renewing.
func TestExplainScopeLeavesOtherErrorsAlone(t *testing.T) {
	original := errors.New("422 POST /calendar/v1: Invalid event data (Code=2000)")

	if got := explainScope(original); got != original {
		t.Errorf("an unrelated error was rewritten: %v", got)
	}

	if explainScope(nil) != nil {
		t.Error("nil was turned into an error")
	}
}

// Resuming a session rotates its refresh token and discards the old one, so a
// callback that does nothing leaves whatever is saved afterwards holding a
// token Proton has already thrown away.
func TestTrackWritesRotatedTokensBack(t *testing.T) {
	s := &session.Session{UID: "old-uid", RefreshToken: "old-token"}

	if err := Track(s)("new-uid", "new-token"); err != nil {
		t.Fatalf("Track: %v", err)
	}

	if s.UID != "new-uid" || s.RefreshToken != "new-token" {
		t.Errorf("session = %+v, want the rotated values", s)
	}
}

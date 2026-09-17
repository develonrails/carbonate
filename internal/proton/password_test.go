package proton

import (
	"errors"
	"strings"
	"testing"

	api "github.com/ProtonMail/go-proton-api"
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

func (p *recordingPrompter) Verify(string) (string, error) { return "", nil }

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
func (p failingPrompter) Verify(string) (string, error)   { return "", p.err }

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

// The version carbonate reports is a guess that Proton happens to accept.
// When it stops, the message must name the knob rather than leave someone
// guessing at a login failure.
func TestExplainVersionNamesTheEnvironmentVariable(t *testing.T) {
	err := explainVersion(errors.New("400 POST /auth/v4/info: This version of the app is no longer supported (Code=5003)"))

	if !errors.Is(err, ErrAppVersionRejected) {
		t.Errorf("error = %v, want it to wrap ErrAppVersionRejected", err)
	}

	if !strings.Contains(err.Error(), "CARBONATE_APP_VERSION") {
		t.Errorf("error %q does not name the variable to set", err)
	}
}

func TestExplainVersionLeavesOtherErrorsAlone(t *testing.T) {
	original := errors.New("401 POST /auth/v4: Incorrect login credentials (Code=8002)")

	if got := explainVersion(original); got != original {
		t.Errorf("an unrelated error was rewritten: %v", got)
	}

	if explainVersion(nil) != nil {
		t.Error("nil was turned into an error")
	}
}

// Proton checks the platform and product, not the version. A name without the
// separator is refused (2064), and so is a product carbonate is not allowed to
// claim — so this string cannot be tidied into something that reads better.
func TestAppVersionKeepsProtonsShape(t *testing.T) {
	version := appVersion()

	platform, rest, found := strings.Cut(version, "-")
	if !found || platform == "" {
		t.Fatalf("app version %q has no platform before the dash", version)
	}

	product, number, found := strings.Cut(rest, "@")
	if !found || product == "" || number == "" {
		t.Fatalf("app version %q is not product@version", version)
	}

	// "bridge" is refused with 8004 and "calendar" with 5002; "mail" is the
	// one the API accepts.
	if product != "mail" {
		t.Errorf("product = %q, which Proton does not accept from us", product)
	}
}

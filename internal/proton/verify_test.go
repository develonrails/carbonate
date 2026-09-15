package proton

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	api "github.com/ProtonMail/go-proton-api"
)

// verifyPrompter records what it was shown and answers with a fixed token.
type verifyPrompter struct {
	answer string
	shown  []string
}

func (p *verifyPrompter) Password(string) ([]byte, error) { return []byte("password"), nil }
func (p *verifyPrompter) Line(string) (string, error)     { return "", nil }

func (p *verifyPrompter) Verify(message string) (string, error) {
	p.shown = append(p.shown, message)

	return p.answer, nil
}

func hvDetails() *api.APIHVDetails {
	return &api.APIHVDetails{Methods: []string{"captcha"}, Token: "challenge-token"}
}

// The URL is what a person is sent to, so everything needed to answer the
// challenge has to be in it.
func TestVerificationURLCarriesTheChallenge(t *testing.T) {
	got := verificationURL(hvDetails())

	if !strings.HasPrefix(got, verifyBaseURL) {
		t.Errorf("URL %q does not point at Proton's verification page", got)
	}

	for _, want := range []string{"token=challenge-token", "methods=captcha"} {
		if !strings.Contains(got, want) {
			t.Errorf("URL %q is missing %q", got, want)
		}
	}

	// Embedded mode expects a host page to talk back to, and carbonate has
	// no browser to provide one.
	if !strings.Contains(got, "embed=false") {
		t.Errorf("URL %q does not ask for the standalone page", got)
	}
}

func TestVerificationURLEscapesTheToken(t *testing.T) {
	got := verificationURL(&api.APIHVDetails{Methods: []string{"captcha", "email"}, Token: "a token&with=trouble"})

	if strings.Contains(got, "a token&with=trouble") {
		t.Errorf("the token was put in the URL unescaped: %s", got)
	}

	if !strings.Contains(got, "methods=captcha%2Cemail") {
		t.Errorf("several methods were not joined and escaped: %s", got)
	}
}

// The retry sends the answer, not the challenge.
func TestSolvedCarriesTheAnswer(t *testing.T) {
	got := solved(hvDetails(), "  the-answer  ")

	if got.Token != "the-answer" {
		t.Errorf("token = %q, want the answer, trimmed", got.Token)
	}

	if len(got.Methods) != 1 || got.Methods[0] != "captcha" {
		t.Errorf("methods = %v, want the ones Proton asked for", got.Methods)
	}
}

// Without a method the header would not say what kind of token it is.
func TestSolvedFallsBackToCaptcha(t *testing.T) {
	got := solved(&api.APIHVDetails{Token: "x"}, "answer")

	if len(got.Methods) == 0 {
		t.Error("no method was set, so the token has no stated kind")
	}
}

func TestHumanVerificationIgnoresOtherFailures(t *testing.T) {
	if got := humanVerification(errors.New("network is down")); got != nil {
		t.Errorf("an unrelated failure was read as a challenge: %+v", got)
	}

	if got := humanVerification(nil); got != nil {
		t.Errorf("success was read as a challenge: %+v", got)
	}
}

// verifyingServer stands in for Proton demanding human verification.
func verifyingServer(t *testing.T) string {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/auth/v4/sessions") {
			json.NewEncoder(w).Encode(map[string]string{"UID": "u", "AccessToken": "a"})

			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)

		json.NewEncoder(w).Encode(map[string]any{
			"Code":    9001,
			"Error":   "Human verification required",
			"Details": map[string]any{"HumanVerificationToken": "challenge-token", "HumanVerificationMethods": []string{"captcha"}},
		})
	}))

	t.Cleanup(server.Close)
	t.Setenv("CARBONATE_API_URL", server.URL)

	return server.URL
}

// The point of all this: a person must be shown where to go, rather than
// meeting a login that failed for no stated reason.
func TestLoginAsksForVerification(t *testing.T) {
	verifyingServer(t)

	p := &verifyPrompter{}

	_, err := Login(context.Background(), "someone", []byte("password"), p)
	if err == nil {
		t.Fatal("login succeeded against a server demanding verification")
	}

	if len(p.shown) != 1 {
		t.Fatalf("asked for verification %d times, want once", len(p.shown))
	}

	message := p.shown[0]

	for _, want := range []string{verifyBaseURL, "challenge-token", "captcha"} {
		if !strings.Contains(message, want) {
			t.Errorf("the message shown is missing %q:\n%s", want, message)
		}
	}
}

// Refusing, or having nothing to answer with, must say what happened rather
// than surface as an unexplained login failure.
func TestLoginReportsUnansweredVerification(t *testing.T) {
	verifyingServer(t)

	_, err := Login(context.Background(), "someone", []byte("password"), &verifyPrompter{})
	if !errors.Is(err, ErrVerificationNeeded) {
		t.Errorf("error = %v, want it to name the verification as the cause", err)
	}
}

// With an answer in hand the login is attempted again, which is the only way
// the retry can differ from the attempt that failed.
//
// What cannot be checked here is the retry succeeding: Proton demands
// verification at the SRP step, which needs a signed modulus no test server
// can produce. That half is exercised by go-proton-api, not by carbonate.
func TestLoginRetriesWhenAnswered(t *testing.T) {
	attempts := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/auth/v4/sessions") {
			json.NewEncoder(w).Encode(map[string]string{"UID": "u", "AccessToken": "a"})

			return
		}

		if strings.HasSuffix(r.URL.Path, "/auth/v4/info") {
			attempts++
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)

		json.NewEncoder(w).Encode(map[string]any{
			"Code":    9001,
			"Error":   "Human verification required",
			"Details": map[string]any{"HumanVerificationToken": "challenge-token", "HumanVerificationMethods": []string{"captcha"}},
		})
	}))
	defer server.Close()

	t.Setenv("CARBONATE_API_URL", server.URL)

	_, _ = Login(context.Background(), "someone", []byte("password"), &verifyPrompter{answer: "the-answer"})

	if attempts < 2 {
		t.Errorf("logged in %d times, want a second attempt carrying the answer", attempts)
	}
}

// Answering must not be attempted twice: a person who solved the challenge
// once should not be sent round again on the same failure.
func TestVerificationIsAskedForOnce(t *testing.T) {
	verifyingServer(t)

	p := &verifyPrompter{answer: "the-answer"}

	_, _ = Login(context.Background(), "someone", []byte("password"), p)

	if len(p.shown) != 1 {
		t.Errorf("asked %d times, want once", len(p.shown))
	}
}

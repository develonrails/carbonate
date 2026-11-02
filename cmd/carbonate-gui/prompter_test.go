//go:build gtk

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/develonrails/carbonate/internal/proton"
)

// fakeAsker stands in for the window, recording what it was asked.
type fakeAsker struct {
	captchas []string
	tokens   []string
	codes    []bool

	err error
}

func (a *fakeAsker) confirmCaptcha(link string) error {
	a.captchas = append(a.captchas, link)

	return a.err
}

func (a *fakeAsker) verificationToken(link string) (string, error) {
	a.tokens = append(a.tokens, link)

	return "token", a.err
}

func (a *fakeAsker) twoFactorCode(stale bool) (string, error) {
	a.codes = append(a.codes, stale)

	return "654321", a.err
}

// captchaServer stands in for Proton demanding a CAPTCHA at every login.
func captchaServer(t *testing.T) {
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
}

// The window used to be handed Proton's instructions as a toast, which cut
// them off before the link. It has to be offered the page to open instead,
// and never asked for a token a CAPTCHA does not give.
func TestCaptchaOffersThePage(t *testing.T) {
	captchaServer(t)

	ask := &fakeAsker{}

	_, _ = proton.Login(context.Background(), "someone", []byte("password"), &guiPrompter{ask: ask})

	if len(ask.captchas) == 0 {
		t.Fatal("the window was never offered the puzzle")
	}

	if link := ask.captchas[0]; !strings.HasPrefix(link, "https://verify.proton.me/") || !strings.Contains(link, "challenge-token") {
		t.Errorf("link = %q, want Proton's page for this challenge", link)
	}

	if len(ask.tokens) != 0 {
		t.Errorf("asked for a token a CAPTCHA does not give: %v", ask.tokens)
	}
}

// Closing the dialog is a choice, and the window tells it from a failure by
// the error alone.
func TestCancellingTheCaptchaSaysSo(t *testing.T) {
	captchaServer(t)

	_, err := proton.Login(context.Background(), "someone", []byte("password"), &guiPrompter{ask: &fakeAsker{err: errCancelled}})
	if !errors.Is(err, errCancelled) {
		t.Errorf("error = %v, want it to say the person cancelled", err)
	}
}

func TestTypedCodeIsUsedWithoutAsking(t *testing.T) {
	ask := &fakeAsker{}
	p := &guiPrompter{twoFactor: "123456", ask: ask}

	code, err := p.Line("Two-factor code: ")
	if err != nil || code != "123456" {
		t.Fatalf("code = %q, err = %v; want the one typed", code, err)
	}

	if len(ask.codes) != 0 {
		t.Error("asked for a code that had already been typed")
	}
}

func TestMissingCodeIsAskedFor(t *testing.T) {
	ask := &fakeAsker{}
	p := &guiPrompter{ask: ask}

	code, err := p.Line("Two-factor code: ")
	if err != nil || code != "654321" {
		t.Fatalf("code = %q, err = %v; want the one asked for", code, err)
	}

	if len(ask.codes) != 1 || ask.codes[0] {
		t.Errorf("asked %v, want once, and not as if a code had expired", ask.codes)
	}
}

// A code lasts thirty seconds and a puzzle takes longer, so the one typed
// before it would fail the login the puzzle was solved for.
func TestCodeTypedBeforeThePuzzleIsAskedForAgain(t *testing.T) {
	ask := &fakeAsker{}
	p := &guiPrompter{twoFactor: "123456", ask: ask}

	if err := p.ConfirmCaptcha("Open this link:\n  https://verify.proton.me/?token=t\n"); err != nil {
		t.Fatal(err)
	}

	code, err := p.Line("Two-factor code: ")
	if err != nil || code != "654321" {
		t.Fatalf("code = %q, err = %v; want a fresh one", code, err)
	}

	if len(ask.codes) != 1 || !ask.codes[0] {
		t.Errorf("asked %v, want once, saying the typed code has expired", ask.codes)
	}
}

func TestLinkIn(t *testing.T) {
	for message, want := range map[string]string{
		"Open this link and complete the puzzle:\n  https://verify.proton.me/?methods=captcha&token=x\n\nDone.": "https://verify.proton.me/?methods=captcha&token=x",
		"nothing to open here": "",
	} {
		if got := linkIn(message); got != want {
			t.Errorf("linkIn(%q) = %q, want %q", message, got, want)
		}
	}
}

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/develonrails/carbonate/internal/session"
)

func TestRunVersion(t *testing.T) {
	var out bytes.Buffer

	if err := run(context.Background(), []string{"version"}, &out); err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := strings.TrimSpace(out.String()); got != version {
		t.Errorf("version output = %q, want %q", got, version)
	}
}

func TestRunHelp(t *testing.T) {
	for _, arg := range []string{"help", "-h", "--help"} {
		t.Run(arg, func(t *testing.T) {
			var out bytes.Buffer

			if err := run(context.Background(), []string{arg}, &out); err != nil {
				t.Fatalf("run: %v", err)
			}

			for _, want := range []string{"carbonate auth", "carbonate serve", "CARBONATE_BRIDGE_PASSWORD"} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("help output is missing %q", want)
				}
			}
		})
	}
}

func TestRunNoArgs(t *testing.T) {
	err := run(context.Background(), nil, &bytes.Buffer{})

	if !errors.Is(err, errUsage) {
		t.Errorf("run with no arguments: got %v, want errUsage", err)
	}
}

func TestRunUnknownCommand(t *testing.T) {
	err := run(context.Background(), []string{"frobnicate"}, &bytes.Buffer{})

	if err == nil {
		t.Fatal("unknown command succeeded")
	}

	if !strings.Contains(err.Error(), "frobnicate") {
		t.Errorf("error %q does not name the offending command", err)
	}
}

// "-h" on a subcommand is a request for help, not an error.
func TestSubcommandHelpIsNotAnError(t *testing.T) {
	for _, cmd := range []string{"auth", "serve"} {
		t.Run(cmd, func(t *testing.T) {
			var out bytes.Buffer

			if err := run(context.Background(), []string{cmd, "-h"}, &out); err != nil {
				t.Fatalf("run %s -h: %v", cmd, err)
			}

			if !strings.Contains(out.String(), "-session") {
				t.Errorf("%s -h did not document -session: %q", cmd, out.String())
			}
		})
	}
}

func TestAuthRequiresUsername(t *testing.T) {
	err := run(context.Background(), []string{"auth"}, &bytes.Buffer{})

	if err == nil {
		t.Fatal("auth without a username succeeded")
	}

	if !strings.Contains(err.Error(), "username") {
		t.Errorf("error %q does not mention the missing username", err)
	}
}

func newStore(t *testing.T) (*sessionStore, string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "auth.json")
	sess := &session.Session{
		Username:        "alice",
		UserID:          "user-id",
		UID:             "old-uid",
		RefreshToken:    "old-token",
		MailboxPassword: []byte("mailbox"),
	}

	if err := session.Save(path, "bridge", sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	return &sessionStore{path: path, password: "bridge", sess: sess}, path
}

func TestSessionStorePersistWritesNewToken(t *testing.T) {
	store, path := newStore(t)

	if err := store.persist("new-uid", "new-token"); err != nil {
		t.Fatalf("persist: %v", err)
	}

	got, err := session.Load(path, "bridge")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got.UID != "new-uid" || got.RefreshToken != "new-token" {
		t.Errorf("stored UID/token = %q/%q, want new-uid/new-token", got.UID, got.RefreshToken)
	}

	// Rotating the token must not disturb anything else in the session.
	if got.Username != "alice" || string(got.MailboxPassword) != "mailbox" {
		t.Errorf("persist clobbered unrelated fields: %+v", got)
	}
}

// An unchanged token should not cause a pointless rewrite of the file.
func TestSessionStorePersistSkipsUnchanged(t *testing.T) {
	store, path := newStore(t)

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	if err := store.persist("old-uid", "old-token"); err != nil {
		t.Fatalf("persist: %v", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	// Every save re-randomises salt and nonce, so an identical file proves no
	// write happened.
	if !bytes.Equal(before, after) {
		t.Error("persist rewrote the file despite an unchanged token")
	}
}

// go-proton-api invokes the auth handler from request goroutines.
func TestSessionStorePersistIsConcurrencySafe(t *testing.T) {
	store, path := newStore(t)

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			if err := store.persist("uid", string(rune('a'+i))); err != nil {
				t.Errorf("persist: %v", err)
			}
		}()
	}
	wg.Wait()

	if _, err := session.Load(path, "bridge"); err != nil {
		t.Fatalf("session file is unreadable after concurrent writes: %v", err)
	}
}

package session_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/develonrails/carbonate/internal/session"
)

func testSession() *session.Session {
	return &session.Session{
		Username:        "someone@proton.me",
		UserID:          "user-id",
		UID:             "session-uid",
		RefreshToken:    "refresh-token",
		MailboxPassword: []byte("mailbox-password"),
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	want := testSession()

	if err := session.Save(path, "bridge-password", want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := session.Load(path, "bridge-password")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got.Username != want.Username || got.UserID != want.UserID {
		t.Errorf("identity not preserved: got %+v", got)
	}

	if got.UID != want.UID || got.RefreshToken != want.RefreshToken {
		t.Errorf("tokens not preserved: got %+v", got)
	}

	if !bytes.Equal(got.MailboxPassword, want.MailboxPassword) {
		t.Errorf("MailboxPassword = %q, want %q", got.MailboxPassword, want.MailboxPassword)
	}
}

func TestLoadWrongPassword(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")

	if err := session.Save(path, "right", testSession()); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, err := session.Load(path, "wrong"); !errors.Is(err, session.ErrWrongPassword) {
		t.Fatalf("Load with wrong password: got %v, want ErrWrongPassword", err)
	}
}

// The session file holds a long-lived credential and the mailbox password, so
// it must not be readable by other users on the machine.
func TestSaveFilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")

	if err := session.Save(path, "bridge-password", testSession()); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}

	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("permissions = %#o, want 0600", perm)
	}
}

// Secrets must not survive in the clear anywhere in the file.
func TestSaveDoesNotLeakPlaintext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")

	if err := session.Save(path, "bridge-password", testSession()); err != nil {
		t.Fatalf("Save: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	for _, secret := range []string{"refresh-token", "mailbox-password", "someone@proton.me"} {
		if bytes.Contains(data, []byte(secret)) {
			t.Errorf("session file contains plaintext %q", secret)
		}
	}
}

// Saving twice must not leave the temporary file behind.
func TestSaveIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	for range 2 {
		if err := session.Save(path, "bridge-password", testSession()); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	if len(entries) != 1 {
		t.Errorf("directory holds %d entries, want only auth.json", len(entries))
	}
}

// Each save must use a fresh salt and nonce, so identical sessions do not
// produce identical ciphertext.
func TestSaveUsesFreshRandomness(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "a.json")
	second := filepath.Join(dir, "b.json")

	if err := session.Save(first, "bridge-password", testSession()); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := session.Save(second, "bridge-password", testSession()); err != nil {
		t.Fatalf("Save: %v", err)
	}

	a, err := os.ReadFile(first)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	b, err := os.ReadFile(second)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	if bytes.Equal(a, b) {
		t.Error("two saves produced identical files; salt or nonce is being reused")
	}
}

func TestNewBridgePasswordIsUnique(t *testing.T) {
	seen := make(map[string]bool)

	for range 100 {
		p, err := session.NewBridgePassword()
		if err != nil {
			t.Fatalf("NewBridgePassword: %v", err)
		}

		if seen[p] {
			t.Fatalf("duplicate bridge password %q", p)
		}

		seen[p] = true
	}
}

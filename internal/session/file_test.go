package session_test

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/develonrails/carbonate/internal/session"
)

func newFile(t *testing.T) (*session.File, string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "auth.json")

	if err := session.Save(path, "bridge", testSession()); err != nil {
		t.Fatalf("Save: %v", err)
	}

	file, err := session.OpenFile(path, "bridge")
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}

	return file, path
}

// The whole reason this type exists: a rotated token must reach the disk
// without anyone having to remember to put it there.
func TestUpdateWritesThrough(t *testing.T) {
	file, path := newFile(t)

	if err := file.Update("new-uid", "new-token"); err != nil {
		t.Fatalf("Update: %v", err)
	}

	stored, err := session.Load(path, "bridge")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if stored.UID != "new-uid" || stored.RefreshToken != "new-token" {
		t.Errorf("stored %q/%q, want the rotated values", stored.UID, stored.RefreshToken)
	}

	// And the session in hand must agree with the file.
	if file.Session().RefreshToken != "new-token" {
		t.Errorf("the tracked session still holds %q", file.Session().RefreshToken)
	}
}

// Rotating the token must not disturb the rest of the session, particularly
// the mailbox password, without which nothing can be decrypted.
func TestUpdateLeavesTheRestAlone(t *testing.T) {
	file, path := newFile(t)

	if err := file.Update("new-uid", "new-token"); err != nil {
		t.Fatalf("Update: %v", err)
	}

	stored, err := session.Load(path, "bridge")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if stored.Username != "someone@proton.me" || string(stored.MailboxPassword) != "mailbox-password" {
		t.Errorf("Update disturbed the session: %+v", stored)
	}
}

func TestUpdateSkipsAnUnchangedToken(t *testing.T) {
	file, path := newFile(t)

	before, err := readFile(path)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}

	if err := file.Update("session-uid", "refresh-token"); err != nil {
		t.Fatalf("Update: %v", err)
	}

	after, err := readFile(path)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}

	// Every save re-randomises salt and nonce, so an identical file proves
	// nothing was written.
	if string(before) != string(after) {
		t.Error("an unchanged token caused a rewrite")
	}
}

// go-proton-api calls the auth handler from request goroutines.
func TestUpdateIsConcurrencySafe(t *testing.T) {
	file, path := newFile(t)

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			if err := file.Update("uid", string(rune('a'+i))); err != nil {
				t.Errorf("Update: %v", err)
			}
		}()
	}
	wg.Wait()

	if _, err := session.Load(path, "bridge"); err != nil {
		t.Fatalf("the session is unreadable after concurrent updates: %v", err)
	}
}

func TestOpenFileRejectsAWrongPassword(t *testing.T) {
	_, path := newFile(t)

	if _, err := session.OpenFile(path, "wrong"); err == nil {
		t.Error("a wrong bridge password opened the session")
	}
}

func readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

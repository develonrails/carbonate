package session_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/develonrails/carbonate/internal/session"
)

func TestLockExcludesASecondHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")

	first, err := session.Lock(path)
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	defer first.Release()

	// A second lock from this process would succeed — flock is per open file
	// description — so the real exclusion is checked below with a child.
	if _, err := lockInChild(t, path); !errors.Is(err, errChildBlocked) {
		t.Errorf("a second process took the lock: %v", err)
	}
}

func TestLockIsAvailableAfterRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")

	first, err := session.Lock(path)
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}

	if err := first.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	if _, err := lockInChild(t, path); err != nil {
		t.Errorf("the lock was not free after release: %v", err)
	}
}

// A crash must not leave a session locked forever, which is why this uses a
// kernel lock rather than a file holding a process ID.
func TestLockDiesWithTheProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")

	if _, err := lockInChild(t, path); err != nil {
		t.Fatalf("child could not take the lock: %v", err)
	}

	// The child has exited by now; the lock must have gone with it.
	guard, err := session.Lock(path)
	if err != nil {
		t.Fatalf("the lock outlived the process that held it: %v", err)
	}

	guard.Release()
}

func TestReleaseOnNilGuardIsHarmless(t *testing.T) {
	var guard *session.Guard

	if err := guard.Release(); err != nil {
		t.Errorf("releasing nothing returned %v", err)
	}
}

var errChildBlocked = errors.New("child was blocked")

// lockInChild takes the lock in a separate process, because flock does not
// exclude a second attempt from the process that already holds it.
func lockInChild(t *testing.T, path string) (bool, error) {
	t.Helper()

	cmd := exec.Command(os.Args[0], "-test.run=TestLockHelper")
	cmd.Env = append(os.Environ(), "CARBONATE_LOCK_HELPER="+path)

	out, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}

	if len(out) > 0 && string(out[:1]) == "L" {
		return false, errChildBlocked
	}

	return false, errors.New(string(out))
}

// TestLockHelper is not a test: it is the child process used above.
func TestLockHelper(t *testing.T) {
	path := os.Getenv("CARBONATE_LOCK_HELPER")
	if path == "" {
		t.Skip("helper only")
	}

	guard, err := session.Lock(path)
	if err != nil {
		if errors.Is(err, session.ErrLocked) {
			os.Stdout.WriteString("LOCKED")
		}

		os.Exit(1)
	}

	guard.Release()
	os.Exit(0)
}

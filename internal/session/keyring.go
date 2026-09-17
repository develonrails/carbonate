package session

import (
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"
)

// keyringService is the name carbonate's secrets appear under in the user's
// keyring, matching the application ID so that a person looking through
// Seahorse or GNOME Passwords recognises what they are looking at.
const keyringService = "io.github.develonrails.Carbonate"

// ErrNoKeyring means the system has no secret service, or it is locked.
//
// Not every system runs one — a headless box, a minimal desktop — so this is
// something to fall back from rather than fail on.
var ErrNoKeyring = errors.New("no usable keyring")

// Remember stores the bridge password in the system keyring.
//
// Without this, starting carbonate at login is pointless: it would come up
// and wait for a password nobody is there to type, while the calendar apps
// that were the reason for starting it find nothing listening. No mail client
// asks for a password on every boot, and neither should this.
//
// The session file stays encrypted with the password. What changes is where
// the password lives: the keyring, encrypted under the user's login and
// unlocked with their session, rather than beside the file it opens.
func Remember(sessionPath, bridgePassword string) error {
	if err := keyring.Set(keyringService, sessionPath, bridgePassword); err != nil {
		return fmt.Errorf("%w: %v", ErrNoKeyring, err)
	}

	return nil
}

// Recall returns the stored bridge password, reporting whether there was one.
//
// The session file's path is the key rather than the account, because the
// account is inside the file and the file cannot be opened without the very
// thing being looked up.
func Recall(sessionPath string) (string, bool, error) {
	password, err := keyring.Get(keyringService, sessionPath)

	switch {
	case errors.Is(err, keyring.ErrNotFound):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("%w: %v", ErrNoKeyring, err)
	}

	return password, true, nil
}

// Forget removes the stored password, for signing in as someone else or
// signing out. A password that was never there is not an error.
func Forget(sessionPath string) error {
	err := keyring.Delete(keyringService, sessionPath)

	if err == nil || errors.Is(err, keyring.ErrNotFound) {
		return nil
	}

	return fmt.Errorf("%w: %v", ErrNoKeyring, err)
}

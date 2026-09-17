//go:build gtk

package main

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/develonrails/carbonate/internal/session"
)

// prefs are the window's own choices, which belong to this machine rather
// than to the Proton account.
//
// A file rather than GSettings: a schema has to be compiled and installed
// before it can be read, which works inside the Flatpak and fails for anyone
// running `make gui` from a checkout. The bridge has exactly one preference,
// and it is not worth that.
type prefs struct {
	// RunInBackground keeps the bridge serving when the window is closed.
	// Off by default: an app that keeps running after you closed it, without
	// having said so, is one people find in their process list and distrust.
	RunInBackground bool `json:"runInBackground"`

	// Address is the last one served on, so that starting at login serves
	// where the already-configured clients are looking.
	Address string `json:"address,omitempty"`
}

// defaultAddress is where clients are told to look, and so where a bridge
// that starts on its own has to listen.
const defaultAddress = "127.0.0.1:8080"

// listenOn returns the address to serve on, falling back to the one the
// documentation gives out.
func (p prefs) listenOn() string {
	if p.Address == "" {
		return defaultAddress
	}

	return p.Address
}

// prefsPath puts the file beside the session, which is already the directory
// carbonate owns and creates.
func prefsPath() (string, error) {
	sessionPath, err := session.DefaultPath()
	if err != nil {
		return "", err
	}

	return filepath.Join(filepath.Dir(sessionPath), "window.json"), nil
}

// loadPrefs reads the choices, falling back to the defaults for anything it
// cannot read. A missing or damaged preferences file is not a reason to
// refuse to start.
func loadPrefs() prefs {
	var p prefs

	path, err := prefsPath()
	if err != nil {
		return p
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return p
	}

	_ = json.Unmarshal(raw, &p)

	return p
}

// save records the choices, reporting a failure so the window can say the
// setting will not survive a restart rather than pretending it took.
func (p prefs) save() error {
	path, err := prefsPath()
	if err != nil {
		return err
	}

	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}

	return os.WriteFile(path, raw, 0o600)
}

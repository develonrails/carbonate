// Package session stores and refreshes Proton credentials.
//
// The session is encrypted at rest with a locally generated bridge password
// and is a long-lived credential. Transparent token refresh lives here; it is
// the piece both hydroxide and protoxide are missing, and the main reason
// carbonate exists.
package session

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/nacl/secretbox"
	"golang.org/x/crypto/scrypt"
)

// ErrWrongPassword is returned when the bridge password does not decrypt the
// session file. The underlying failure is an authentication-tag mismatch, so
// it is indistinguishable from a corrupted file.
var ErrWrongPassword = errors.New("wrong bridge password, or the session file is corrupt")

// Session is everything carbonate needs to reach Proton unattended.
type Session struct {
	Username string
	UserID   string

	// UID identifies the Proton session; RefreshToken is rotated by Proton on
	// every refresh, so a stale copy is worthless. Always persist the new one.
	UID          string
	RefreshToken string

	// MailboxPassword unlocks the user's OpenPGP keys. Proton never sees it,
	// and in two-password mode it differs from the login password.
	MailboxPassword []byte

	// SaltedKeyPassword is MailboxPassword after salting, which is what
	// actually opens the keys.
	//
	// It is kept so that resuming a session need not ask Proton for the key
	// salts. That endpoint requires a scope a refreshed session eventually
	// loses, and losing it stopped the bridge dead while every other request
	// still worked.
	SaltedKeyPassword []byte
}

// scrypt parameters. N is the cost; raising it slows a brute-force attack on
// the session file at the cost of a one-off delay on each load.
const (
	scryptN      = 1 << 15
	scryptR      = 8
	scryptP      = 1
	keyLen       = 32
	saltLen      = 16
	fileVersion  = 1
	filePermMode = 0o600
)

// envelope is the on-disk format: a scrypt salt and a secretbox nonce in the
// clear, wrapping the encrypted Session.
type envelope struct {
	Version int    `json:"version"`
	Salt    []byte `json:"salt"`
	Nonce   []byte `json:"nonce"`
	Box     []byte `json:"box"`
}

// DefaultPath returns the standard session location, creating its parent
// directory if needed.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locating config dir: %w", err)
	}

	dir = filepath.Join(dir, "carbonate")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating %s: %w", dir, err)
	}

	return filepath.Join(dir, "auth.json"), nil
}

// NewBridgePassword returns a fresh random password for encrypting the session.
func NewBridgePassword() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating bridge password: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func deriveKey(bridgePassword string, salt []byte) (*[keyLen]byte, error) {
	buf, err := scrypt.Key([]byte(bridgePassword), salt, scryptN, scryptR, scryptP, keyLen)
	if err != nil {
		return nil, fmt.Errorf("deriving key: %w", err)
	}

	var key [keyLen]byte
	copy(key[:], buf)

	return &key, nil
}

// Save encrypts s under bridgePassword and writes it to path, replacing any
// existing file. The write is atomic so an interrupted save cannot leave a
// half-written session behind.
func Save(path, bridgePassword string, s *Session) error {
	plaintext, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("encoding session: %w", err)
	}

	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("generating salt: %w", err)
	}

	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fmt.Errorf("generating nonce: %w", err)
	}

	key, err := deriveKey(bridgePassword, salt)
	if err != nil {
		return err
	}

	data, err := json.Marshal(envelope{
		Version: fileVersion,
		Salt:    salt,
		Nonce:   nonce[:],
		Box:     secretbox.Seal(nil, plaintext, &nonce, key),
	})
	if err != nil {
		return fmt.Errorf("encoding envelope: %w", err)
	}

	return writeFileAtomic(path, data)
}

// Load decrypts the session at path.
func Load(path, bridgePassword string) (*Session, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading session: %w", err)
	}

	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("decoding envelope: %w", err)
	}

	if env.Version != fileVersion {
		return nil, fmt.Errorf("unsupported session file version %d", env.Version)
	}

	if len(env.Nonce) != 24 {
		return nil, errors.New("malformed session file: bad nonce length")
	}

	key, err := deriveKey(bridgePassword, env.Salt)
	if err != nil {
		return nil, err
	}

	var nonce [24]byte
	copy(nonce[:], env.Nonce)

	plaintext, ok := secretbox.Open(nil, env.Box, &nonce, key)
	if !ok {
		return nil, ErrWrongPassword
	}

	var s Session
	if err := json.Unmarshal(plaintext, &s); err != nil {
		return nil, fmt.Errorf("decoding session: %w", err)
	}

	return &s, nil
}

// writeFileAtomic writes data to a temporary file in the same directory and
// renames it into place, so readers never observe a partial file.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)

	f, err := os.CreateTemp(dir, ".auth-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmp := f.Name()

	defer func() {
		// No-op once the rename has succeeded.
		_ = os.Remove(tmp)
	}()

	if err := f.Chmod(filePermMode); err != nil {
		f.Close()
		return fmt.Errorf("setting permissions: %w", err)
	}

	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("writing session: %w", err)
	}

	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("syncing session: %w", err)
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("closing session: %w", err)
	}

	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replacing session: %w", err)
	}

	return nil
}

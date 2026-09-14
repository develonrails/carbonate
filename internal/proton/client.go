// Package proton wraps Proton's private API: SRP authentication, key
// unlocking, and the event-loop endpoint used for delta sync.
//
// It builds on go-proton-api, the library underneath the official Proton Mail
// Bridge, rather than reimplementing SRP and OpenPGP handling.
package proton

import (
	"context"
	"errors"
	"fmt"
	"os"

	api "github.com/ProtonMail/go-proton-api"
	"github.com/ProtonMail/gopenpgp/v2/crypto"

	"github.com/develonrails/carbonate/internal/session"
)

// ErrWrongMailboxPassword means the password did not unlock any of the user's
// keys. Proton cannot tell us this directly: the unlock happens locally, and
// go-proton-api skips keys it fails to open rather than reporting an error.
var ErrWrongMailboxPassword = errors.New("mailbox password did not unlock any keys")

// ErrFIDO2Unsupported is returned for accounts whose only second factor is a
// security key.
var ErrFIDO2Unsupported = errors.New("account requires a FIDO2 security key, which carbonate does not support yet")

// appVersion identifies the client to Proton. The API validates this and
// rejects versions it does not recognise, so this may need updating when
// Proton retires an old client. Override with CARBONATE_APP_VERSION.
const defaultAppVersion = "linux-mail@1.0.0"

func appVersion() string {
	if v := os.Getenv("CARBONATE_APP_VERSION"); v != "" {
		return v
	}

	return defaultAppVersion
}

// apiURL returns the Proton API host. Empty means the production API; tests
// point this at a fake server.
func apiURL() string {
	return os.Getenv("CARBONATE_API_URL")
}

// Prompter supplies the interactive parts of login. The caller owns terminal
// handling so this package stays testable and headless-friendly.
type Prompter interface {
	// Password reads a secret without echoing it.
	Password(prompt string) ([]byte, error)
	// Line reads a visible line, such as a TOTP code.
	Line(prompt string) (string, error)
}

// Conn is a live, authenticated connection to Proton.
type Conn struct {
	Manager *api.Manager
	Client  *api.Client

	// UserKR is the unlocked user keyring. Address and calendar keys are
	// unlocked through it, not directly from the mailbox password.
	UserKR *crypto.KeyRing
}

// Close releases the client and its underlying manager.
func (c *Conn) Close() {
	if c.Client != nil {
		c.Client.Close()
	}

	if c.Manager != nil {
		c.Manager.Close()
	}
}

func newManager() *api.Manager {
	opts := []api.Option{api.WithAppVersion(appVersion())}

	if u := apiURL(); u != "" {
		opts = append(opts, api.WithHostURL(u))
	}

	return api.New(opts...)
}

// Login performs a full interactive login and returns a session ready to be
// persisted. It verifies the mailbox password before returning, so a stored
// session is always one that actually works.
func Login(ctx context.Context, username string, loginPassword []byte, p Prompter) (*session.Session, error) {
	m := newManager()
	defer m.Close()

	c, auth, err := m.NewClientWithLogin(ctx, username, loginPassword)
	if err != nil {
		return nil, fmt.Errorf("login failed: %w", err)
	}
	defer c.Close()

	switch {
	case auth.TwoFA.Enabled&api.HasTOTP != 0:
		code, err := p.Line("Two-factor code: ")
		if err != nil {
			return nil, err
		}

		if err := c.Auth2FA(ctx, api.Auth2FAReq{TwoFactorCode: code}); err != nil {
			return nil, fmt.Errorf("two-factor authentication failed: %w", err)
		}

	case auth.TwoFA.Enabled&api.HasFIDO2 != 0:
		return nil, ErrFIDO2Unsupported
	}

	// In two-password mode the login password only proves who you are; a
	// second, separate password unlocks the keys.
	mailboxPassword := loginPassword
	if auth.PasswordMode == api.TwoPasswordMode {
		mailboxPassword, err = p.Password("Mailbox password: ")
		if err != nil {
			return nil, err
		}
	}

	user, err := c.GetUser(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetching user: %w", err)
	}

	if _, err := unlock(ctx, c, user, mailboxPassword); err != nil {
		return nil, err
	}

	return &session.Session{
		Username:        username,
		UserID:          user.ID,
		UID:             auth.UID,
		RefreshToken:    auth.RefreshToken,
		MailboxPassword: mailboxPassword,
	}, nil
}

// PersistFunc is called whenever Proton hands out a new refresh token. The
// old token is dead at that point, so failing to persist the new one locks
// the user out until they log in again.
type PersistFunc func(uid, refreshToken string) error

// Resume restores a stored session and unlocks the user's keys. persist is
// called immediately with the rotated token and again on every later refresh.
func Resume(ctx context.Context, s *session.Session, persist PersistFunc) (*Conn, error) {
	m := newManager()

	c, auth, err := m.NewClientWithRefresh(ctx, s.UID, s.RefreshToken)
	if err != nil {
		m.Close()
		return nil, fmt.Errorf("resuming session (re-run `carbonate auth`): %w", err)
	}

	// Refreshing already rotated the token, so persist before anything else
	// can fail and strand us with a token Proton has discarded.
	if err := persist(auth.UID, auth.RefreshToken); err != nil {
		c.Close()
		m.Close()
		return nil, fmt.Errorf("persisting refreshed token: %w", err)
	}

	c.AddAuthHandler(func(a api.Auth) {
		if err := persist(a.UID, a.RefreshToken); err != nil {
			fmt.Fprintf(os.Stderr, "carbonate: failed to persist refreshed token: %v\n", err)
		}
	})

	user, err := c.GetUser(ctx)
	if err != nil {
		c.Close()
		m.Close()
		return nil, fmt.Errorf("fetching user: %w", err)
	}

	userKR, err := unlock(ctx, c, user, s.MailboxPassword)
	if err != nil {
		c.Close()
		m.Close()
		return nil, err
	}

	return &Conn{Manager: m, Client: c, UserKR: userKR}, nil
}

// unlock derives the salted key passphrase and opens the user's keyring.
//
// Keys.Unlock skips keys it cannot open and still returns a nil error, so an
// empty keyring — not an error — is how a wrong password shows up.
func unlock(ctx context.Context, c *api.Client, user api.User, mailboxPassword []byte) (*crypto.KeyRing, error) {
	salts, err := c.GetSalts(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetching key salts: %w", err)
	}

	saltedPassword, err := salts.SaltForKey(mailboxPassword, user.Keys.Primary().ID)
	if err != nil {
		return nil, fmt.Errorf("salting mailbox password: %w", err)
	}

	userKR, err := user.Keys.Unlock(saltedPassword, nil)
	if err != nil {
		return nil, fmt.Errorf("unlocking user keys: %w", err)
	}

	if userKR == nil || userKR.CountDecryptionEntities() == 0 {
		return nil, ErrWrongMailboxPassword
	}

	return userKR, nil
}

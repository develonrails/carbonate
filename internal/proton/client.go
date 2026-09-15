// Package proton wraps Proton's private API: SRP authentication, key
// unlocking, and the event-loop endpoint used for delta sync.
//
// It builds on go-proton-api, the library underneath the official Proton Mail
// Bridge, rather than reimplementing SRP and OpenPGP handling.
package proton

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	api "github.com/ProtonMail/go-proton-api"
	"github.com/ProtonMail/gopenpgp/v2/crypto"

	"github.com/develonrails/carbonate/internal/session"
)

// ErrWrongMailboxPassword means the password did not unlock any of the user's
// keys. Proton cannot tell us this directly, because the unlock happens
// locally.
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

	// saltedKeyPass is the mailbox password after salting. Modern address keys
	// carry a token that the user keyring opens, but legacy ones still need
	// this passphrase directly.
	saltedKeyPass []byte

	// Credentials for raw requests, kept current as Proton rotates them.
	mu          sync.RWMutex
	uid         string
	accessToken string

	// refresh provokes go-proton-api into renewing the access token, which
	// fires the auth handler that updates the credentials above. Held as a
	// function so the retry can be exercised without a live client.
	refresh func(ctx context.Context) error

	onClose []func()
}

// setCredentials records the tokens used for raw requests.
func (c *Conn) setCredentials(uid, accessToken string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.uid, c.accessToken = uid, accessToken
}

// Get performs an authenticated GET against a Proton API path and decodes the
// JSON response into out.
//
// go-proton-api's structs have drifted from the live API — a calendar's name,
// for example, moved into the member object and is no longer decoded at all —
// so carbonate occasionally needs the response as Proton actually sends it.
func (c *Conn) Get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

// Put sends body as JSON and decodes the response into out.
//
// Proton's calendar write path is a single PUT .../events/sync that carries
// create, update and delete as differently shaped entries.
func (c *Conn) Put(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPut, path, body, out)
}

func (c *Conn) do(ctx context.Context, method, path string, body, out any) error {
	status, err := c.attempt(ctx, method, path, body, out)
	if err != nil {
		return err
	}

	if status != http.StatusUnauthorized {
		return nil
	}

	// Raw requests carry a snapshot of the access token, and Proton expires
	// them. go-proton-api refreshes on its own 401s, so provoke one cheaply:
	// it fires the auth handler, which updates our copy. Then try once more.
	if c.refresh == nil {
		return fmt.Errorf("requesting %s: unauthorised, and there is no way to refresh the token", path)
	}

	if err := c.refresh(ctx); err != nil {
		return fmt.Errorf("refreshing expired token: %w", err)
	}

	status, err = c.attempt(ctx, method, path, body, out)
	if err != nil {
		return err
	}

	if status == http.StatusUnauthorized {
		return fmt.Errorf("requesting %s: still unauthorised after refreshing the token", path)
	}

	return nil
}

// attempt makes one request. It returns the status code when the request was
// rejected in a way worth retrying, and otherwise reports the failure.
func (c *Conn) attempt(ctx context.Context, method, path string, body, out any) (int, error) {
	c.mu.RLock()
	uid, token := c.uid, c.accessToken
	c.mu.RUnlock()

	var payload io.Reader

	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, fmt.Errorf("encoding request for %s: %w", path, err)
		}

		payload = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, hostURL()+path, payload)
	if err != nil {
		return 0, fmt.Errorf("building request for %s: %w", path, err)
	}

	req.Header.Set("x-pm-appversion", appVersion())
	req.Header.Set("x-pm-uid", uid)
	req.Header.Set("Authorization", "Bearer "+token)

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("requesting %s: %w", path, err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return 0, fmt.Errorf("reading %s: %w", path, err)
	}

	if os.Getenv("CARBONATE_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "carbonate: %s %s -> %s\n%s\n", method, path, res.Status, raw)
	}

	if res.StatusCode == http.StatusUnauthorized {
		return res.StatusCode, nil
	}

	if res.StatusCode != http.StatusOK {
		// Proton puts the useful reason in the body, not the status line.
		return res.StatusCode, fmt.Errorf("requesting %s: %s: %s", path, res.Status, strings.TrimSpace(string(raw)))
	}

	if out == nil {
		return res.StatusCode, nil
	}

	if err := json.Unmarshal(raw, out); err != nil {
		return res.StatusCode, fmt.Errorf("decoding %s: %w", path, err)
	}

	return res.StatusCode, nil
}

// AddressKeyRing unlocks the keys of a single address.
func (c *Conn) AddressKeyRing(addr api.Address) (*crypto.KeyRing, error) {
	kr, err := addr.Keys.Unlock(c.saltedKeyPass, c.UserKR)
	if err != nil {
		return nil, fmt.Errorf("unlocking keys for %s: %w", addr.Email, err)
	}

	if kr == nil || kr.CountDecryptionEntities() == 0 {
		return nil, fmt.Errorf("no usable keys for address %s", addr.Email)
	}

	return kr, nil
}

// ErrSessionLostScope means Proton has demoted the session. It happens when a
// client holds more sessions than Proton allows: rather than refusing the
// newest, it strips scope from older ones, so the failure surfaces later on an
// unrelated request and looks like a permissions problem.
var ErrSessionLostScope = errors.New("this Proton session no longer has full access; log in again")

// Codes Proton returns that mean something carbonate can explain better than
// the bare message does.
const (
	// scopeCode: the session may no longer make this request.
	scopeCode = 9101

	// versionCode: the client version reported at login is no longer accepted.
	versionCode = 5003
)

// ErrAppVersionRejected means Proton no longer accepts the version carbonate
// reports. The value is a guess that happens to be recognised, so this will
// come true eventually.
var ErrAppVersionRejected = errors.New("Proton no longer accepts the version carbonate reports; set CARBONATE_APP_VERSION to a version it recognises")

// explainVersion turns Proton's rejection of the client version into
// something that names the knob to turn.
func explainVersion(err error) error {
	if err == nil || !strings.Contains(err.Error(), strconv.Itoa(versionCode)) {
		return err
	}

	return fmt.Errorf("%w (%v)", ErrAppVersionRejected, err)
}

// explainScope turns Proton's bare complaint about scope into something
// actionable.
func explainScope(err error) error {
	if err == nil || !strings.Contains(err.Error(), strconv.Itoa(scopeCode)) {
		return err
	}

	return fmt.Errorf("%w (%v)", ErrSessionLostScope, err)
}

// UID identifies the session this connection is using.
func (c *Conn) UID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.uid
}

// Sessions lists the Proton sessions open for this account.
func (c *Conn) Sessions(ctx context.Context) ([]api.AuthSession, error) {
	return c.Client.AuthSessions(ctx)
}

// RevokeOtherSessions ends every session except this one.
//
// This logs out other clients too — the web app, a phone — so it is never
// done on carbonate's own initiative.
func (c *Conn) RevokeOtherSessions(ctx context.Context) error {
	return c.Client.AuthRevokeAll(ctx)
}

// RevokeSession ends a Proton session by its UID.
//
// Used to retire the session being replaced at login: Proton keeps a limited
// number, and leaving them behind eventually costs the newest one its scope.
func (c *Conn) RevokeSession(ctx context.Context, uid string) error {
	return c.Client.AuthRevoke(ctx, uid)
}

// PrimaryAddressKeyRing unlocks the keys of the account's primary address.
//
// Contacts are encrypted to it rather than to a per-collection key, so this is
// the keyring that reads and writes them.
func (c *Conn) PrimaryAddressKeyRing(ctx context.Context) (*crypto.KeyRing, error) {
	addresses, err := c.Client.GetAddresses(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetching addresses: %w", err)
	}

	// Addresses come back ordered; the first one that can receive is the one
	// Proton treats as primary.
	for _, addr := range addresses {
		if addr.Status != api.AddressStatusEnabled {
			continue
		}

		return c.AddressKeyRing(addr)
	}

	return nil, fmt.Errorf("account has no enabled address")
}

// CalendarKeys is everything needed to read or write a calendar's events.
type CalendarKeys struct {
	// MemberID identifies our membership. Writes are attributed to it.
	MemberID string

	// Email is the address that membership belongs to. Proton requires an
	// event with attendees to name an organiser, and this is ours.
	Email string

	// CalKR decrypts and encrypts event content.
	CalKR *crypto.KeyRing

	// AddrKR verifies signatures on read, and makes them on write. Proton
	// rejects an event signed with anything but the member's own address key.
	AddrKR *crypto.KeyRing
}

// CalendarKeys unlocks a calendar's keys.
//
// A calendar key is wrapped in a passphrase encrypted to a member's address
// key, so the address keyring is needed both to get in and to handle the
// signatures on event parts.
func (c *Conn) CalendarKeys(ctx context.Context, calendarID string) (*CalendarKeys, error) {
	members, err := c.Client.GetCalendarMembers(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("fetching calendar members: %w", err)
	}

	addresses, err := c.Client.GetAddresses(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetching addresses: %w", err)
	}

	// Membership is recorded by email, so pair it back up with our address.
	var member api.CalendarMember
	var addr api.Address

	for _, m := range members {
		for _, a := range addresses {
			if strings.EqualFold(m.Email, a.Email) {
				member, addr = m, a
			}
		}
	}

	if member.ID == "" {
		return nil, fmt.Errorf("no membership of calendar %s belongs to this account", calendarID)
	}

	addrKR, err := c.AddressKeyRing(addr)
	if err != nil {
		return nil, err
	}

	passphrase, err := c.Client.GetCalendarPassphrase(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("fetching calendar passphrase: %w", err)
	}

	raw, err := passphrase.Decrypt(member.ID, addrKR)
	if err != nil {
		return nil, fmt.Errorf("decrypting calendar passphrase: %w", err)
	}

	keys, err := c.Client.GetCalendarKeys(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("fetching calendar keys: %w", err)
	}

	calKR, err := keys.Unlock(raw)
	if err != nil {
		return nil, fmt.Errorf("unlocking calendar keys: %w", err)
	}

	return &CalendarKeys{MemberID: member.ID, Email: addr.Email, CalKR: calKR, AddrKR: addrKR}, nil
}

// OnClose registers work to run when the connection is closed, so that a
// caller holding a resource for the connection's lifetime can let it go
// without tracking the connection itself.
func (c *Conn) OnClose(fn func()) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.onClose = append(c.onClose, fn)
}

// Close releases the client, its underlying manager, and anything registered
// with OnClose.
func (c *Conn) Close() {
	if c.Client != nil {
		c.Client.Close()
	}

	if c.Manager != nil {
		c.Manager.Close()
	}

	c.mu.Lock()
	handlers := c.onClose
	c.onClose = nil
	c.mu.Unlock()

	for _, fn := range handlers {
		fn()
	}
}

func newManager() *api.Manager {
	opts := []api.Option{api.WithAppVersion(appVersion())}

	if u := apiURL(); u != "" {
		opts = append(opts, api.WithHostURL(u))
	}

	// CARBONATE_DEBUG dumps full requests and responses, including access
	// tokens and encrypted payloads. For diagnosing API drift, not for
	// everyday use.
	if os.Getenv("CARBONATE_DEBUG") != "" {
		opts = append(opts, api.WithDebug(true))
	}

	return api.New(opts...)
}

// Login performs a full interactive login and returns a session ready to be
// persisted. It verifies the mailbox password before returning, so a stored
// session is always one that actually works.
func Login(ctx context.Context, username string, loginPassword []byte, p Prompter) (*session.Session, error) {
	m, err := newAnonymousManager(ctx)
	if err != nil {
		return nil, err
	}
	defer m.Close()

	c, auth, err := m.NewClientWithLogin(ctx, username, loginPassword)
	if err != nil {
		return nil, fmt.Errorf("login failed: %w", explainVersion(err))
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

	mailboxPassword, err := mailboxPasswordFor(auth, loginPassword, p)
	if err != nil {
		return nil, err
	}

	user, err := c.GetUser(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetching user: %w", err)
	}

	_, salted, err := unlock(ctx, c, user, mailboxPassword)
	if err != nil {
		return nil, err
	}

	return &session.Session{
		Username:          username,
		UserID:            user.ID,
		UID:               auth.UID,
		RefreshToken:      auth.RefreshToken,
		MailboxPassword:   mailboxPassword,
		SaltedKeyPassword: salted,
	}, nil
}

// mailboxPasswordFor returns the password that unlocks the user's keys.
//
// Most accounts use one password for both jobs. In two-password mode they are
// separate: the login password only proves who you are to Proton, while the
// mailbox password never leaves the machine and is the only thing that can
// decrypt anything. Reusing the login password there would fail later, at the
// unlock, with an error that says nothing about a second password existing.
func mailboxPasswordFor(auth api.Auth, loginPassword []byte, p Prompter) ([]byte, error) {
	if auth.PasswordMode != api.TwoPasswordMode {
		return loginPassword, nil
	}

	return p.Password("Mailbox password: ")
}

// TokenStore is a session together with somewhere to keep it.
//
// Resume takes this rather than a callback deliberately. Proton rotates the
// refresh token on every use and discards the old one, so a callback that
// does nothing leaves the stored session holding a dead token — and it
// compiles, runs, and fails much later as an account that seems broken.
// session.File satisfies this and cannot forget.
type TokenStore interface {
	// Session returns the session to resume.
	Session() *session.Session

	// Update records rotated credentials and stores them.
	Update(uid, refreshToken string) error
}

// Resume restores a stored session and unlocks the user's keys.
//
// The store is updated with the rotated token immediately, and again on every
// later refresh.
func Resume(ctx context.Context, store TokenStore) (*Conn, error) {
	s := store.Session()

	m := newManager()

	c, auth, err := m.NewClientWithRefresh(ctx, s.UID, s.RefreshToken)
	if err != nil {
		m.Close()
		return nil, fmt.Errorf("resuming session (re-run `carbonate auth`): %w", err)
	}

	// Refreshing already rotated the token, so store it before anything else
	// can fail and strand us with a token Proton has discarded.
	if err := store.Update(auth.UID, auth.RefreshToken); err != nil {
		c.Close()
		m.Close()
		return nil, fmt.Errorf("persisting refreshed token: %w", err)
	}

	conn := &Conn{Manager: m, Client: c}
	conn.setCredentials(auth.UID, auth.AccessToken)

	// Any authenticated call will do: go-proton-api renews the token when it
	// meets a 401 of its own, and the auth handler below records the result.
	conn.refresh = func(ctx context.Context) error {
		_, err := c.GetUser(ctx)

		return err
	}

	c.AddAuthHandler(func(a api.Auth) {
		conn.setCredentials(a.UID, a.AccessToken)

		if err := store.Update(a.UID, a.RefreshToken); err != nil {
			fmt.Fprintf(os.Stderr, "carbonate: failed to store the refreshed token: %v\n", err)
		}
	})

	user, err := c.GetUser(ctx)
	if err != nil {
		c.Close()
		m.Close()
		return nil, fmt.Errorf("fetching user: %w", err)
	}

	userKR, salted, err := unlockStored(ctx, c, user, s, store)
	if err != nil {
		c.Close()
		m.Close()

		return nil, err
	}

	conn.UserKR = userKR
	conn.saltedKeyPass = salted

	return conn, nil
}

// unlockStored opens the user's keyring using the salted passphrase kept with
// the session, falling back to asking Proton for the salts.
//
// Fetching the salts needs a scope that a refreshed session eventually loses:
// ordinary requests keep working while /core/v4/keys/salts answers 403, which
// stopped the bridge with an error about permissions that had nothing to do
// with what it was trying to read. Keeping the salted passphrase avoids the
// endpoint altogether.
//
// The fallback matters for sessions stored before this existed, and for the
// day a key is added or rotated and the old passphrase stops working.
func unlockStored(ctx context.Context, c *api.Client, user api.User, s *session.Session, store TokenStore) (*crypto.KeyRing, []byte, error) {
	if len(s.SaltedKeyPassword) > 0 {
		if kr, err := user.Keys.Unlock(s.SaltedKeyPassword, nil); err == nil && kr != nil && kr.CountDecryptionEntities() > 0 {
			return kr, s.SaltedKeyPassword, nil
		}
	}

	kr, salted, err := unlock(ctx, c, user, s.MailboxPassword)
	if err != nil {
		return nil, nil, err
	}

	// Keep what was just derived, so the salts are not needed again.
	s.SaltedKeyPassword = salted

	if saver, ok := store.(interface{ Save() error }); ok {
		if err := saver.Save(); err != nil {
			fmt.Fprintf(os.Stderr, "carbonate: could not store the salted key password: %v\n", err)
		}
	}

	return kr, salted, nil
}

// unlock derives the salted key passphrase and opens the user's keyring.
//
// A wrong mailbox password surfaces here rather than at the API: keys are
// unlocked locally. Current go-proton-api reports it as "not able to unlock
// any key"; older versions returned an empty keyring with a nil error, so
// both are treated as the same thing.
func unlock(ctx context.Context, c *api.Client, user api.User, mailboxPassword []byte) (*crypto.KeyRing, []byte, error) {
	salts, err := c.GetSalts(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("fetching key salts: %w", explainScope(err))
	}

	saltedPassword, err := salts.SaltForKey(mailboxPassword, user.Keys.Primary().ID)
	if err != nil {
		return nil, nil, fmt.Errorf("salting mailbox password: %w", err)
	}

	userKR, err := user.Keys.Unlock(saltedPassword, nil)
	if err != nil {
		// Unlock's only realistic failure is that no key opened.
		return nil, nil, fmt.Errorf("%w: %v", ErrWrongMailboxPassword, err)
	}

	if userKR == nil || userKR.CountDecryptionEntities() == 0 {
		return nil, nil, ErrWrongMailboxPassword
	}

	return userKR, saltedPassword, nil
}

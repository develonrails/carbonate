//go:build gtk

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/develonrails/carbonate/internal/proton"
	"github.com/develonrails/carbonate/internal/server"
	"github.com/develonrails/carbonate/internal/session"
)

// bridge is the non-visual half of the app: the Proton session and the
// running server. It keeps no GTK types, so the window can be rebuilt or
// replaced without disturbing a live connection.
type bridge struct {
	mu       sync.Mutex
	path     string
	password string
	sess     *session.Session
	conn     *proton.Conn

	cancel context.CancelFunc
	done   chan struct{}
	email  string
}

func newBridge() *bridge {
	path, err := session.DefaultPath()
	if err != nil {
		// Without a config directory there is nowhere to keep a session; the
		// login attempt will say so in a place the user can see.
		path = ""
	}

	return &bridge{path: path}
}

// sessionPath is where the encrypted session lives. It is the key the bridge
// password is remembered under, because the account it belongs to is inside
// the file and cannot be read without that very password.
func (b *bridge) sessionPath() string {
	return b.path
}

func (b *bridge) hasSession() bool {
	if b.path == "" {
		return false
	}

	_, err := os.Stat(b.path)

	return err == nil
}

// guiPrompter answers the interactive parts of login from values the window
// already collected, rather than from a terminal.
//
// Login only asks for a mailbox password on a two-password account, where the
// key that unlocks your data is separate from the one that proves who you
// are. Most accounts use one password for both.
type guiPrompter struct {
	mailbox      []byte
	twoFactor    string
	verification string

	// onVerify is called with Proton's message when a challenge is needed,
	// so the window can show the URL rather than swallow it.
	onVerify func(message string)
}

func (p guiPrompter) Password(string) ([]byte, error) {
	if len(p.mailbox) == 0 {
		return nil, errors.New("this account keeps its mailbox password separate; fill in the mailbox password field")
	}

	return p.mailbox, nil
}

func (p guiPrompter) Line(string) (string, error) {
	if p.twoFactor == "" {
		return "", errors.New("this account needs a two-factor code")
	}

	return p.twoFactor, nil
}

// Verify hands back a token already in the window, and otherwise shows what
// Proton is asking for so the person can fetch one.
func (p guiPrompter) Verify(message string) (string, error) {
	if p.verification == "" {
		if p.onVerify != nil {
			p.onVerify(message)
		}

		return "", errors.New("open the link above, solve the challenge, and paste the token into the verification field")
	}

	return p.verification, nil
}

// login signs in to Proton and stores the session, returning the bridge
// password the user must give to their apps.
func (b *bridge) login(ctx context.Context, username string, password []byte, twoFactor, mailboxPassword, verification string, onVerify func(string)) (string, error) {
	if b.path == "" {
		return "", errors.New("no configuration directory to store the session in")
	}

	// An empty field means the account is not in two-password mode, so the
	// login password is also the one that unlocks the keys.
	mailbox := []byte(mailboxPassword)
	if len(mailbox) == 0 {
		mailbox = password
	}

	sess, err := proton.Login(ctx, username, password, guiPrompter{
		mailbox:      mailbox,
		twoFactor:    twoFactor,
		verification: verification,
		onVerify:     onVerify,
	})
	if err != nil {
		return "", err
	}

	bridgePassword, err := session.NewBridgePassword()
	if err != nil {
		return "", err
	}

	if err := session.Save(b.path, bridgePassword, sess); err != nil {
		return "", err
	}

	b.mu.Lock()
	b.sess, b.password = sess, bridgePassword
	b.mu.Unlock()

	return bridgePassword, b.connect(ctx)
}

// unlock opens the stored session with an existing bridge password.
func (b *bridge) unlock(ctx context.Context, bridgePassword string) error {
	sess, err := session.Load(b.path, bridgePassword)
	if err != nil {
		return err
	}

	b.mu.Lock()
	b.sess, b.password = sess, bridgePassword
	b.mu.Unlock()

	return b.connect(ctx)
}

// connect resumes the Proton session, storing rotated tokens as it goes.
func (b *bridge) connect(ctx context.Context) error {
	b.mu.Lock()
	sess, password := b.sess, b.password
	b.mu.Unlock()

	conn, err := proton.Resume(ctx, session.NewFile(b.path, password, sess))
	if err != nil {
		return err
	}

	user, err := conn.Client.GetUser(ctx)
	if err != nil {
		conn.Close()

		return fmt.Errorf("fetching account: %w", err)
	}

	b.mu.Lock()
	b.conn, b.email = conn, user.Email
	b.mu.Unlock()

	return nil
}

func (b *bridge) account() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.email
}

func (b *bridge) running() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.cancel != nil
}

// start serves CalDAV and CardDAV until stop is called.
//
// activity receives the server's log, which the window shows: someone running
// the window has no terminal to read it in. watch is how often to ask Proton
// whether anything changed, so that the log says something even when no client
// is connected.
func (b *bridge) start(address string, activity io.Writer, watch time.Duration) error {
	b.mu.Lock()

	if b.conn == nil {
		b.mu.Unlock()

		return errors.New("not connected to Proton yet")
	}

	if b.cancel != nil {
		b.mu.Unlock()

		return errors.New("already serving")
	}

	conn, email, password := b.conn, b.email, b.password

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	b.cancel, b.done = cancel, done
	b.mu.Unlock()

	// Give Serve a moment to bind, so a busy port is reported here rather
	// than appearing to start and then vanishing.
	failed := make(chan error, 1)

	go func() {
		defer close(done)

		failed <- server.Serve(ctx, conn, server.Options{
			Addr:     address,
			Username: email,
			Password: password,
			Out:      io.Discard,
			Activity: activity,
			Watch:    watch,
		})
	}()

	select {
	case err := <-failed:
		b.mu.Lock()
		b.cancel, b.done = nil, nil
		b.mu.Unlock()

		cancel()

		if err == nil {
			return errors.New("the server stopped immediately")
		}

		return err

	case <-afterBind():
		return nil
	}
}

func (b *bridge) stop() {
	b.mu.Lock()
	cancel, done := b.cancel, b.done
	b.cancel, b.done = nil, nil
	b.mu.Unlock()

	if cancel == nil {
		return
	}

	cancel()

	if done != nil {
		<-done
	}
}

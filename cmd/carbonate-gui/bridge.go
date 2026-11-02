//go:build gtk

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
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

// asker reaches the person while a login is under way, for what can only be
// known once Proton has answered: a puzzle to solve, or a code to type. The
// window implements it; the bridge only knows when to ask.
//
// Each method waits for the answer, so none may be called from GTK's thread.
type asker interface {
	// confirmCaptcha offers the page with Proton's puzzle and returns once
	// the person says they have solved it.
	confirmCaptcha(link string) error

	// verificationToken offers the page for any other kind of challenge and
	// returns the token the person brings back from it.
	verificationToken(link string) (string, error)

	// twoFactorCode asks for the code an authenticator app shows. stale says
	// that one was typed earlier and has probably expired since.
	twoFactorCode(stale bool) (string, error)
}

// The window answers a CAPTCHA by waiting, as the terminal does. Without this
// it would be asked for a token there is no way to get.
var (
	_ proton.Prompter         = (*guiPrompter)(nil)
	_ proton.CaptchaConfirmer = (*guiPrompter)(nil)
)

// guiPrompter answers the interactive parts of login, from what the window
// already collected where it can and by asking where it cannot.
//
// Login only asks for a mailbox password on a two-password account, where the
// key that unlocks your data is separate from the one that proves who you
// are. Most accounts use one password for both.
type guiPrompter struct {
	mailbox   []byte
	twoFactor string
	ask       asker

	// solvedPuzzle records that the person went off to a browser. A
	// two-factor code typed before that has had time to expire: they last
	// thirty seconds, and a CAPTCHA takes longer than that.
	solvedPuzzle bool
}

func (p *guiPrompter) Password(string) ([]byte, error) {
	if len(p.mailbox) == 0 {
		return nil, errors.New("this account keeps its mailbox password separate; fill in the mailbox password field")
	}

	return p.mailbox, nil
}

// Line is only ever asked for the two-factor code.
func (p *guiPrompter) Line(string) (string, error) {
	typed := p.twoFactor != ""

	if typed && !p.solvedPuzzle {
		return p.twoFactor, nil
	}

	return p.ask.twoFactorCode(typed)
}

// ConfirmCaptcha lets the login carry on with Proton's own challenge token
// once the puzzle is solved, as the terminal does. There is nothing to paste:
// the page marks that token as answered.
func (p *guiPrompter) ConfirmCaptcha(message string) error {
	if err := p.ask.confirmCaptcha(linkIn(message)); err != nil {
		return err
	}

	p.solvedPuzzle = true

	return nil
}

// Verify is for challenges other than a CAPTCHA, which do hand back a token.
func (p *guiPrompter) Verify(message string) (string, error) {
	return p.ask.verificationToken(linkIn(message))
}

// linkIn finds the page a challenge is answered on. Proton's message is worded
// for a terminal; the window wants the address on its own, behind a button.
func linkIn(message string) string {
	for _, field := range strings.Fields(message) {
		if strings.HasPrefix(field, "https://") {
			return field
		}
	}

	return ""
}

// login signs in to Proton and stores the session, returning the bridge
// password the user must give to their apps.
func (b *bridge) login(ctx context.Context, username string, password []byte, twoFactor, mailboxPassword string, ask asker) (string, error) {
	if b.path == "" {
		return "", errors.New("no configuration directory to store the session in")
	}

	// An empty field means the account is not in two-password mode, so the
	// login password is also the one that unlocks the keys.
	mailbox := []byte(mailboxPassword)
	if len(mailbox) == 0 {
		mailbox = password
	}

	sess, err := proton.Login(ctx, username, password, &guiPrompter{
		mailbox:   mailbox,
		twoFactor: twoFactor,
		ask:       ask,
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
	paths := server.PathsFile(b.path)

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
			Paths:    paths,
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

// Command carbonate bridges Proton Calendar and Proton Contacts to local
// CalDAV and CardDAV endpoints, so standards-compliant clients can talk to
// Proton.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/develonrails/carbonate/internal/proton"
	"github.com/develonrails/carbonate/internal/session"
)

// version is overridden at build time via -ldflags.
var version = "dev"

// errUsage signals that the top-level usage text should be printed.
var errUsage = errors.New("usage")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		if errors.Is(err, errUsage) {
			usage(os.Stderr)
			os.Exit(2)
		}

		fmt.Fprintf(os.Stderr, "carbonate: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errUsage
	}

	switch args[0] {
	case "auth":
		return cmdAuth(ctx, args[1:], out)
	case "serve":
		return cmdServe(ctx, args[1:], out)
	case "version":
		fmt.Fprintln(out, version)
		return nil
	case "help", "-h", "--help":
		usage(out)
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `carbonate bridges Proton Calendar and Contacts to local CalDAV/CardDAV.

Usage:
  carbonate auth <username>   log in to Proton and store an encrypted session
  carbonate serve             serve CalDAV and CardDAV on localhost
  carbonate version           print the version

Environment:
  CARBONATE_BRIDGE_PASSWORD   bridge password, to run unattended
  CARBONATE_APP_VERSION       client version reported to Proton

Run "carbonate <command> -h" for command-specific flags.
`)
}

// cmdAuth performs the interactive Proton login and writes an encrypted
// session to disk, so that "serve" can run unattended afterwards.
func cmdAuth(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("auth", flag.ContinueOnError)
	fs.SetOutput(out)
	path := fs.String("session", "", "path to the session file (default: user config dir)")

	if err := fs.Parse(reorderArgs(args, authFlagsWithValues)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}

		return err
	}

	if fs.NArg() != 1 {
		return errors.New("auth requires exactly one argument: <username>")
	}
	username := fs.Arg(0)

	sessionPath, err := resolvePath(*path)
	if err != nil {
		return err
	}

	p := terminalPrompter{}

	loginPassword, err := p.Password(fmt.Sprintf("Proton password for %s: ", username))
	if err != nil {
		return err
	}

	sess, err := proton.Login(ctx, username, loginPassword, p)
	if err != nil {
		return err
	}

	bridgePassword, err := session.NewBridgePassword()
	if err != nil {
		return err
	}

	if err := session.Save(sessionPath, bridgePassword, sess); err != nil {
		return err
	}

	fmt.Fprintf(out, `
Logged in as %s.

Session written to %s

Bridge password: %s

This is shown once. It encrypts the session file, and your DAV clients will
use it as their password. Store it in your password manager now.
`, sess.Username, sessionPath, bridgePassword)

	return nil
}

// cmdServe starts the CalDAV and CardDAV endpoints and the Proton event-loop
// poller that keeps the local cache in sync.
func cmdServe(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(out)
	addr := fs.String("addr", "127.0.0.1:8080", "address to listen on (localhost only)")
	path := fs.String("session", "", "path to the session file (default: user config dir)")

	if err := fs.Parse(reorderArgs(args, serveFlagsWithValues)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}

		return err
	}

	sessionPath, err := resolvePath(*path)
	if err != nil {
		return err
	}

	bridgePassword := os.Getenv("CARBONATE_BRIDGE_PASSWORD")
	if bridgePassword == "" {
		buf, err := terminalPrompter{}.Password("Bridge password: ")
		if err != nil {
			return err
		}
		bridgePassword = string(buf)
	}

	sess, err := session.Load(sessionPath, bridgePassword)
	if err != nil {
		return err
	}

	store := &sessionStore{path: sessionPath, password: bridgePassword, sess: sess}

	conn, err := proton.Resume(ctx, sess, store.persist)
	if err != nil {
		return err
	}
	defer conn.Close()

	user, err := conn.Client.GetUser(ctx)
	if err != nil {
		return fmt.Errorf("fetching user: %w", err)
	}

	calendars, err := conn.Client.GetCalendars(ctx)
	if err != nil {
		return fmt.Errorf("fetching calendars: %w", err)
	}

	contacts, err := conn.Client.CountContacts(ctx)
	if err != nil {
		return fmt.Errorf("counting contacts: %w", err)
	}

	fmt.Fprintf(out, "Connected as %s: %d calendar(s), %d contact(s), keys unlocked.\n",
		user.Email, len(calendars), contacts)

	return fmt.Errorf("not implemented: would serve CalDAV and CardDAV on %s", *addr)
}

func resolvePath(override string) (string, error) {
	if override != "" {
		return override, nil
	}

	return session.DefaultPath()
}

// sessionStore rewrites the session file when Proton rotates the refresh
// token. Proton discards the old token at that moment, so this must not be
// skipped or deferred.
type sessionStore struct {
	mu       sync.Mutex
	path     string
	password string
	sess     *session.Session
}

func (s *sessionStore) persist(uid, refreshToken string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.sess.UID == uid && s.sess.RefreshToken == refreshToken {
		return nil
	}

	s.sess.UID = uid
	s.sess.RefreshToken = refreshToken

	return session.Save(s.path, s.password, s.sess)
}

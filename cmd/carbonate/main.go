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
	"syscall"

	"github.com/develonrails/carbonate/internal/proton"
	"github.com/develonrails/carbonate/internal/server"
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
	case "calendars":
		return cmdCalendars(ctx, args[1:], out)
	case "event":
		return cmdEvent(ctx, args[1:], out)
	case "contacts":
		return cmdContacts(ctx, args[1:], out)
	case "sessions":
		return cmdSessions(ctx, args[1:], out)
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
                              (-keep-bridge-password to keep the current one)
  carbonate serve             serve CalDAV and CardDAV on localhost
  carbonate calendars         list calendars, and optionally their events
  carbonate event put         create or replace an event from iCalendar on stdin
  carbonate event delete      delete an event by its iCalendar UID
  carbonate contacts          list contacts, optionally as vCards
  carbonate sessions          list Proton sessions; -revoke-others to clear them
  carbonate version           print the version

Unattended login reads answers from stdin, one line at a time, in the order
they are asked (password, then any two-factor code):

  carbonate auth alice@proton.me --password-stdin < secret-file

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
	passwordStdin := fs.Bool("password-stdin", false, "read the password, and any further answers, from stdin one line at a time")
	keepBridgePassword := fs.Bool("keep-bridge-password", false, "reuse the existing bridge password instead of generating a new one")

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

	var p proton.Prompter = terminalPrompter{}
	if *passwordStdin {
		p = newStdinPrompter(os.Stdin)
	}

	loginPassword, err := p.Password(fmt.Sprintf("Proton password for %s: ", username))
	if err != nil {
		return err
	}

	// Logging in again would otherwise mint a new bridge password and break
	// every client already configured with the old one.
	bridgePassword, reused, err := bridgePasswordForAuth(*keepBridgePassword)
	if err != nil {
		return err
	}

	// Logging in rewrites the session, so no other process may be holding it.
	guard, err := lockSession(sessionPath)
	if err != nil {
		return err
	}
	defer guard.Release()

	sess, err := proton.Login(ctx, username, loginPassword, p)
	if err != nil {
		return err
	}

	// Retire the session being replaced. Proton allows only so many, and an
	// abandoned one eventually costs a later session its scope.
	retireOldSession(ctx, sessionPath, bridgePassword, sess, out)

	if err := session.Save(sessionPath, bridgePassword, sess); err != nil {
		return err
	}

	if reused {
		fmt.Fprintf(out, "\nLogged in as %s. Session written to %s\n\nYour existing bridge password still works; clients need no change.\n",
			sess.Username, sessionPath)

		return nil
	}

	fmt.Fprintf(out, `
Logged in as %s.

Session written to %s

Bridge password: %s

This is shown once. It encrypts the session file, and your DAV clients will
use it as their password. Store it in your password manager now.

Logging in again generates a new one, which every configured client would
then reject. Pass -keep-bridge-password to keep this one.
`, sess.Username, sessionPath, bridgePassword)

	return nil
}

// retireOldSession revokes the session this login replaces, when it can be
// read. Failing to is not worth stopping a successful login over, so it is
// reported and passed by.
func retireOldSession(ctx context.Context, path, bridgePassword string, fresh *session.Session, out io.Writer) {
	old, err := session.Load(path, bridgePassword)
	if err != nil {
		// A new bridge password, or no session at all: nothing we can open.
		return
	}

	if old.UID == "" || old.UID == fresh.UID {
		return
	}

	// Resuming rotates the refresh token. Handing over the file the session
	// is about to be written to means the rotation is kept whatever happens
	// next.
	conn, err := proton.Resume(ctx, session.NewFile(path, bridgePassword, fresh))
	if err != nil {
		return
	}
	defer conn.Close()

	if err := conn.RevokeSession(ctx, old.UID); err != nil {
		fmt.Fprintf(out, "Note: the previous Proton session could not be retired (%v).\n", err)
	}
}

// bridgePasswordForAuth returns the password to encrypt the new session with,
// reporting whether an existing one was kept.
func bridgePasswordForAuth(keep bool) (string, bool, error) {
	if !keep {
		password, err := session.NewBridgePassword()

		return password, false, err
	}

	if password := os.Getenv("CARBONATE_BRIDGE_PASSWORD"); password != "" {
		return password, true, nil
	}

	buf, err := terminalPrompter{}.Password("Existing bridge password: ")
	if err != nil {
		return "", false, err
	}

	if len(buf) == 0 {
		return "", false, errors.New("no bridge password given")
	}

	return string(buf), true, nil
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

	bridgePassword, err := bridgePassword()
	if err != nil {
		return err
	}

	conn, err := resume(ctx, sessionPath, bridgePassword)
	if err != nil {
		return err
	}
	defer conn.Close()

	user, err := conn.Client.GetUser(ctx)
	if err != nil {
		return fmt.Errorf("fetching user: %w", err)
	}

	fmt.Fprintf(out, "Connected as %s.\n", user.Email)

	return server.Serve(ctx, conn, *addr, user.Email, bridgePassword, out)
}

func resolvePath(override string) (string, error) {
	if override != "" {
		return override, nil
	}

	return session.DefaultPath()
}

// connect loads the stored session and resumes it, prompting for the bridge
// password unless CARBONATE_BRIDGE_PASSWORD is set.
func connect(ctx context.Context, pathOverride string) (*proton.Conn, error) {
	sessionPath, err := resolvePath(pathOverride)
	if err != nil {
		return nil, err
	}

	password, err := bridgePassword()
	if err != nil {
		return nil, err
	}

	return resume(ctx, sessionPath, password)
}

// bridgePassword reads the bridge password from the environment, falling back
// to a terminal prompt.
func bridgePassword() (string, error) {
	if p := os.Getenv("CARBONATE_BRIDGE_PASSWORD"); p != "" {
		return p, nil
	}

	buf, err := terminalPrompter{}.Password("Bridge password: ")
	if err != nil {
		return "", err
	}

	return string(buf), nil
}

func resume(ctx context.Context, sessionPath, password string) (*proton.Conn, error) {
	guard, err := lockSession(sessionPath)
	if err != nil {
		return nil, err
	}

	store, err := session.OpenFile(sessionPath, password)
	if err != nil {
		guard.Release()

		return nil, err
	}

	conn, err := proton.Resume(ctx, store)
	if err != nil {
		guard.Release()

		return nil, err
	}

	conn.OnClose(func() { guard.Release() })

	return conn, nil
}

// lockSession claims the session for this process, explaining the refusal in
// terms of what the user can see rather than of file locks.
func lockSession(path string) (*session.Guard, error) {
	guard, err := session.Lock(path)
	if errors.Is(err, session.ErrLocked) {
		return nil, errors.New("carbonate is already running and using this session; stop it first, or point this command at another session with -session")
	}

	return guard, err
}

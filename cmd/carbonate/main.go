// Command carbonate bridges Proton Calendar and Proton Contacts to local
// CalDAV and CardDAV endpoints, so standards-compliant clients can talk to
// Proton.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

// version is overridden at build time via -ldflags.
var version = "dev"

// errUsage signals that the top-level usage text should be printed.
var errUsage = errors.New("usage")

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, errUsage) {
			usage(os.Stderr)
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "carbonate: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errUsage
	}
	switch args[0] {
	case "auth":
		return cmdAuth(args[1:])
	case "serve":
		return cmdServe(args[1:])
	case "version":
		fmt.Println(version)
		return nil
	case "help", "-h", "--help":
		usage(os.Stdout)
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

Run "carbonate <command> -h" for command-specific flags.
`)
}

// cmdAuth performs the interactive Proton login and writes an encrypted
// session to disk, so that "serve" can run unattended afterwards.
func cmdAuth(args []string) error {
	fs := flag.NewFlagSet("auth", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("auth requires exactly one argument: <username>")
	}
	return errors.New("not implemented: SRP login, 2FA and human verification")
}

// cmdServe starts the CalDAV and CardDAV endpoints and the Proton event-loop
// poller that keeps the local cache in sync.
func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:8080", "address to listen on (localhost only)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return fmt.Errorf("not implemented: would serve CalDAV and CardDAV on %s", *addr)
}

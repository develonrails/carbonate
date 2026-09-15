package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"
)

// cmdSessions shows the Proton sessions open for the account, and can clear
// out the ones carbonate is not using.
//
// Proton allows a limited number and strips scope from the older ones rather
// than refusing the newest, so an account that has accumulated sessions fails
// later on unrelated requests.
func cmdSessions(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("sessions", flag.ContinueOnError)
	fs.SetOutput(out)
	path := fs.String("session", "", "path to the session file (default: user config dir)")
	revoke := fs.Bool("revoke-stale", false, "end carbonate's own earlier sessions, leaving other clients alone")

	if err := fs.Parse(reorderArgs(args, calendarsFlagsWithValues)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}

		return err
	}

	conn, err := connect(ctx, *path)
	if err != nil {
		return err
	}
	defer conn.Close()

	sessions, err := conn.Sessions(ctx)
	if err != nil {
		return fmt.Errorf("listing sessions: %w", err)
	}

	fmt.Fprintf(out, "%d session(s) open for this account.\n", len(sessions))

	for _, s := range sessions {
		name := s.LocalizedClientName
		if name == "" {
			name = "(unnamed client)"
		}

		fmt.Fprintf(out, "  %s  %s\n", time.Unix(s.CreateTime, 0).Format("2006-01-02 15:04"), name)
	}

	if !*revoke {
		if len(sessions) > 5 {
			fmt.Fprintln(out, "\nThat is a lot. Proton takes access away from older sessions rather than\nrefusing new ones, so requests start failing for reasons that look unrelated.\nPass -revoke-stale to clear carbonate's own.")
		}

		return nil
	}

	// Revoked one at a time on purpose. Proton offers a call that ends every
	// other session at once, but that would sign the user out of the web app
	// and their phone as well — a side effect nobody asked for while tidying
	// up after a bridge.
	ours := clientName()
	current := conn.UID()
	ended := 0

	for _, s := range sessions {
		// Every carbonate session carries the same client name, including the
		// one doing the revoking.
		if s.UID == current || s.LocalizedClientName != ours || !bool(s.Revocable) {
			continue
		}

		if err := conn.RevokeSession(ctx, s.UID); err != nil {
			fmt.Fprintf(out, "  could not end the session from %s: %v\n", time.Unix(s.CreateTime, 0).Format("15:04"), err)

			continue
		}

		ended++
	}

	fmt.Fprintf(out, "\nEnded %d of carbonate's earlier sessions. Other clients were left alone.\n", ended)

	return nil
}

// clientName is how Proton labels the sessions carbonate opens, which follows
// from the app version it reports.
func clientName() string {
	return "Proton Mail for GNU/Linux"
}

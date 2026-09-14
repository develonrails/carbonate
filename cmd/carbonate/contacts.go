package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/develonrails/carbonate/internal/contacts"
)

// cmdContacts lists contacts, as a way to see what carbonate can decrypt
// without involving a DAV client.
func cmdContacts(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("contacts", flag.ContinueOnError)
	fs.SetOutput(out)
	path := fs.String("session", "", "path to the session file (default: user config dir)")
	full := fs.Bool("vcard", false, "print each contact as a vCard")

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

	all, err := contacts.List(ctx, conn)
	if err != nil {
		return err
	}

	if len(all) == 0 {
		fmt.Fprintln(out, "No contacts.")

		return nil
	}

	fmt.Fprintf(out, "%d contact(s)\n", len(all))

	for _, c := range all {
		name := c.Card.Value("FN")
		if name == "" {
			name = "(no name)"
		}

		email := c.Card.Value("EMAIL")
		if email != "" {
			email = "  <" + email + ">"
		}

		fmt.Fprintf(out, "  %s%s\n", name, email)

		if *full {
			fmt.Fprintf(out, "%s\n", c)
		}
	}

	return nil
}

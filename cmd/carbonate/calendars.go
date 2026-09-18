package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/develonrails/carbonate/internal/calendar"
)

// cmdCalendars lists calendars and their events. It exists to show what
// carbonate can actually decrypt, ahead of the CalDAV endpoints.
func cmdCalendars(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("calendars", flag.ContinueOnError)
	fs.SetOutput(out)
	path := fs.String("session", "", "path to the session file (default: user config dir)")
	events := fs.Bool("events", false, "list the events in each calendar")
	ics := fs.Bool("ics", false, "print every decrypted iCalendar property")

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

	calendars, err := calendar.List(ctx, conn)
	if err != nil {
		return err
	}

	if len(calendars) == 0 {
		fmt.Fprintln(out, "No calendars.")
		return nil
	}

	for _, cal := range calendars {
		count, err := conn.Client.CountCalendarEvents(ctx, cal.ID)
		if err != nil {
			return fmt.Errorf("counting events in %q: %w", cal.Name, err)
		}

		name := cal.Name
		if name == "" {
			name = "(unnamed)"
		}

		fmt.Fprintf(out, "\n%s — %d event(s)\n", name, count)

		if !*events && !*ics {
			continue
		}

		decoded, unreadable, err := calendar.Events(ctx, conn, cal.ID)
		if err != nil {
			return err
		}

		// Saying nothing here would make a missing event look like one that
		// was never there.
		if len(unreadable) > 0 {
			fmt.Fprintf(out, "  %d event(s) could not be decrypted and are not listed.\n", len(unreadable))
		}

		for _, e := range decoded {
			summary := e.Summary()
			if summary == "" {
				summary = "(no summary)"
			}

			unverified := ""
			if e.Unverified {
				unverified = "  (signature not verified)"
			}

			fmt.Fprintf(out, "  %s  %s%s\n", e.When(), summary, unverified)

			if e.Attendees > 0 {
				fmt.Fprintf(out, "    %d attendee(s)\n", e.Attendees)
			}

			if *ics {
				for _, line := range strings.Split(strings.TrimSpace(e.ICS()), "\r\n") {
					fmt.Fprintf(out, "    | %s\n", line)
				}
			}
		}
	}

	return nil
}

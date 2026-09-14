package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/develonrails/carbonate/internal/calendar"
)

// cmdEvent writes events to a calendar. It takes iCalendar on stdin, in the
// shape a CalDAV client would PUT, so the write path is exercised exactly as
// the DAV endpoint will use it.
func cmdEvent(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("event requires a subcommand: add")
	}

	switch args[0] {
	case "add":
		return cmdEventAdd(ctx, args[1:], out)
	default:
		return fmt.Errorf("unknown event subcommand %q", args[0])
	}
}

func cmdEventAdd(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("event add", flag.ContinueOnError)
	fs.SetOutput(out)
	path := fs.String("session", "", "path to the session file (default: user config dir)")
	calendarID := fs.String("calendar", "", "calendar ID to write to (default: the only calendar)")

	if err := fs.Parse(reorderArgs(args, eventFlagsWithValues)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}

		return err
	}

	ics, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("reading iCalendar from stdin: %w", err)
	}

	if len(ics) == 0 {
		return errors.New("no iCalendar on stdin")
	}

	conn, err := connect(ctx, *path)
	if err != nil {
		return err
	}
	defer conn.Close()

	id := *calendarID
	if id == "" {
		calendars, err := calendar.List(ctx, conn)
		if err != nil {
			return err
		}

		if len(calendars) != 1 {
			return fmt.Errorf("account has %d calendars; name one with -calendar", len(calendars))
		}

		id = calendars[0].ID
	}

	eventID, err := calendar.Create(ctx, conn, id, string(ics))
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "Created event %s\n", eventID)

	return nil
}

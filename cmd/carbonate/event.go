package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/develonrails/carbonate/internal/calendar"
	"github.com/develonrails/carbonate/internal/proton"
)

// cmdEvent writes events to a calendar. It takes iCalendar on stdin, in the
// shape a CalDAV client would PUT, so the write path is exercised exactly as
// the DAV endpoint will use it.
func cmdEvent(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("event requires a subcommand: put or delete")
	}

	switch args[0] {
	case "put":
		return cmdEventPut(ctx, args[1:], out)
	case "delete":
		return cmdEventDelete(ctx, args[1:], out)
	default:
		return fmt.Errorf("unknown event subcommand %q", args[0])
	}
}

func cmdEventPut(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("event put", flag.ContinueOnError)
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

	id, err := resolveCalendar(ctx, conn, *calendarID)
	if err != nil {
		return err
	}

	eventID, created, err := calendar.Put(ctx, conn, id, string(ics))
	if err != nil {
		return err
	}

	verb := "Updated"
	if created {
		verb = "Created"
	}

	fmt.Fprintf(out, "%s event %s\n", verb, eventID)

	return nil
}

func cmdEventDelete(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("event delete", flag.ContinueOnError)
	fs.SetOutput(out)
	path := fs.String("session", "", "path to the session file (default: user config dir)")
	calendarID := fs.String("calendar", "", "calendar ID (default: the only calendar)")
	uid := fs.String("uid", "", "iCalendar UID of the event to delete")

	if err := fs.Parse(reorderArgs(args, eventFlagsWithValues)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}

		return err
	}

	if *uid == "" {
		return errors.New("event delete requires -uid")
	}

	conn, err := connect(ctx, *path)
	if err != nil {
		return err
	}
	defer conn.Close()

	id, err := resolveCalendar(ctx, conn, *calendarID)
	if err != nil {
		return err
	}

	deleted, err := calendar.Delete(ctx, conn, id, *uid)
	if err != nil {
		return err
	}

	if !deleted {
		return fmt.Errorf("no event with UID %q", *uid)
	}

	fmt.Fprintf(out, "Deleted event %s\n", *uid)

	return nil
}

// resolveCalendar falls back to the account's only calendar when none is named.
func resolveCalendar(ctx context.Context, conn *proton.Conn, calendarID string) (string, error) {
	if calendarID != "" {
		return calendarID, nil
	}

	calendars, err := calendar.List(ctx, conn)
	if err != nil {
		return "", err
	}

	if len(calendars) != 1 {
		return "", fmt.Errorf("account has %d calendars; name one with -calendar", len(calendars))
	}

	return calendars[0].ID, nil
}

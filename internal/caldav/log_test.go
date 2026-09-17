package caldav

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

// An idle bridge must stay quiet, or the lines that matter are lost among the
// ones that say a client polled and nothing had happened.
func TestLoggingSaysNothingWhileNothingChanges(t *testing.T) {
	var log bytes.Buffer

	store := Logging(newFake(), &log)

	for range 3 {
		if _, err := store.ChangeToken(context.Background(), "cal-1"); err != nil {
			t.Fatalf("ChangeToken: %v", err)
		}
	}

	if log.Len() != 0 {
		t.Errorf("an unchanged calendar was reported: %s", log.String())
	}
}

func TestLoggingReportsAChange(t *testing.T) {
	var log bytes.Buffer

	fake := newFake()
	store := Logging(fake, &log)

	if _, err := store.ChangeToken(context.Background(), "cal-1"); err != nil {
		t.Fatalf("ChangeToken: %v", err)
	}

	fake.token = "token-2"

	if _, err := store.ChangeToken(context.Background(), "cal-1"); err != nil {
		t.Fatalf("ChangeToken: %v", err)
	}

	if !strings.Contains(log.String(), "reports a change") {
		t.Errorf("the change was not reported: %s", log.String())
	}
}

// "Nothing arrives from Proton" is the report this exists to answer, so a read
// that did happen has to be visible.
func TestLoggingReportsWhatWasRead(t *testing.T) {
	var log bytes.Buffer

	store := Logging(newFake(), &log)

	if _, err := store.Events(context.Background(), "cal-1"); err != nil {
		t.Fatalf("Events: %v", err)
	}

	if !strings.Contains(log.String(), "read 1 events") {
		t.Errorf("the read was not reported: %s", log.String())
	}
}

// A failure that reaches no client — a poll that could not read the token —
// would otherwise leave no trace at all.
func TestLoggingReportsAFailure(t *testing.T) {
	var log bytes.Buffer

	fake := newFake()
	fake.err = errors.New("Proton is unreachable")

	if _, err := Logging(fake, &log).Events(context.Background(), "cal-1"); err == nil {
		t.Fatal("a failing store reported success")
	}

	if !strings.Contains(log.String(), "Proton is unreachable") {
		t.Errorf("the failure was not reported: %s", log.String())
	}
}

func TestLoggingWithoutAWriterIsTheStoreItself(t *testing.T) {
	fake := newFake()

	if got := Logging(fake, nil); got != Store(fake) {
		t.Error("a store was wrapped although there is nowhere to log to")
	}
}

// A Proton ID is about ninety characters of base64, which would leave no room
// on the line for what happened to it.
func TestShortenedIDsStayIdentifiable(t *testing.T) {
	id := "7WcS1DSkFTERiPA3YO1zL3QmyCDvTQBjZwfINezH775Xxq2XManNs8xTpJTiNwCTTMZPWmhWGohMKWJUxxt3cw=="

	got := short(id)

	if len(got) > 12 {
		t.Errorf("short(%q) = %q, which is not short", id, got)
	}

	if !strings.HasPrefix(id, strings.TrimSuffix(got, "…")) {
		t.Errorf("short(%q) = %q, which does not identify it", id, got)
	}
}

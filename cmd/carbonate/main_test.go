package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRunVersion(t *testing.T) {
	var out bytes.Buffer

	if err := run(context.Background(), []string{"version"}, &out); err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := strings.TrimSpace(out.String()); got != version {
		t.Errorf("version output = %q, want %q", got, version)
	}
}

func TestRunHelp(t *testing.T) {
	for _, arg := range []string{"help", "-h", "--help"} {
		t.Run(arg, func(t *testing.T) {
			var out bytes.Buffer

			if err := run(context.Background(), []string{arg}, &out); err != nil {
				t.Fatalf("run: %v", err)
			}

			for _, want := range []string{"carbonate auth", "carbonate serve", "CARBONATE_BRIDGE_PASSWORD"} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("help output is missing %q", want)
				}
			}
		})
	}
}

func TestRunNoArgs(t *testing.T) {
	err := run(context.Background(), nil, &bytes.Buffer{})

	if !errors.Is(err, errUsage) {
		t.Errorf("run with no arguments: got %v, want errUsage", err)
	}
}

func TestRunUnknownCommand(t *testing.T) {
	err := run(context.Background(), []string{"frobnicate"}, &bytes.Buffer{})

	if err == nil {
		t.Fatal("unknown command succeeded")
	}

	if !strings.Contains(err.Error(), "frobnicate") {
		t.Errorf("error %q does not name the offending command", err)
	}
}

// "-h" on a subcommand is a request for help, not an error.
func TestSubcommandHelpIsNotAnError(t *testing.T) {
	for _, cmd := range []string{"auth", "serve"} {
		t.Run(cmd, func(t *testing.T) {
			var out bytes.Buffer

			if err := run(context.Background(), []string{cmd, "-h"}, &out); err != nil {
				t.Fatalf("run %s -h: %v", cmd, err)
			}

			if !strings.Contains(out.String(), "-session") {
				t.Errorf("%s -h did not document -session: %q", cmd, out.String())
			}
		})
	}
}

func TestAuthRequiresUsername(t *testing.T) {
	err := run(context.Background(), []string{"auth"}, &bytes.Buffer{})

	if err == nil {
		t.Fatal("auth without a username succeeded")
	}

	if !strings.Contains(err.Error(), "username") {
		t.Errorf("error %q does not mention the missing username", err)
	}
}

// Logging in again used to mint a new bridge password, which every client
// already configured with the old one would then reject.
func TestBridgePasswordForAuthCanKeepTheExistingOne(t *testing.T) {
	t.Setenv("CARBONATE_BRIDGE_PASSWORD", "already-in-use")

	got, reused, err := bridgePasswordForAuth(true)
	if err != nil {
		t.Fatalf("bridgePasswordForAuth: %v", err)
	}

	if !reused {
		t.Error("the existing password was not reported as reused")
	}

	if got != "already-in-use" {
		t.Errorf("password = %q, want the existing one", got)
	}
}

func TestBridgePasswordForAuthGeneratesByDefault(t *testing.T) {
	t.Setenv("CARBONATE_BRIDGE_PASSWORD", "already-in-use")

	got, reused, err := bridgePasswordForAuth(false)
	if err != nil {
		t.Fatalf("bridgePasswordForAuth: %v", err)
	}

	if reused {
		t.Error("a fresh login reported reusing a password")
	}

	if got == "already-in-use" {
		t.Error("the environment password was used without being asked for")
	}

	if len(got) < 16 {
		t.Errorf("generated password %q is too short to be a credential", got)
	}
}

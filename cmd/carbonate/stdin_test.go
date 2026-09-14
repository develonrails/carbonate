package main

import (
	"strings"
	"testing"
)

func TestStdinPrompterReadsSuccessiveLines(t *testing.T) {
	p := newStdinPrompter(strings.NewReader("secret\n123456\nmailbox\n"))

	password, err := p.Password("Proton password: ")
	if err != nil {
		t.Fatalf("Password: %v", err)
	}

	if string(password) != "secret" {
		t.Errorf("password = %q, want %q", password, "secret")
	}

	code, err := p.Line("Two-factor code: ")
	if err != nil {
		t.Fatalf("Line: %v", err)
	}

	if code != "123456" {
		t.Errorf("code = %q, want %q", code, "123456")
	}

	mailbox, err := p.Password("Mailbox password: ")
	if err != nil {
		t.Fatalf("Password: %v", err)
	}

	if string(mailbox) != "mailbox" {
		t.Errorf("mailbox password = %q, want %q", mailbox, "mailbox")
	}
}

// A file written with `printf` or `echo -n` has no trailing newline.
func TestStdinPrompterAcceptsUnterminatedFinalLine(t *testing.T) {
	p := newStdinPrompter(strings.NewReader("secret"))

	password, err := p.Password("Proton password: ")
	if err != nil {
		t.Fatalf("Password: %v", err)
	}

	if string(password) != "secret" {
		t.Errorf("password = %q, want %q", password, "secret")
	}
}

func TestStdinPrompterHandlesCRLF(t *testing.T) {
	p := newStdinPrompter(strings.NewReader("secret\r\n"))

	password, err := p.Password("Proton password: ")
	if err != nil {
		t.Fatalf("Password: %v", err)
	}

	if string(password) != "secret" {
		t.Errorf("password = %q, want %q", password, "secret")
	}
}

// Trimming must not reach into the password itself.
func TestStdinPrompterPreservesSignificantWhitespace(t *testing.T) {
	p := newStdinPrompter(strings.NewReader("pass word \n"))

	password, err := p.Password("Proton password: ")
	if err != nil {
		t.Fatalf("Password: %v", err)
	}

	if string(password) != "pass word " {
		t.Errorf("password = %q, want %q", password, "pass word ")
	}
}

// Running out of input must say what it was waiting for, not fail obscurely.
func TestStdinPrompterReportsExhaustedInput(t *testing.T) {
	p := newStdinPrompter(strings.NewReader("secret\n"))

	if _, err := p.Password("Proton password: "); err != nil {
		t.Fatalf("Password: %v", err)
	}

	_, err := p.Line("Two-factor code: ")
	if err == nil {
		t.Fatal("reading past the end of stdin succeeded")
	}

	if !strings.Contains(err.Error(), "Two-factor code") {
		t.Errorf("error %q does not name the unanswered prompt", err)
	}
}

func TestStdinPrompterEmptyInput(t *testing.T) {
	p := newStdinPrompter(strings.NewReader(""))

	if _, err := p.Password("Proton password: "); err == nil {
		t.Fatal("reading from empty stdin succeeded")
	}
}

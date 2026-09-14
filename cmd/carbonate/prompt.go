package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// terminalPrompter reads secrets and lines from the controlling terminal.
type terminalPrompter struct{}

// Password reads a secret without echoing it.
func (terminalPrompter) Password(prompt string) ([]byte, error) {
	fd := int(os.Stdin.Fd())

	if !term.IsTerminal(fd) {
		return nil, fmt.Errorf("cannot prompt for %q: stdin is not a terminal", strings.TrimSpace(prompt))
	}

	fmt.Fprint(os.Stderr, prompt)

	buf, err := term.ReadPassword(fd)

	// ReadPassword swallows the newline the user typed, so restore it to keep
	// subsequent output on its own line.
	fmt.Fprintln(os.Stderr)

	if err != nil {
		return nil, fmt.Errorf("reading password: %w", err)
	}

	return buf, nil
}

// Line reads a visible line, such as a TOTP code.
func (terminalPrompter) Line(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("reading input: %w", err)
	}

	return strings.TrimSpace(line), nil
}

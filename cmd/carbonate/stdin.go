package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

// stdinPrompter answers prompts by reading successive lines from a stream,
// in the order the login flow asks for them: password first, then any
// two-factor code or mailbox password.
//
// This is what makes unattended login possible — from a file, a password
// manager, or a CI secret — without a terminal.
type stdinPrompter struct {
	r *bufio.Reader
}

func newStdinPrompter(r io.Reader) *stdinPrompter {
	return &stdinPrompter{r: bufio.NewReader(r)}
}

func (p *stdinPrompter) Password(prompt string) ([]byte, error) {
	line, err := p.readLine(prompt)
	if err != nil {
		return nil, err
	}

	return []byte(line), nil
}

func (p *stdinPrompter) Line(prompt string) (string, error) {
	return p.readLine(prompt)
}

func (p *stdinPrompter) readLine(prompt string) (string, error) {
	line, err := p.r.ReadString('\n')

	// A final line need not be newline-terminated, so EOF with content is a
	// complete answer rather than a failure.
	if err != nil {
		if !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("reading stdin: %w", err)
		}

		if line == "" {
			return "", fmt.Errorf("stdin ended before answering %q", strings.TrimSpace(prompt))
		}
	}

	// Trim only the line ending: a password may legitimately end in a space.
	return strings.TrimRight(line, "\r\n"), nil
}

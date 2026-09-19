package main

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Running other CLIs is most of what this tool does - gh, git, kubectl,
// go - and a failed one has to say WHY. "exit status 1" cannot tell an
// expired gh token from a network outage from a repo that does not
// exist, and a person fixes those three differently.
//
// The standard library already captures what is needed and it is easy
// to miss: Cmd.Output() populates ExitError.Stderr when Cmd.Stderr is
// nil. Nothing was being thrown away - fmt just renders an ExitError as
// "exit status 1", so the field was there and nobody printed it.
//
// Cmd.Run() is the exception the docs call out: it does NOT populate
// the field, so a command run for its effect has to capture stderr
// itself. Verified both, including that Output() truncates only past
// about 32KB.
//
// A call whose failure IS the answer - asking whether a file exists
// yet, where a 404 is the common case - stays on plain Output() and
// ignores the error. Wrapping those would suggest a problem where there
// is none.

// capture runs argv and returns its stdout, with a failure that carries
// whatever the command said.
//
// what names the operation, so the message reads as a sentence about
// the task: "reading ChristopherScot/homelab main: exit status 1"
// rather than "exit status 1" alone.
func capture(what string, argv ...string) ([]byte, error) {
	out, err := exec.Command(argv[0], argv[1:]...).Output()
	if err != nil {
		return nil, withStderr(what, err)
	}
	return out, nil
}

// runQuiet runs argv for its effect, discarding stdout. stdin is
// optional; pass "" for none.
//
// Captures stderr explicitly because Run() does not populate
// ExitError.Stderr the way Output() does.
func runQuiet(what, stdin string, argv ...string) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%s: %w\n%s", what, err, msg)
		}
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}

// withStderr folds a command's own message into its error.
//
// On its own line: gh prints usage and kubectl prints a hint, so these
// are frequently several lines and folding them inline makes both
// unreadable.
func withStderr(what string, err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if msg := strings.TrimSpace(string(ee.Stderr)); msg != "" {
			return fmt.Errorf("%s: %w\n%s", what, err, msg)
		}
	}
	return fmt.Errorf("%s: %w", what, err)
}

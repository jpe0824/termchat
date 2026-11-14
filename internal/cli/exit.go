package cli

import (
	"errors"
	"strings"

	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/sanitize"
)

// Exit codes of the client commands. 2 (usage) is the usageError type.
const (
	exitOther      = 1
	exitUsage      = 2
	exitNotFound   = 3
	exitNoServer   = 4
	exitTimeout    = 5
	exitSendDenied = 6
	exitInterrupt  = 130
)

// exitError is an error with its exit code and a message that is safe to
// print: one line, no control characters.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

func exitf(code int, msg string) error {
	return &exitError{code: code, msg: oneLine(msg)}
}

// oneLine makes text that may carry user or server data safe for a single
// line of stderr.
func oneLine(s string) string {
	return strings.Join(strings.Fields(sanitize.Text(s)), " ")
}

// exitCodeFor maps the errors of package ipc to an exit code. It is the only
// place that does.
func exitCodeFor(err error) int {
	switch {
	case errors.Is(err, ipc.ErrSendDenied):
		return exitSendDenied
	case errors.Is(err, ipc.ErrNotFound), errors.Is(err, ipc.ErrAmbiguous):
		return exitNotFound
	case errors.Is(err, ipc.ErrNoAccount), errors.Is(err, ipc.ErrNotConnected),
		errors.Is(err, ipc.ErrNotAuthorised), errors.Is(err, ipc.ErrForbidden):
		return exitNoServer
	case errors.Is(err, ipc.ErrTimeout):
		return exitTimeout
	}
	return exitOther
}

// fail turns err into the error a client command returns: its exit code
// comes from exitCodeFor and its message is one sanitised line. Usage and
// exit errors pass through unchanged.
func fail(err error) error {
	var u *usageError
	var x *exitError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &u), errors.As(err, &x):
		return err
	}
	msg := err.Error()
	if errors.Is(err, ipc.ErrNotConnected) {
		msg = "not connected to the server; try again once it is reachable"
	}
	return exitf(exitCodeFor(err), msg)
}

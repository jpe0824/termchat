package ipc

import (
	"context"
	"errors"

	"github.com/jpe0824/termchat/internal/connector"
	"github.com/jpe0824/termchat/internal/store"
)

// Errors returned by Client methods. They cross the socket as stable codes
// with the original message kept, so test with errors.Is and show
// err.Error() to users.
var (
	ErrNotFound      = errors.New("not found")
	ErrAmbiguous     = errors.New("ambiguous")
	ErrSendDenied    = errors.New("sending from the command line is disabled (cli.send = \"deny\")")
	ErrNotConnected  = errors.New("not connected")
	ErrNotAuthorised = errors.New("not authorised")
	ErrForbidden     = errors.New("forbidden")
	ErrInvalid       = errors.New("invalid request")
	ErrRefused       = errors.New("refused from the command line: edit config.toml")
	// ErrTimeout means a request got no answer in time, or the server did
	// not confirm a change; the message says which.
	ErrTimeout         = errors.New("timed out")
	ErrVersionMismatch = errors.New("background process speaks a different protocol version")
	ErrNoAccount       = errors.New("no account configured; join a server first")
	// ErrShuttingDown means the background process was shutting down and
	// did not run the request: it had no effect and may be repeated.
	ErrShuttingDown = errors.New("the background process is shutting down")
	// ErrSubscriptionOverflow is SubscriptionErr's value when a subscription
	// ended because its reader fell too far behind.
	ErrSubscriptionOverflow = errors.New("subscription ended: events were not read fast enough")
	// ErrClosed means the connection to the background process ended.
	ErrClosed = errors.New("connection to the background process closed")
)

const (
	codeNotFound        = "not_found"
	codeAmbiguous       = "ambiguous"
	codeSendDenied      = "send_denied"
	codeNotConnected    = "not_connected"
	codeNotAuthorised   = "not_authorised"
	codeForbidden       = "forbidden"
	codeInvalid         = "invalid"
	codeRefused         = "refused"
	codeTimeout         = "timeout"
	codeVersionMismatch = "version_mismatch"
	codeNoAccount       = "no_account"
	codeShuttingDown    = "shutting_down"
	codeInternal        = "internal"
)

// codes maps each wire code to the errors sent as it. The first error of an
// entry is the sentinel a client gets back for the code.
var codes = []struct {
	code string
	errs []error
}{
	{codeNotFound, []error{ErrNotFound, store.ErrNotFound, connector.ErrNotFound}},
	{codeAmbiguous, []error{ErrAmbiguous}},
	{codeSendDenied, []error{ErrSendDenied}},
	{codeRefused, []error{ErrRefused}},
	{codeNoAccount, []error{ErrNoAccount}},
	{codeShuttingDown, []error{ErrShuttingDown}},
	{codeVersionMismatch, []error{ErrVersionMismatch, connector.ErrVersionMismatch}},
	// An unknown outcome may also wrap ErrOffline; it is reported as a
	// timeout, never as "not connected", because the change may have happened.
	{codeTimeout, []error{ErrTimeout, connector.ErrUnknownOutcome, context.DeadlineExceeded}},
	{codeNotConnected, []error{ErrNotConnected, connector.ErrOffline}},
	{codeNotAuthorised, []error{ErrNotAuthorised, connector.ErrNotAuthorised}},
	{codeForbidden, []error{ErrForbidden, connector.ErrForbidden}},
	{codeInvalid, []error{ErrInvalid, store.ErrInvalid, connector.ErrInvalid, connector.ErrTooLarge}},
}

type wireError struct {
	Code string `json:"code"`
	Msg  string `json:"msg"`
}

func encodeError(err error) *wireError {
	if err == nil {
		return nil
	}
	for _, c := range codes {
		for _, e := range c.errs {
			if errors.Is(err, e) {
				return &wireError{Code: c.code, Msg: err.Error()}
			}
		}
	}
	return &wireError{Code: codeInternal, Msg: err.Error()}
}

// codedError carries the original message and matches its sentinel.
type codedError struct {
	msg      string
	sentinel error
}

func (e *codedError) Error() string { return e.msg }
func (e *codedError) Unwrap() error { return e.sentinel }

func decodeError(w *wireError) error {
	if w == nil {
		return nil
	}
	for _, c := range codes {
		if c.code == w.Code {
			return &codedError{msg: w.Msg, sentinel: c.errs[0]}
		}
	}
	return errors.New(w.Msg)
}

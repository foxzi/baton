package httpx

import (
	"errors"
	"fmt"
)

// Error classes, spelled the way section 9.1 spells them. The engine maps
// them to its own classes without translation.
const (
	ClassTransient = "transient"
	ClassCommand   = "command"
	ClassTimeout   = "timeout"
	ClassPolicy    = "policy"
	ClassConfig    = "config"
)

// Error is a classified HTTP failure.
type Error struct {
	Class string
	Msg   string
	Err   error

	// Status is the response status, when the failure came with one.
	Status int

	// BodyTail is the beginning of the response body of a failed request, so
	// that the message says what the service complained about. It goes
	// through the secret redactor with everything else the runner writes.
	BodyTail string
}

func (e *Error) Error() string {
	message := e.Msg
	if e.Err != nil {
		message = fmt.Sprintf("%s: %v", message, e.Err)
	}
	if e.BodyTail != "" {
		message = fmt.Sprintf("%s: %s", message, e.BodyTail)
	}
	return message
}

func (e *Error) Unwrap() error { return e.Err }

// Class returns the class of an error, or the empty string when the error did
// not come from this package.
func Class(err error) string {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Class
	}
	return ""
}

// errorf builds an error of the given class.
func errorf(class, format string, args ...any) *Error {
	return &Error{Class: class, Msg: fmt.Sprintf(format, args...)}
}

// wrapf builds an error of the given class around a cause.
func wrapf(class string, cause error, format string, args ...any) *Error {
	return &Error{Class: class, Msg: fmt.Sprintf(format, args...), Err: cause}
}

// classForStatus classifies an unexpected response status: the statuses the
// specification calls transient are worth retrying, the rest are not.
func classForStatus(status int) string {
	switch {
	case status == 408, status == 429, status >= 500:
		return ClassTransient
	default:
		return ClassCommand
	}
}

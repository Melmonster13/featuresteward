// Package errs holds the sentinel errors shared by domain packages and
// mapped to HTTP statuses by the API.
package errs

import "errors"

var (
	ErrNotFound     = errors.New("not found")
	ErrConflict     = errors.New("already exists")
	ErrInvalid      = errors.New("invalid")
	ErrUnauthorized = errors.New("unauthorized")
	ErrForbidden    = errors.New("forbidden")
)

// Invalid returns an ErrInvalid carrying a message safe to show clients.
func Invalid(msg string) error { return errors.Join(ErrInvalid, errors.New(msg)) }

// Conflict returns an ErrConflict carrying a message safe to show clients.
func Conflict(msg string) error { return errors.Join(ErrConflict, errors.New(msg)) }

// Forbidden returns an ErrForbidden carrying a message safe to show clients.
func Forbidden(msg string) error { return errors.Join(ErrForbidden, errors.New(msg)) }

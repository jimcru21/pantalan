package domain

import (
	"errors"
	"fmt"
)

// Sentinel errors. Adapters wrap these so the HTTP layer can map them to
// status codes without knowing which store or runtime produced them.
var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid input")
	ErrConflict = errors.New("conflict")
)

func invalid(msg string) error { return fmt.Errorf("%w: %s", ErrInvalid, msg) }

// FieldError is a rule one field of an input breaks, so a form can show
// Msg beside that field. Field is the field's JSON name (e.g. "domain").
// It wraps Kind, ErrInvalid or ErrConflict, which sets the status code.
type FieldError struct {
	Field string
	Kind  error
	Msg   string
}

func (e *FieldError) Error() string { return e.Kind.Error() + ": " + e.Msg }

func (e *FieldError) Unwrap() error { return e.Kind }

// InvalidField is an ErrInvalid about one field.
func InvalidField(field, msg string) error {
	return &FieldError{Field: field, Kind: ErrInvalid, Msg: msg}
}

// ConflictingField is an ErrConflict about one field, e.g. a domain
// another App already has.
func ConflictingField(field, msg string) error {
	return &FieldError{Field: field, Kind: ErrConflict, Msg: msg}
}

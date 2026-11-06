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

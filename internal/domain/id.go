package domain

import (
	"crypto/rand"
	"strings"
)

// NewID returns a random identifier. It is lowercase because IDs end up in
// Docker image names, which must be lowercase.
func NewID() string { return strings.ToLower(rand.Text()) }

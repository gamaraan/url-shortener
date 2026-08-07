// Package shortcode generates random base62 short codes for shortened URLs.
// It uses crypto/rand for unpredictability and regenerates on collision (the
// caller detects a unique-constraint violation and asks for another). See
// development.md §3.1.
package shortcode

import (
	"crypto/rand"
	"errors"
	"fmt"
)

// Alphabet is the URL-safe base62 alphabet (ordered for lexicographic clarity).
const Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// ErrLength is returned by New when length is not positive.
var ErrLength = errors.New("shortcode: length must be positive")

// Generator produces random base62 codes of a fixed length.
type Generator struct {
	length int
}

// New returns a Generator that emits codes of the given length.
func New(length int) (*Generator, error) {
	if length <= 0 {
		return nil, fmt.Errorf("%w: got %d", ErrLength, length)
	}
	return &Generator{length: length}, nil
}

// Generate returns a random base62 code of the configured length. It panics
// only if the system CSPRNG fails (treated as fatal/unrecoverable).
func (g *Generator) Generate() string {
	buf := make([]byte, g.length)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("shortcode: crypto/rand failed: %v", err))
	}
	out := make([]byte, g.length)
	for i, b := range buf {
		out[i] = Alphabet[int(b)%len(Alphabet)]
	}
	return string(out)
}

// MustNew is like New but panics on error. Convenience for main.go where the
// length comes from validated config.
func MustNew(length int) *Generator {
	g, err := New(length)
	if err != nil {
		panic(err)
	}
	return g
}
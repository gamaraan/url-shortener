package shortcode_test

import (
	"strings"
	"testing"

	"github.com/gamaraan/url-shortener/backend/internal/shortcode"
)

func TestGenerate_LengthAndAlphabet(t *testing.T) {
	gen, err := shortcode.New(7)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for i := 0; i < 1000; i++ {
		code := gen.Generate()
		if len(code) != 7 {
			t.Fatalf("code length = %d, want 7 (code=%q)", len(code), code)
		}
		for _, r := range code {
			if !strings.ContainsRune(shortcode.Alphabet, r) {
				t.Fatalf("code %q contains non-alphabet rune %q", code, r)
			}
		}
	}
}

func TestGenerate_Uniqueness(t *testing.T) {
	gen, _ := shortcode.New(8)
	seen := make(map[string]struct{}, 5000)
	for i := 0; i < 5000; i++ {
		c := gen.Generate()
		if _, dup := seen[c]; dup {
			t.Fatalf("duplicate code %q after %d generations", c, i)
		}
		seen[c] = struct{}{}
	}
}

func TestNew_InvalidLength(t *testing.T) {
	if _, err := shortcode.New(0); err == nil {
		t.Fatal("expected error for length 0")
	}
	if _, err := shortcode.New(-1); err == nil {
		t.Fatal("expected error for negative length")
	}
}

func TestMustNew_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for invalid length")
		}
	}()
	_ = shortcode.MustNew(0)
}

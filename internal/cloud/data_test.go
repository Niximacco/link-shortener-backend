package data

import (
	"strings"
	"testing"
)

// ValidAddress is the one thing in this package that can be tested without a
// datastore behind it, and it is worth pinning: it stands in front of both the
// user kind and the call to auth.ajn.me, and the header injection case at the
// bottom of the invalid list is the reason it refuses control characters.
func TestValidAddress(t *testing.T) {
	valid := []string{"a@b.co", "anthony@nixon.dev", "first.last+tag@sub.example.com"}
	for _, address := range valid {
		if !ValidAddress(address) {
			t.Errorf("ValidAddress(%q) = false, want true", address)
		}
	}

	invalid := []string{
		"",
		"nobody",
		"@example.com",
		"nobody@",
		"nobody@localhost",
		"nobody@.com",
		"nobody@example.",
		"no body@example.com",
		"a@b.co\r\nbcc: someone@else.com",
		strings.Repeat("a", 250) + "@example.com",
	}
	for _, address := range invalid {
		if ValidAddress(address) {
			t.Errorf("ValidAddress(%q) = true, want false", address)
		}
	}
}

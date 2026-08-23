package magiclink

import (
	"strings"
	"testing"
)

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

func TestNewTokenIsUniqueAndHashed(t *testing.T) {
	seen := map[string]bool{}

	for i := 0; i < 100; i++ {
		token, tokenHash, err := newToken()
		if err != nil {
			t.Fatalf("newToken() returned %v", err)
		}

		if len(token) < 40 {
			t.Fatalf("token %q is shorter than expected", token)
		}

		if seen[token] {
			t.Fatalf("newToken() repeated a token")
		}
		seen[token] = true

		if strings.Contains(tokenHash, token) {
			t.Fatal("hash contains the plaintext token")
		}

		if tokenHash != hashToken(token) {
			t.Fatal("hashToken is not deterministic for the same token")
		}
	}
}

func TestBuildURLCarriesTokenAndNext(t *testing.T) {
	url := buildURL("tok en/+value", "/link/ABC")

	if !strings.Contains(url, "/auth/callback?") {
		t.Errorf("buildURL() = %q, want it to point at the callback", url)
	}

	if strings.Contains(url, "tok en/+value") {
		t.Errorf("buildURL() = %q, want the token query-escaped", url)
	}

	if !strings.Contains(url, "next=%2Flink%2FABC") {
		t.Errorf("buildURL() = %q, want it to carry next", url)
	}
}

func TestBuildURLOmitsEmptyNext(t *testing.T) {
	if url := buildURL("abc", ""); strings.Contains(url, "next=") {
		t.Errorf("buildURL() = %q, want no next parameter", url)
	}
}

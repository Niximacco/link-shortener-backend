package web

import "testing"

func TestSafeNextAllowsSitePaths(t *testing.T) {
	for _, next := range []string{"/", "/link/ABC123", "/login?next=/", "/a/b?q=1&r=2"} {
		if got := SafeNext(next); got != next {
			t.Errorf("SafeNext(%q) = %q, want it left alone", next, got)
		}
	}
}

func TestSafeNextRejectsOffSiteDestinations(t *testing.T) {
	offSite := []string{
		"",
		"https://evil.example",
		"//evil.example",
		`/\evil.example`,
		"http://evil.example/path",
		"evil.example",
		"/ok\r\nSet-Cookie: x=y",
	}

	for _, next := range offSite {
		if got := SafeNext(next); got != "/" {
			t.Errorf("SafeNext(%q) = %q, want %q", next, got, "/")
		}
	}
}

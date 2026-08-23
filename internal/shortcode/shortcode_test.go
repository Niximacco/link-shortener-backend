package shortcode

import "testing"

func TestValidAcceptsUsableCodes(t *testing.T) {
	for _, short := range []string{"A", "ABC123", "launch-2026", "my_link", "aB9", New()} {
		if !Valid(short) {
			t.Errorf("Valid(%q) = false, want true", short)
		}
	}
}

func TestValidRejectsUnusableCodes(t *testing.T) {
	unusable := map[string]string{
		"":       "empty",
		"a/b":    "a slash would make it two path segments",
		"a b":    "spaces",
		"a?b":    "starts a query string",
		"a#b":    "starts a fragment",
		"a.b":    "dots are not in the allowed set",
		"héllo":  "non-ascii",
		"a%2Fb":  "percent encoding",
		"login":  "reserved, the login page would shadow it",
		"LOGIN":  "reserved regardless of case",
		"api":    "reserved",
		"link":   "reserved",
		"links":  "reserved",
		"logout": "reserved",
		"auth":   "reserved",
	}

	for short, why := range unusable {
		if Valid(short) {
			t.Errorf("Valid(%q) = true, want false (%s)", short, why)
		}
	}

	long := ""
	for i := 0; i < 65; i++ {
		long += "a"
	}
	if Valid(long) {
		t.Error("Valid() accepted a 65 character code")
	}
}

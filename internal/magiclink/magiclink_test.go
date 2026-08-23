package magiclink

import (
	"strings"
	"testing"
	"time"
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

// sendsAgo turns a list of ages into the send times an entity would carry.
func sendsAgo(now time.Time, ages ...time.Duration) []int64 {
	sends := make([]int64, 0, len(ages))
	for _, age := range ages {
		sends = append(sends, now.Add(-age).Unix())
	}

	return sends
}

func TestOverSendLimitAllowsNormalUse(t *testing.T) {
	now := time.Now()

	cases := map[string][]int64{
		"a brand new address":     nil,
		"one link a moment ago":   sendsAgo(now, time.Minute),
		"a few spread over a day": sendsAgo(now, 20*time.Hour, 10*time.Hour, 2*time.Hour),
	}

	for name, sends := range cases {
		if OverSendLimit(sends, now) {
			t.Errorf("%s: should not be over the limit", name)
		}
	}
}

func TestOverSendLimitStopsAnHourlyFlood(t *testing.T) {
	now := time.Now()

	var ages []time.Duration
	for i := 0; i < SEND_LIMIT_HOUR; i++ {
		ages = append(ages, time.Duration(i+1)*time.Minute)
	}

	if !OverSendLimit(sendsAgo(now, ages...), now) {
		t.Fatalf("%d sends inside the hour should be over the hourly cap", SEND_LIMIT_HOUR)
	}
}

func TestTheHourlyCapLetsGoOnceTheHourPasses(t *testing.T) {
	now := time.Now()

	// The same flood, but all of it now older than the hourly window. The daily
	// cap is the only thing still counting, and this is under it.
	var ages []time.Duration
	for i := 0; i < SEND_LIMIT_HOUR; i++ {
		ages = append(ages, time.Duration(i+2)*time.Hour)
	}

	if OverSendLimit(sendsAgo(now, ages...), now) {
		t.Fatal("sends older than the hourly window should stop counting against it")
	}
}

func TestOverSendLimitStopsADailyFlood(t *testing.T) {
	now := time.Now()

	// Spread wide enough that no hour holds enough to trip the hourly cap, so
	// only the daily one can catch this.
	var ages []time.Duration
	for i := 0; i < SEND_LIMIT_DAY; i++ {
		ages = append(ages, time.Duration(i+1)*90*time.Minute)
	}

	sends := sendsAgo(now, ages...)

	if !OverSendLimit(sends, now) {
		t.Fatalf("%d sends inside the day should be over the daily cap", SEND_LIMIT_DAY)
	}

	// One fewer, and it is allowed - the cap is the ceiling, not the floor.
	if OverSendLimit(sends[1:], now) {
		t.Fatal("one under the daily cap should still be allowed")
	}
}

// A send stamped in the future is either clock skew or a hand-edited entity.
// Either way it must not read as "long ago" and hand back the allowance.
func TestFutureSendsCountAgainstTheLimit(t *testing.T) {
	now := time.Now()

	var ages []time.Duration
	for i := 0; i < SEND_LIMIT_HOUR; i++ {
		ages = append(ages, -time.Duration(i+1)*time.Hour)
	}

	if !OverSendLimit(sendsAgo(now, ages...), now) {
		t.Fatal("sends dated in the future were treated as expired")
	}
}

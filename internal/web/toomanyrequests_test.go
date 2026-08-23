package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// TestTooManyRequestsRendersARealPage covers the wiring rather than any
// counting: a caller turned away by a limiter gets the styled page and a
// Retry-After, not a broken template or a bare status. The limiters themselves
// are tested in the ratelimit package.
func TestTooManyRequestsRendersARealPage(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/login", nil)

	TooManyRequests(time.Minute)(c)

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("wanted 429, got %d", recorder.Code)
	}

	if retry := recorder.Header().Get("Retry-After"); retry != "60" {
		t.Errorf("wanted Retry-After 60, got %q", retry)
	}

	body := recorder.Body.String()

	if !strings.Contains(body, "Too many attempts") {
		t.Errorf("the message page did not render, got: %s", body)
	}

	// Whoever hit this was counted by connection, so nothing about accounts or
	// addresses should appear on the way out.
	if strings.Contains(strings.ToLower(body), "email address") {
		t.Errorf("the refusal page mentions email addresses: %s", body)
	}
}

package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthonynixon/link-shortener-backend/internal/types"
	"github.com/gin-gonic/gin"
)

// Templates fail at render time, not at compile time, so every page gets
// rendered here with everything populated. A typo in a field name shows up as a
// truncated page rather than a build error.
func render(t *testing.T, name string, page Page) string {
	t.Helper()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

	Render(c, http.StatusOK, name, page)

	if recorder.Code != http.StatusOK {
		t.Fatalf("Render(%s) wrote status %d, want 200", name, recorder.Code)
	}

	body := recorder.Body.String()
	if !strings.HasPrefix(body, "<!doctype html>") || !strings.Contains(body, "</html>") {
		t.Fatalf("Render(%s) did not produce a whole page:\n%s", name, body)
	}

	return body
}

func samplePage(title string) Page {
	page := New(title)
	page.Email = "someone@example.com"
	page.Next = "/link/ABC123"
	page.Token = "a-token-value"
	page.Error = "an error happened"
	page.Message = "a message"
	page.ExpiresMinutes = 15

	return page
}

func TestEveryPageRenders(t *testing.T) {
	for _, name := range []string{LoginPage, SentPage, ConfirmPage, DashboardPage, MessagePage} {
		body := render(t, name, samplePage("A Title"))

		if !strings.Contains(body, "A Title") {
			t.Errorf("Render(%s) left out the title", name)
		}

		if strings.Contains(body, "&lt;no value&gt;") || strings.Contains(body, "<no value>") {
			t.Errorf("Render(%s) referenced a field that doesn't exist", name)
		}
	}
}

func TestPagesCarryTheirOwnFields(t *testing.T) {
	page := samplePage("A Title")

	if body := render(t, LoginPage, page); !strings.Contains(body, `value="/link/ABC123"`) {
		t.Error("login page dropped the next path")
	}

	if body := render(t, SentPage, page); !strings.Contains(body, "15 minutes") {
		t.Error("sent page dropped the expiry")
	}

	if body := render(t, ConfirmPage, page); !strings.Contains(body, `value="a-token-value"`) {
		t.Error("confirm page dropped the token")
	}

	if body := render(t, DashboardPage, page); !strings.Contains(body, "someone@example.com") {
		t.Error("dashboard dropped the signed in address")
	}
}

func TestDashboardScriptGetsTheBaseURL(t *testing.T) {
	page := samplePage("Short links")
	page.BaseURL = "https://links.example"

	body := render(t, DashboardPage, page)
	if !strings.Contains(body, `const BASE_URL = "https://links.example"`) {
		t.Error("dashboard script did not get a quoted base url")
	}
}

func TestUserSuppliedValuesAreEscaped(t *testing.T) {
	page := samplePage("Sign in")
	page.Email = `"><script>alert(1)</script>`
	page.Next = `"><script>alert(2)</script>`

	body := render(t, LoginPage, page)

	if strings.Contains(body, "<script>alert(1)</script>") || strings.Contains(body, "<script>alert(2)</script>") {
		t.Errorf("login page did not escape user input:\n%s", body)
	}
}

func dashboardWithLinks() Page {
	page := samplePage("Short links")
	page.Error = ""
	page.Limit = 500
	page.Links = []types.Link{
		{Short: "ABC123", Long: "https://example.com/one", Clicks: 7, Created: 1755900000, CreatedBy: "owner@example.com"},
		{Short: "OLD", Long: "https://example.com/two", Clicks: 0, Created: 0, CreatedBy: ""},
	}

	return page
}

func TestDashboardListsLinks(t *testing.T) {
	body := render(t, DashboardPage, dashboardWithLinks())

	for _, want := range []string{"ABC123", "https://example.com/one", ">7<", "Aug 22, 2025"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard did not render %q", want)
		}
	}

	// A link created before the Created field existed carries a zero.
	if !strings.Contains(body, ">-<") {
		t.Error("dashboard rendered a zero timestamp as a date instead of a dash")
	}

	if strings.Contains(body, "You have not created a link yet") {
		t.Error("dashboard showed the empty state despite having links")
	}
}

func TestDashboardHidesAdminControlsFromNonAdmins(t *testing.T) {
	page := dashboardWithLinks()
	page.IsAdmin = false
	page.ShowingAll = false

	body := render(t, DashboardPage, page)

	if strings.Contains(body, "?all=1") {
		t.Error("dashboard offered the show-everyone toggle to a non-admin")
	}

	if strings.Contains(body, "owner@example.com") {
		t.Error("dashboard showed an owner column to a non-admin")
	}

	if strings.Contains(body, "admin") {
		t.Error("dashboard labelled a non-admin as admin")
	}
}

func TestDashboardShowsAdminControlsToAdmins(t *testing.T) {
	page := dashboardWithLinks()
	page.IsAdmin = true

	body := render(t, DashboardPage, page)
	if !strings.Contains(body, "?all=1") {
		t.Error("admin was not offered the show-everyone toggle")
	}

	page.ShowingAll = true
	body = render(t, DashboardPage, page)

	if !strings.Contains(body, "owner@example.com") {
		t.Error("owner column missing while showing everyone")
	}

	if !strings.Contains(body, "All links") {
		t.Error("heading did not switch to the everyone view")
	}
}

func TestDashboardEmptyState(t *testing.T) {
	page := samplePage("Short links")
	page.Error = ""
	page.Limit = 500

	body := render(t, DashboardPage, page)
	if !strings.Contains(body, "You have not created a link yet") {
		t.Error("dashboard did not render the empty state")
	}

	if strings.Contains(body, "Showing the first") {
		t.Error("dashboard claimed truncation with no links")
	}
}

func TestDashboardEscapesLinkValues(t *testing.T) {
	page := dashboardWithLinks()
	page.Links = []types.Link{{
		Short: `X"><script>alert(1)</script>`,
		Long:  `javascript:alert(2)`,
	}}

	body := render(t, DashboardPage, page)

	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Error("a short code escaped into markup")
	}

	if strings.Contains(body, `href="javascript:alert(2)"`) {
		t.Error("a javascript: destination was rendered as a live href")
	}
}

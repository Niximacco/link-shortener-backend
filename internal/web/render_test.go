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
	for _, name := range []string{LoginPage, SentPage, ConfirmPage, DashboardPage, MessagePage, UsersPage, TagsPage} {
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

func TestDashboardHidesAccessFromNonAdmins(t *testing.T) {
	page := dashboardWithLinks()
	page.IsAdmin = false

	body := render(t, DashboardPage, page)

	for _, control := range []string{`href="/users"`, "Manage users", "add-user", "new-admin"} {
		if strings.Contains(body, control) {
			t.Errorf("dashboard exposed the access control %q to a non-admin", control)
		}
	}
}

func TestDashboardShowsAccessToAdmins(t *testing.T) {
	page := dashboardWithLinks()
	page.IsAdmin = true

	body := render(t, DashboardPage, page)

	if !strings.Contains(body, `href="/users"`) {
		t.Error("dashboard did not link an admin to the access page")
	}
}

func usersPage() Page {
	page := New("Access")
	page.Email = "me@example.com"
	page.IsAdmin = true
	page.Limit = 500
	page.Users = []types.User{
		{Email: "me@example.com", Admin: true, Created: 1755900000, LastLogin: 1755990000},
		{Email: "someone@example.com", Created: 1755000000},
		{Email: "gone@example.com", Created: 1754000000, Disabled: true},
	}

	return page
}

func TestUsersPageListsEveryone(t *testing.T) {
	body := render(t, UsersPage, usersPage())

	for _, want := range []string{"me@example.com", "someone@example.com", "gone@example.com", "Admin", "User"} {
		if !strings.Contains(body, want) {
			t.Errorf("users page did not render %q", want)
		}
	}

	// A user who has never signed in has a zero LastLogin.
	if !strings.Contains(body, ">-<") && !strings.Contains(body, "-\n") {
		t.Error("users page did not render a never-signed-in user as a dash")
	}
}

func TestUsersPageMarksYouAndDisabled(t *testing.T) {
	body := render(t, UsersPage, usersPage())

	if !strings.Contains(body, `<span class="tag">you</span>`) {
		t.Error("users page did not mark the signed-in user")
	}

	if !strings.Contains(body, `<span class="tag off">disabled</span>`) {
		t.Error("users page did not mark the disabled user")
	}
}

func TestUsersPageCarriesTheAddForm(t *testing.T) {
	body := render(t, UsersPage, usersPage())

	for _, control := range []string{`id="add-user"`, `id="new-email"`, `id="new-admin"`, "Add user"} {
		if !strings.Contains(body, control) {
			t.Errorf("users page did not render %q", control)
		}
	}
}

func TestUsersPageEmptyState(t *testing.T) {
	page := usersPage()
	page.Users = nil

	body := render(t, UsersPage, page)
	if !strings.Contains(body, "Nobody can sign in yet") {
		t.Error("users page did not render the empty state")
	}
}

func TestUsersPageEscapesAddresses(t *testing.T) {
	page := usersPage()
	page.Users = []types.User{{Email: `x"><script>alert(1)</script>`}}

	body := render(t, UsersPage, page)
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Error("an address escaped into markup")
	}
}

func TestUsersPageOffersRoleAndAccessControls(t *testing.T) {
	body := render(t, UsersPage, usersPage())

	for _, control := range []string{"toggle-admin", "toggle-disabled", "Make admin", "Remove admin", "Disable", "Enable"} {
		if !strings.Contains(body, control) {
			t.Errorf("users page did not render the control %q", control)
		}
	}
}

func TestUsersPageWillNotLetYouChangeYourself(t *testing.T) {
	page := usersPage()
	body := render(t, UsersPage, page)

	// The signed-in admin's own row must carry no controls: demoting or
	// disabling yourself is how a service ends up with no working admin.
	rows := strings.Split(body, "<tr ")
	var own string
	for _, row := range rows {
		if strings.Contains(row, `data-email="me@example.com"`) {
			own = row
			break
		}
	}

	if own == "" {
		t.Fatal("could not find the signed-in user's row")
	}

	if strings.Contains(own, "data-action") {
		t.Errorf("the signed-in user's own row offered controls:\n%s", own)
	}

	if !strings.Contains(own, "you") {
		t.Error("the signed-in user's row was not marked")
	}
}

func TestUsersPageCarriesStateForTheToggles(t *testing.T) {
	body := render(t, UsersPage, usersPage())

	for _, want := range []string{`data-admin="true"`, `data-admin="false"`, `data-disabled="true"`, `data-disabled="false"`} {
		if !strings.Contains(body, want) {
			t.Errorf("users page did not render %q, so a toggle can't tell which way to flip", want)
		}
	}
}

func taggedDashboard() Page {
	page := dashboardWithLinks()
	page.Links = []types.Link{
		{Short: "ABC123", Long: "https://example.com/one", Clicks: 7, Created: 1755900000, CreatedBy: "owner@example.com", Tags: "work,urgent"},
		{Short: "PLAIN", Long: "https://example.com/two", Created: 1755900000, CreatedBy: "owner@example.com"},
	}
	page.Tags = []types.Tag{
		{Name: "work", Color: "#3b7dd8", Owner: "someone@example.com"},
		{Name: "urgent", Color: "#b4453c", Owner: "someone@example.com"},
	}

	return page
}

func TestDashboardRendersTagPills(t *testing.T) {
	body := render(t, DashboardPage, taggedDashboard())

	// Each tag is drawn in its own colour and links to its filtered view.
	for _, want := range []string{"#3b7dd8", "#b4453c", `href="/?tag=work"`, `href="/?tag=urgent"`} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard did not render %q", want)
		}
	}

	// ZgotmplZ is what html/template leaves behind when it refuses a value in a
	// css context. A colour that came out as that would silently be no colour.
	if strings.Contains(body, "ZgotmplZ") {
		t.Error("a colour was rejected by the template escaper instead of reaching the style attribute")
	}
}

func TestDashboardOffersTheTagFilters(t *testing.T) {
	body := render(t, DashboardPage, taggedDashboard())

	if !strings.Contains(body, `class="filters"`) {
		t.Error("dashboard did not render the filter row")
	}

	// With nothing filtered, "All" is the selected chip.
	if !strings.Contains(body, `class="pill filter on"`) {
		t.Error("dashboard did not mark a filter chip as selected")
	}
}

func TestDashboardMarksTheActiveFilter(t *testing.T) {
	page := taggedDashboard()
	page.Tag = "WORK"

	body := render(t, DashboardPage, page)

	// The active chip is matched case-insensitively, the way tags compare, and
	// clicking it clears the filter rather than reapplying it.
	if !strings.Contains(body, "work &times;") {
		t.Error("dashboard did not mark the active tag as the one to clear")
	}
}

func TestDashboardFilteredEmptyStateNamesTheTag(t *testing.T) {
	page := taggedDashboard()
	page.Tag = "personal"
	page.Links = nil

	body := render(t, DashboardPage, page)

	if !strings.Contains(body, "Nothing is tagged personal") {
		t.Error("dashboard did not explain that the filter is what emptied the list")
	}

	if strings.Contains(body, "You have not created a link yet") {
		t.Error("a filtered-to-nothing list claimed the user has no links at all")
	}
}

func TestDashboardCarriesTheTagEditorAndPicker(t *testing.T) {
	body := render(t, DashboardPage, taggedDashboard())

	for _, want := range []string{`id="tags"`, `list="tag-names"`, `id="tag-names"`, `value="work,urgent"`} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard did not render %q", want)
		}
	}
}

func TestDashboardLinksToTheTagsPage(t *testing.T) {
	body := render(t, DashboardPage, taggedDashboard())

	// Tags are everybody's, unlike the access page, so this is not admin-gated.
	page := taggedDashboard()
	page.IsAdmin = false

	for _, rendered := range []string{body, render(t, DashboardPage, page)} {
		if !strings.Contains(rendered, `href="/tags"`) {
			t.Error("dashboard did not link to the tags page")
		}
	}
}

func TestDashboardEscapesTagNames(t *testing.T) {
	page := taggedDashboard()
	page.Links = []types.Link{{Short: "X", Long: "https://example.com", Tags: `<script>alert(1)</script>`}}
	page.Tags = []types.Tag{{Name: `<script>alert(2)</script>`, Color: "#3b7dd8"}}

	body := render(t, DashboardPage, page)

	for _, escaped := range []string{"<script>alert(1)</script>", "<script>alert(2)</script>"} {
		if strings.Contains(body, escaped) {
			t.Errorf("a tag name escaped into markup: %q", escaped)
		}
	}
}

// A colour is written straight into a style attribute, so a value that isn't
// one has to become the default rather than reaching the page.
func TestDashboardRefusesAColorThatIsNotOne(t *testing.T) {
	page := taggedDashboard()
	page.Tags = []types.Tag{{Name: "work", Color: `#fff;background:url(//evil.example/x)`}}
	page.Links = []types.Link{{Short: "X", Long: "https://example.com", Tags: "work"}}

	body := render(t, DashboardPage, page)

	if strings.Contains(body, "evil.example") {
		t.Errorf("a crafted colour reached the style attribute:\n%s", body)
	}
}

// A link can carry a tag with no entity behind it: somebody else's tag on a
// link an admin is looking at, or one typed on a moment before the tag entity
// caught up. It still has to draw.
func TestDashboardDrawsTagsWithNoEntityBehindThem(t *testing.T) {
	page := taggedDashboard()
	page.Tags = nil
	page.Links = []types.Link{{Short: "X", Long: "https://example.com", Tags: "orphan"}}

	body := render(t, DashboardPage, page)

	if !strings.Contains(body, ">orphan</a>") {
		t.Error("a tag with no entity behind it was not drawn")
	}
}

func tagsPage() Page {
	page := New("Tags")
	page.Email = "someone@example.com"
	page.Limit = 200
	page.Palette = []string{"#3b7dd8", "#b4453c"}
	page.Tags = []types.Tag{
		{Name: "work", Color: "#3b7dd8", Owner: "someone@example.com", Created: 1755900000},
		{Name: "urgent", Color: "#b4453c", Owner: "someone@example.com", Created: 1755000000},
	}
	page.TagCounts = map[string]int{"work": 4, "urgent": 0}

	return page
}

func TestTagsPageListsTagsWithTheirCounts(t *testing.T) {
	body := render(t, TagsPage, tagsPage())

	for _, want := range []string{"work", "urgent", "#3b7dd8", "#b4453c", ">4<", "Aug 22, 2025"} {
		if !strings.Contains(body, want) {
			t.Errorf("tags page did not render %q", want)
		}
	}
}

func TestTagsPageCarriesTheAddFormAndPalette(t *testing.T) {
	body := render(t, TagsPage, tagsPage())

	for _, want := range []string{`id="add-tag"`, `id="new-name"`, `id="new-color"`, "Add tag", `class="swatch"`} {
		if !strings.Contains(body, want) {
			t.Errorf("tags page did not render %q", want)
		}
	}
}

func TestTagsPageOffersEditAndDelete(t *testing.T) {
	body := render(t, TagsPage, tagsPage())

	for _, control := range []string{`data-action="edit"`, `data-action="delete"`, `data-action="save"`, `data-name="work"`, `data-color="#3b7dd8"`} {
		if !strings.Contains(body, control) {
			t.Errorf("tags page did not render the control %q", control)
		}
	}
}

func TestTagsPageEmptyState(t *testing.T) {
	page := tagsPage()
	page.Tags = nil

	body := render(t, TagsPage, page)
	if !strings.Contains(body, "You have not made a tag yet") {
		t.Error("tags page did not render the empty state")
	}
}

func TestTagsPageEscapesTagNames(t *testing.T) {
	page := tagsPage()
	page.Tags = []types.Tag{{Name: `x"><script>alert(1)</script>`, Color: "#3b7dd8"}}

	body := render(t, TagsPage, page)
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Error("a tag name escaped into markup")
	}
}

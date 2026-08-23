package web

import (
	"embed"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/anthonynixon/link-shortener-backend/internal/config"
	"github.com/anthonynixon/link-shortener-backend/internal/types"
	"github.com/gin-gonic/gin"
)

// Templates ship inside the binary so there is nothing to mount or copy at
// deploy time, and a cold start doesn't touch the filesystem.
//
//go:embed templates/*.html
var templateFS embed.FS

const (
	LoginPage     = "login.html"
	SentPage      = "sent.html"
	ConfirmPage   = "confirm.html"
	DashboardPage = "dashboard.html"
	MessagePage   = "message.html"
	UsersPage     = "users.html"
)

var pages = map[string]*template.Template{}

// funcs are the helpers the templates can call.
var funcs = template.FuncMap{
	// date renders a unix timestamp. Links created before the field existed
	// carry a zero, which is worth showing as unknown rather than as 1970.
	"date": func(seconds int64) string {
		if seconds <= 0 {
			return "-"
		}

		return time.Unix(seconds, 0).UTC().Format("Jan 2, 2006")
	},
}

func init() {
	for _, page := range []string{LoginPage, SentPage, ConfirmPage, DashboardPage, MessagePage, UsersPage} {
		tmpl := template.New(page).Funcs(funcs)
		pages[page] = template.Must(tmpl.ParseFS(templateFS, "templates/base.html", "templates/"+page))
	}
}

// Page is everything the templates can render. Fields that don't apply to a
// given page are simply left empty.
type Page struct {
	Title          string
	SiteName       string
	BaseURL        string
	Email          string
	Next           string
	Token          string
	Error          string
	Message        string
	ExpiresMinutes int

	// Dashboard state.
	Links      []types.Link
	Users      []types.User
	IsAdmin    bool
	ShowingAll bool
	Limit      int
}

// New starts a Page with the site-wide values already filled in.
func New(title string) Page {
	return Page{
		Title:    title,
		SiteName: config.SITE_NAME,
		BaseURL:  config.BASE_URL,
	}
}

// Render writes an html page. Everything interpolated goes through
// html/template, so user-supplied values are escaped for their context.
func Render(c *gin.Context, status int, name string, page Page) {
	tmpl, ok := pages[name]
	if !ok {
		log.Printf("no such template: %s", name)
		c.String(http.StatusInternalServerError, "template error")
		return
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Status(status)

	if err := tmpl.ExecuteTemplate(c.Writer, "base", page); err != nil {
		log.Printf("could not render %s: %s", name, err.Error())
	}
}

// SafeNext sanitizes a "?next=" value so it can only ever send a browser to a
// path on this site. Anything that could resolve to another origin - an
// absolute url, a protocol-relative "//host", a backslash trick - collapses to
// the dashboard.
func SafeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") {
		return "/"
	}

	if strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return "/"
	}

	if strings.ContainsAny(next, "\r\n") {
		return "/"
	}

	return next
}

// TooManyRequests renders the page a caller gets when they have been turned
// away by a rate limit, with a Retry-After for anything that reads one. It says
// nothing about email addresses, because the limits that use it are counted per
// connection and never looked at one.
func TooManyRequests(retryAfter time.Duration) gin.HandlerFunc {
	seconds := strconv.Itoa(int(retryAfter.Seconds()))

	return func(c *gin.Context) {
		c.Header("Retry-After", seconds)

		page := New("Too many attempts")
		page.Error = "Too many attempts from your connection. Wait a minute and try again."
		Render(c, http.StatusTooManyRequests, MessagePage, page)
	}
}

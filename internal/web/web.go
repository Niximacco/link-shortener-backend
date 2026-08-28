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
	"github.com/anthonynixon/link-shortener-backend/internal/tags"
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
	DashboardPage = "dashboard.html"
	MessagePage   = "message.html"
	UsersPage     = "users.html"
	TagsPage      = "tags.html"
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

	// hex passes a colour into a style attribute. Everything that ends up in one
	// goes through here, and what makes that safe is the check rather than the
	// cast: a value that isn't a plain six digit hex - a colour edited by hand
	// onto an entity in the console, say - becomes the default instead of being
	// written into the page.
	"hex": hex,

	// tagColor is hex, looking the colour up by tag name first. A name with no
	// tag entity behind it gets the default: a link can carry a tag that isn't
	// in the list, either somebody else's on a link an admin is looking at or
	// one typed onto a link a moment before its entity was written.
	"tagColor": func(colors map[string]string, name string) template.CSS {
		return hex(colors[tags.Key(name)])
	},

	// readable picks black or white text for a background colour, so a pill is
	// legible whatever colour was chosen for it.
	"readable": func(color template.CSS) template.CSS {
		return template.CSS(tags.Readable(string(color)))
	},

	// tagCount reads a per-tag link count, keyed the way tags are compared.
	"tagCount": func(counts map[string]int, name string) int {
		return counts[tags.Key(name)]
	},

	// sameTag reports whether two names are the same tag, so the filter chips
	// can mark the active one.
	"sameTag": func(left string, right string) bool {
		return left != "" && tags.Key(left) == tags.Key(right)
	},
}

// hex is the one way a colour reaches a style attribute. Marking a value as
// template.CSS turns off the escaping that would otherwise protect the page, so
// the check in front of the cast is the whole point: what comes back is either
// a six digit hex value or the default, and never anything a caller wrote.
func hex(color string) template.CSS {
	if !tags.ValidColor(color) {
		return template.CSS(tags.DefaultColor)
	}

	return template.CSS(color)
}

func init() {
	for _, page := range []string{LoginPage, SentPage, DashboardPage, MessagePage, UsersPage, TagsPage} {
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
	Error          string
	Message        string
	ExpiresMinutes int

	// Dashboard state.
	Links      []types.Link
	Users      []types.User
	IsAdmin    bool
	ShowingAll bool
	Limit      int

	// Tag state. Tags is the caller's own set, whatever list is being shown
	// beside it; Tag is the one the list is filtered to, empty for no filter.
	Tags      []types.Tag
	Tag       string
	TagCounts map[string]int
	Palette   []string
}

// TagColors maps a tag name to the colour it should be drawn in, keyed the way
// tags are compared. A link can carry a tag whose entity isn't in the list -
// somebody else's tag on a link an admin is looking at, or one typed onto a
// link a moment before its entity was written - so a lookup that misses is
// normal and gets the default colour.
//
// Colours are checked on the way in, so what this returns is safe to write into
// a style attribute and safe to hand to a script that will do the same. That
// matters because it is the only shape a colour reaches the page in: the pages
// build their pills from this rather than from Tags directly.
func (p Page) TagColors() map[string]string {
	colors := make(map[string]string, len(p.Tags))
	for _, tag := range p.Tags {
		colors[tags.Key(tag.Name)] = string(hex(tag.Color))
	}

	return colors
}

// DefaultTagColor is the colour a name with no tag entity behind it is drawn
// in, for the scripts that build a pill in the browser.
func (p Page) DefaultTagColor() string {
	return tags.DefaultColor
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

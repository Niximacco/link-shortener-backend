package auth_handler

import (
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/anthonynixon/link-shortener-backend/internal/auth"
	data "github.com/anthonynixon/link-shortener-backend/internal/cloud"
	"github.com/anthonynixon/link-shortener-backend/internal/email"
	"github.com/anthonynixon/link-shortener-backend/internal/magiclink"
	"github.com/anthonynixon/link-shortener-backend/internal/ratelimit"
	"github.com/anthonynixon/link-shortener-backend/internal/web"
	"github.com/gin-gonic/gin"
)

// The two routes that turn an anonymous request into datastore work get a per
// caller ceiling. This is about the cost of being probed - datastore reads and
// instance time - rather than about email: an address that is not on the allow
// list never gets mailed anything, and the ones that are have their own caps in
// magiclink. Both are generous enough that a person retrying will not meet
// them, and per instance, so they are a brake on floods rather than a promise.
var (
	loginLimiter    = ratelimit.New(10, time.Minute)
	callbackLimiter = ratelimit.New(20, time.Minute)
)

func AddAuthV1(router *gin.Engine) {
	// The dashboard is the front door: signed in you get the link tools, signed
	// out you get bounced to /login.
	router.GET("/", auth.RequiredPage(), Dashboard)

	router.GET("/login", auth.Optional(), LoginPage)
	router.POST("/login", loginLimiter.Middleware(web.TooManyRequests(time.Minute)), auth.Optional(), RequestMagicLink)
	router.GET("/auth/callback", ConfirmLogin)
	router.POST("/auth/callback", callbackLimiter.Middleware(web.TooManyRequests(time.Minute)), CompleteLogin)
	router.POST("/logout", Logout)

	router.GET("/api/auth/session", auth.Required(), Session)
}

func Dashboard(c *gin.Context) {
	address := auth.Email(c)
	admin := auth.IsAdmin(c)

	// Showing everybody's links is an admin-only view, and only on request.
	showingAll := admin && c.Query("all") != ""

	owner := address
	if showingAll {
		owner = ""
	}

	page := web.New("Short links")
	page.Email = address
	page.IsAdmin = admin
	page.ShowingAll = showingAll
	page.Limit = data.LINK_LIST_LIMIT
	page.Tag = strings.TrimSpace(c.Query("tag"))

	links, err := data.ListLinks(owner, data.LINK_LIST_LIMIT)
	if err != nil {
		// The list failing shouldn't cost you the rest of the dashboard.
		log.Printf("could not list links: %s", err.Error())
		page.Error = "Your links couldn't be loaded. Creating one still works."
	}

	// Filtering happens here rather than in the query: a datastore filter on
	// tags alongside the CreatedBy one would need a composite index, and the
	// page is already bounded by LINK_LIST_LIMIT.
	page.Links = data.FilterByTag(links, page.Tag)

	// The tag list is always the caller's own, even for an admin looking at
	// everybody's links: tags are a private filing system, and the filter is
	// there to sort through your own labels. A link carrying somebody else's
	// tag still shows it, in the default colour.
	page.Tags, err = data.ListTags(address, data.TAG_LIST_LIMIT)
	if err != nil {
		// Losing the tag list costs the filters and the pill colours, which is
		// not worth losing the links over.
		log.Printf("could not list tags: %s", err.Error())
	}

	web.Render(c, http.StatusOK, web.DashboardPage, page)
}

func LoginPage(c *gin.Context) {
	next := web.SafeNext(c.Query("next"))

	if auth.IsSignedIn(c) {
		c.Redirect(http.StatusFound, next)
		return
	}

	page := web.New("Sign in")
	page.Next = next

	web.Render(c, http.StatusOK, web.LoginPage, page)
}

func RequestMagicLink(c *gin.Context) {
	next := web.SafeNext(c.PostForm("next"))
	address := strings.TrimSpace(c.PostForm("email"))

	page := web.New("Sign in")
	page.Next = next
	page.Email = address

	err := magiclink.Request(address, next)

	switch {
	case err == nil:
	// A link went out.

	case errors.Is(err, magiclink.ErrInvalidEmail):
		page.Error = "That doesn't look like an email address."
		web.Render(c, http.StatusBadRequest, web.LoginPage, page)
		return

	case errors.Is(err, magiclink.ErrNotAllowed), errors.Is(err, magiclink.ErrThrottled):
	// Both of these look exactly like success to the visitor. Saying "no such
	// user" here would turn the login form into an address checker, and saying
	// "slow down" would confirm the address exists.

	case errors.Is(err, email.ErrNotConfigured):
		log.Print("magic link requested but email sending is not configured")
		page.Title = "Sign in unavailable"
		page.Error = "Sign in is temporarily unavailable. Please try again later."
		web.Render(c, http.StatusServiceUnavailable, web.MessagePage, page)
		return

	default:
		log.Printf("could not send magic link: %s", err.Error())
		page.Title = "Something went wrong"
		page.Error = "We couldn't send your sign in link. Please try again."
		web.Render(c, http.StatusInternalServerError, web.MessagePage, page)
		return
	}

	page.Title = "Check your email"
	page.ExpiresMinutes = int(magiclink.TOKEN_VALID_TIME.Minutes())
	web.Render(c, http.StatusOK, web.SentPage, page)
}

// ConfirmLogin renders the "yes, it was me" step. Following the emailed link
// deliberately does not sign anyone in: mail scanners and link previewers fetch
// urls out of email, and a plain GET would let them burn the token before the
// real person ever clicked it.
func ConfirmLogin(c *gin.Context) {
	token := c.Query("token")

	page := web.New("Finish signing in")
	page.Next = web.SafeNext(c.Query("next"))

	if token == "" {
		page.Title = "That link is incomplete"
		page.Error = "This sign in link is missing its token. Request a new one."
		web.Render(c, http.StatusBadRequest, web.MessagePage, page)
		return
	}

	page.Token = token
	web.Render(c, http.StatusOK, web.ConfirmPage, page)
}

func CompleteLogin(c *gin.Context) {
	next := web.SafeNext(c.PostForm("next"))

	address, err := magiclink.Consume(c.PostForm("token"))
	if err != nil {
		page := web.New("That link didn't work")

		switch {
		case errors.Is(err, magiclink.ErrBadToken):
			page.Error = "This sign in link has already been used or has expired. Request a new one."
		case errors.Is(err, magiclink.ErrNotAllowed):
			page.Error = "That account can no longer sign in."
		default:
			log.Printf("could not complete login: %s", err.Error())
			page.Error = "Something went wrong signing you in. Please try again."
		}

		web.Render(c, http.StatusUnauthorized, web.MessagePage, page)
		return
	}

	if err = auth.StartSession(c, address); err != nil {
		log.Printf("could not issue session token: %s", err.Error())
		page := web.New("Something went wrong")
		page.Error = "We couldn't start your session. Please try again."
		web.Render(c, http.StatusInternalServerError, web.MessagePage, page)
		return
	}

	log.Printf("signed in %s", address)
	c.Redirect(http.StatusSeeOther, next)
}

func Logout(c *gin.Context) {
	auth.ClearSessionCookie(c)
	c.Redirect(http.StatusSeeOther, "/login")
}

// Session reports who the caller is. Hitting it also slides the session forward,
// same as any other authenticated request.
func Session(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"email": auth.Email(c)})
}

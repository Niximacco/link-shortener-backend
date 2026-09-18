package auth_handler

import (
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Niximacco/ajn_auth/pkg/authclient"
	"github.com/anthonynixon/link-shortener-backend/internal/auth"
	data "github.com/anthonynixon/link-shortener-backend/internal/cloud"
	"github.com/anthonynixon/link-shortener-backend/internal/ratelimit"
	"github.com/anthonynixon/link-shortener-backend/internal/web"
	"github.com/gin-gonic/gin"
)

// login is this site's half of the magic link service at auth.ajn.me. Minting
// the token, mailing it, hosting the "yes, it was me" page and the per-address
// send caps all live there now, because they were byte-identical in four sites
// and a bug in that email was four fixes.
//
// What did not move is the part that is actually ours: who may sign in. The
// service can say somebody proved they can read an address. It holds no user
// list and has no opinion about whether that address gets a session here.
var login = authclient.New()

// LINK_VALID_MINUTES is what the "check your email" page tells the visitor. The
// clock behind it belongs to the service, so this is a copy of a number set
// somewhere else - which is fine for a sentence of reassurance, and is why
// nothing here decides anything from it.
const LINK_VALID_MINUTES = 15

// The two routes that turn an anonymous request into datastore work get a per
// caller ceiling. This is about the cost of being probed - datastore reads and
// instance time - rather than about email: an address that is not on the allow
// list never reaches the service at all, and the ones that are have their own
// per-address caps there. Both are generous enough that a person retrying will
// not meet them, and per instance, so they are a brake on floods rather than a
// promise.
var (
	loginLimiter    = ratelimit.New(10, time.Minute)
	callbackLimiter = ratelimit.New(20, time.Minute)
	codeLimiter     = ratelimit.New(10, time.Minute)
)

func AddAuthV1(router *gin.Engine) {
	// Said at start rather than at somebody's first login, which is the other
	// place an unset key would be discovered.
	if !login.Configured() {
		log.Print("WARNING: AJN_AUTH_URL and/or AJN_AUTH_API_KEY are unset, magic link login is disabled")
	}

	// The dashboard is the front door: signed in you get the link tools, signed
	// out you get bounced to /login.
	router.GET("/", auth.RequiredPage(), Dashboard)

	router.GET("/login", auth.Optional(), LoginPage)
	router.POST("/login", loginLimiter.Middleware(web.TooManyRequests(time.Minute)), auth.Optional(), RequestMagicLink)
	router.GET("/auth/callback", callbackLimiter.Middleware(web.TooManyRequests(time.Minute)), CompleteLogin)
	router.POST("/login/code", codeLimiter.Middleware(web.TooManyRequests(time.Minute)), CompleteLoginByCode)
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
	address := data.NormalizeEmail(c.PostForm("email"))

	page := web.New("Sign in")
	page.Next = next
	page.Email = address

	// Junk is worth saying so about before anything else happens. This is a
	// check on the shape of the string and tells the visitor nothing about who
	// has an account here, which is what the rest of this function is careful
	// of.
	if !data.ValidAddress(address) {
		page.Error = "That doesn't look like an email address."
		web.Render(c, http.StatusBadRequest, web.LoginPage, page)
		return
	}

	// The user list is ours and stays ours. An address that is not on it gets
	// the same page as one that is, and no call is made - so the sign in form
	// cannot be used to work out who has an account here, and a stranger's
	// address never costs us an email.
	if _, err := data.GetUser(address); err == nil {
		switch _, err := login.RequestLink(c, address, next); {
		case err == nil:
			// Sent or throttled. The two come back the same way on purpose and
			// are rendered the same way here: telling one address "slow down"
			// and another "check your email" is the address checker again.

		case errors.Is(err, authclient.ErrInvalidEmail):
			page.Error = "That doesn't look like an email address."
			web.Render(c, http.StatusBadRequest, web.LoginPage, page)
			return

		case errors.Is(err, authclient.ErrNotConfigured), errors.Is(err, authclient.ErrUnauthorized):
			// Ours to fix rather than theirs: no key, a wrong url, or a key
			// this site no longer holds. Worth its own log line, because
			// nothing else in the service will notice.
			log.Printf("this site cannot ask ajn auth for a link: %s", err.Error())
			page.Title = "Sign in unavailable"
			page.Error = "Sign in is temporarily unavailable. Please try again later."
			web.Render(c, http.StatusServiceUnavailable, web.MessagePage, page)
			return

		default:
			log.Printf("could not send a magic link: %s", err.Error())
			page.Title = "Sign in unavailable"
			page.Error = "We couldn't send your sign in link. Please try again."
			web.Render(c, http.StatusServiceUnavailable, web.MessagePage, page)
			return
		}
	}

	page.Title = "Check your email"
	page.ExpiresMinutes = LINK_VALID_MINUTES
	web.Render(c, http.StatusOK, web.SentPage, page)
}

// CompleteLogin turns an exchange code into a session.
//
// The confirm step that used to be here - "yes, it was me", which is what kept
// a mail scanner from burning the token on its way past - moved to the service
// along with the token itself. What lands on this route now is a code that has
// already been through it, arriving on one redirect and worth nothing a moment
// later.
func CompleteLogin(c *gin.Context) {
	identity, err := login.Redeem(c, c.Query("code"))
	if err != nil {
		page := web.New("That link didn't work")

		if errors.Is(err, authclient.ErrBadCode) {
			page.Error = "This sign in link has already been used or has expired. Request a new one."
			web.Render(c, http.StatusUnauthorized, web.MessagePage, page)
			return
		}

		log.Printf("could not redeem a login code: %s", err.Error())
		page.Title = "Sign in unavailable"
		page.Error = "Something went wrong signing you in. Please try again."
		web.Render(c, http.StatusServiceUnavailable, web.MessagePage, page)
		return
	}

	finishLogin(c, identity)
}

// CompleteLoginByCode is the typed alternative to the link: the six digit code
// from the same email, entered on the "check your email" page. It is for
// somebody reading their mail on a phone and signing in on a laptop.
//
// The address comes from a hidden field, so it is the visitor's to change -
// which is fine, because a code only works for the address it was mailed to.
// Guesses are counted and capped by the service, not here.
func CompleteLoginByCode(c *gin.Context) {
	address := data.NormalizeEmail(c.PostForm("email"))

	identity, err := login.VerifyCode(c, address, c.PostForm("code"))
	if err != nil {
		if errors.Is(err, authclient.ErrBadCode) {
			// Back to the same page, with the form still on it. A typo is the
			// ordinary case, and the address has to survive for the retry.
			page := web.New("Check your email")
			page.Email = address
			page.Next = web.SafeNext(c.PostForm("next"))
			page.ExpiresMinutes = LINK_VALID_MINUTES
			page.Error = "That code didn't work. Check it and try again, or ask for a new email."
			web.Render(c, http.StatusUnauthorized, web.SentPage, page)
			return
		}

		log.Printf("could not verify a login code: %s", err.Error())
		page := web.New("Sign in unavailable")
		page.Error = "Something went wrong signing you in. Please try again."
		web.Render(c, http.StatusServiceUnavailable, web.MessagePage, page)
		return
	}

	finishLogin(c, identity)
}

// finishLogin is where both ways in end: the user list again, a session, and
// the redirect.
func finishLogin(c *gin.Context, identity authclient.Identity) {
	// Ask the user list again. The email this login came from can sit in an
	// inbox for a quarter of an hour, and an account can be removed in fourteen
	// minutes of that.
	address := data.NormalizeEmail(identity.Email)
	if _, err := data.GetUser(address); err != nil {
		page := web.New("That account can no longer sign in")

		if errors.Is(err, data.UserNotFoundErr) || errors.Is(err, data.UserDisabledErr) {
			page.Error = "That account can no longer sign in."
			web.Render(c, http.StatusUnauthorized, web.MessagePage, page)
			return
		}

		log.Printf("could not check who is signing in: %s", err.Error())
		page.Title = "Something went wrong"
		page.Error = "Something went wrong signing you in. Please try again."
		web.Render(c, http.StatusInternalServerError, web.MessagePage, page)
		return
	}

	if err := auth.StartSession(c, address); err != nil {
		log.Printf("could not issue session token: %s", err.Error())
		page := web.New("Something went wrong")
		page.Error = "We couldn't start your session. Please try again."
		web.Render(c, http.StatusInternalServerError, web.MessagePage, page)
		return
	}

	// The sign in already happened; a failure to write it down is not worth
	// turning anybody away for.
	if err := data.MarkLoggedIn(address, time.Now()); err != nil {
		log.Printf("could not record login time: %s", err.Error())
	}

	log.Printf("signed in %s", address)

	// The service hands back the next it was given, untouched and unexamined.
	// It is our value, so it goes through our own sanitizing before a browser
	// is pointed at it.
	c.Redirect(http.StatusSeeOther, web.SafeNext(identity.Next))
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

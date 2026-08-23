package auth_handler

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/anthonynixon/link-shortener-backend/internal/auth"
	"github.com/anthonynixon/link-shortener-backend/internal/email"
	"github.com/anthonynixon/link-shortener-backend/internal/magiclink"
	"github.com/anthonynixon/link-shortener-backend/internal/web"
	"github.com/gin-gonic/gin"
)

func AddAuthV1(router *gin.Engine) {
	// The dashboard is the front door: signed in you get the link tools, signed
	// out you get bounced to /login.
	router.GET("/", auth.RequiredPage(), Dashboard)

	router.GET("/login", auth.Optional(), LoginPage)
	router.POST("/login", auth.Optional(), RequestMagicLink)
	router.GET("/auth/callback", ConfirmLogin)
	router.POST("/auth/callback", CompleteLogin)
	router.POST("/logout", Logout)

	router.GET("/api/auth/session", auth.Required(), Session)
}

func Dashboard(c *gin.Context) {
	page := web.New("Short links")
	page.Email = auth.Email(c)

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

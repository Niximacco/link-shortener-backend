package user_handler

import (
	"errors"
	"log"
	"net/http"

	"github.com/anthonynixon/link-shortener-backend/internal/auth"
	data "github.com/anthonynixon/link-shortener-backend/internal/cloud"
	"github.com/anthonynixon/link-shortener-backend/internal/web"
	"github.com/gin-gonic/gin"
)

func AddUserV1(router *gin.Engine) {
	router.GET("/users", auth.RequiredPage(), RequireAdminPage(), ShowUsers)
	router.POST("/users", auth.Required(), RequireAdmin(), CreateUser)
	router.PATCH("/users/:email", auth.Required(), RequireAdmin(), UpdateUser)
}

// RequireAdminPage is RequireAdmin for html routes: a person who followed a
// link somewhere they can't go should get a page explaining that, not json.
func RequireAdminPage() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !auth.IsAdmin(c) {
			page := web.New("Admins only")
			page.Email = auth.Email(c)
			page.Error = "You need to be an admin to manage who can sign in."
			web.Render(c, http.StatusForbidden, web.MessagePage, page)
			c.Abort()
			return
		}

		c.Next()
	}
}

// ShowUsers renders the page listing everyone who is allowed to sign in.
func ShowUsers(c *gin.Context) {
	page := web.New("Access")
	page.Email = auth.Email(c)
	page.IsAdmin = true
	page.Limit = data.USER_LIST_LIMIT

	users, err := data.ListUsers(data.USER_LIST_LIMIT)
	if err != nil {
		// Adding somebody still works even when the roster won't load.
		log.Printf("could not list users: %s", err.Error())
		page.Error = "The list of users couldn't be loaded."
	}

	page.Users = users

	web.Render(c, http.StatusOK, web.UsersPage, page)
}

// userUpdate is the body of a user PATCH. The pointers distinguish "leave this
// alone" from "set this to false".
type userUpdate struct {
	Admin    *bool `json:"admin"`
	Disabled *bool `json:"disabled"`
}

// UpdateUser changes somebody's role or blocks them from signing in.
//
// An admin may not change their own role or block themselves. That single rule
// also guarantees the service can never be left without a working admin: only
// an admin can demote anyone, and they can't demote themselves, so the last one
// standing cannot be removed by anybody.
func UpdateUser(c *gin.Context) {
	address := data.NormalizeEmail(c.Param("email"))
	if address == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "which user?"})
		return
	}

	if address == data.NormalizeEmail(auth.Email(c)) {
		c.JSON(http.StatusForbidden, gin.H{"error": "you can't change your own role or block yourself - ask another admin"})
		return
	}

	var request userUpdate
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if request.Admin == nil && request.Disabled == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "nothing to change"})
		return
	}

	user, err := data.UpdateUser(address, request.Admin, request.Disabled)
	if err != nil {
		if errors.Is(err, data.UserNotFoundErr) {
			c.JSON(http.StatusNotFound, gin.H{"error": "no such user"})
			return
		}

		log.Printf("could not update user: %s", err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Worth an audit line: these are the calls that change who can do what.
	log.Printf("%s updated user %s (admin=%v disabled=%v)", auth.Email(c), address, request.Admin, request.Disabled)

	c.JSON(http.StatusOK, user)
}

// newUser is the body of a create request.
type newUser struct {
	Email string `json:"email"`
	Admin bool   `json:"admin"`
}

// RequireAdmin rejects anyone who isn't an admin. It runs after auth.Required,
// so by this point there is a valid session; this decides what that session is
// allowed to do. Admin status is read from datastore on every request rather
// than carried in the token, so revoking it takes effect immediately instead of
// waiting out a 30 day cookie.
func RequireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !auth.IsAdmin(c) {
			log.Printf("refused an admin-only request from a non-admin")
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "that's an admin-only action"})
			return
		}

		c.Next()
	}
}

// CreateUser adds an address to the allow list. Having a user entity is the
// whole of what grants access, so this is the "invite" step: the person can
// then sign in themselves at /login.
func CreateUser(c *gin.Context) {
	var request newUser
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	address := data.NormalizeEmail(request.Email)
	if !data.ValidAddress(address) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "that doesn't look like an email address"})
		return
	}

	user, err := data.NewUser(address, request.Admin)
	if err != nil {
		if errors.Is(err, data.AlreadyExistsErr) {
			c.JSON(http.StatusConflict, gin.H{"error": "that address can already sign in"})
			return
		}

		log.Printf("could not add user: %s", err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Worth an audit line: this is the one call that widens who can get in.
	log.Printf("%s added user %s (admin=%t)", auth.Email(c), address, request.Admin)

	c.JSON(http.StatusCreated, user)
}

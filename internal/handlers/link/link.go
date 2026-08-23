package link

import (
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/anthonynixon/link-shortener-backend/internal/auth"
	data "github.com/anthonynixon/link-shortener-backend/internal/cloud"
	"github.com/anthonynixon/link-shortener-backend/internal/shortcode"
	"github.com/anthonynixon/link-shortener-backend/internal/types"
	"github.com/gin-gonic/gin"
)

func AddLinkV1(router *gin.Engine) {
	router.GET("/:short", RedirectToLink)
	router.GET("/links", auth.Required(), ListLinks)
	router.GET("/link/:short", auth.Required(), GetLongLink)
	router.POST("/link", auth.Required(), CreateShortLink)
	router.PATCH("/link/:short", auth.Required(), UpdateLink)
	router.DELETE("/link/:short", auth.Required(), DeleteLink)
}

// linkUpdate is the body of a PATCH. Both fields are optional; whichever is
// present gets changed.
type linkUpdate struct {
	Long  string `json:"long"`
	Short string `json:"short"`
}

func getLinkDetails(short string) (link types.Link, err error) {
	link, err = data.GetLink(short)
	return
}

// scope works out what the caller is allowed to touch. Admins get "", which
// every data-layer ownership check reads as "no restriction"; everybody else
// gets their own address and is held to their own links.
func scope(c *gin.Context) (email string, owner string, admin bool) {
	email = auth.Email(c)
	admin = data.IsAdmin(email)

	if admin {
		return email, "", true
	}

	return email, email, false
}

// respond turns a data-layer error into the right status. Not-found and
// not-owned deliberately both come back as 404 for non-admins: telling somebody
// a code exists but isn't theirs is more than they need to know.
func respond(c *gin.Context, err error, admin bool) {
	switch {
	case errors.Is(err, data.NotFoundErr):
		c.JSON(http.StatusNotFound, gin.H{"error": "that link doesn't exist"})
	case errors.Is(err, data.NotOwnedErr):
		if admin {
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "that link doesn't exist"})
	case errors.Is(err, data.AlreadyExistsErr):
		c.JSON(http.StatusConflict, gin.H{"error": "that short code is already taken"})
	default:
		log.Printf("link operation failed: %s", err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	}
}

func ListLinks(c *gin.Context) {
	_, owner, admin := scope(c)

	// Only an admin can ask for everybody's links, and only by asking.
	if admin && c.Query("all") == "" {
		owner = auth.Email(c)
	}

	links, err := data.ListLinks(owner, data.LINK_LIST_LIMIT)
	if err != nil {
		respond(c, err, admin)
		return
	}

	c.JSON(http.StatusOK, gin.H{"links": links, "admin": admin})
}

func GetLongLink(c *gin.Context) {
	short := c.Param("short")
	link, err := getLinkDetails(short)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, link)
}

func RedirectToLink(c *gin.Context) {
	short := c.Param("short")
	short = strings.ToUpper(short)
	link, err := getLinkDetails(short)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "That link doesn't exist"})
		return
	}

	// Counted before the redirect is written, not in a goroutine after it.
	// Cloud Run throttles the container's cpu as soon as the response goes out,
	// so work started here would often be frozen before it reached datastore.
	if err = data.IncrementClicks(link.Short); err != nil {
		log.Printf("could not count a click on %s: %s", link.Short, err.Error())
	}

	c.Redirect(http.StatusFound, link.Long)
}

func CreateShortLink(c *gin.Context) {
	var newLink types.Link
	err := c.ShouldBindJSON(&newLink)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if strings.TrimSpace(newLink.Long) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a destination is required"})
		return
	}

	newLink.CreatedBy = auth.Email(c)

	if newLink.Short == "" {
		newLink.Short = shortcode.New()
	} else if !shortcode.Valid(newLink.Short) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "short codes can only use letters, numbers, - and _, and can't be a word this site already uses"})
		return
	}

	newLink.Created = time.Now().Unix()

	err = data.NewLink(newLink)
	if err != nil {
		if errors.Is(err, data.AlreadyExistsErr) {
			c.JSON(http.StatusConflict, gin.H{"error": "that short code is already taken"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}

	newLink.Short = strings.ToUpper(newLink.Short)
	c.JSON(http.StatusCreated, newLink)
}

func UpdateLink(c *gin.Context) {
	_, owner, admin := scope(c)

	var update linkUpdate
	if err := c.ShouldBindJSON(&update); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	update.Long = strings.TrimSpace(update.Long)
	update.Short = strings.TrimSpace(update.Short)

	if update.Long == "" && update.Short == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "nothing to change"})
		return
	}

	if update.Short != "" && !shortcode.Valid(update.Short) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "short codes can only use letters, numbers, - and _, and can't be a word this site already uses"})
		return
	}

	link, err := data.UpdateLink(c.Param("short"), update.Short, update.Long, owner)
	if err != nil {
		respond(c, err, admin)
		return
	}

	c.JSON(http.StatusOK, link)
}

func DeleteLink(c *gin.Context) {
	_, owner, admin := scope(c)

	if err := data.DeleteLink(c.Param("short"), owner); err != nil {
		respond(c, err, admin)
		return
	}

	c.Status(http.StatusNoContent)
}

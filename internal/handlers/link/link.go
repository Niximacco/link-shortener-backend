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
	"github.com/anthonynixon/link-shortener-backend/internal/tags"
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

// linkUpdate is the body of a PATCH. Every field is optional; whichever is
// present gets changed.
//
// Tags is a pointer so that sending "tags": "" means "take every tag off this
// link" while leaving the field out means "don't touch the tags".
type linkUpdate struct {
	Long  string  `json:"long"`
	Short string  `json:"short"`
	Tags  *string `json:"tags"`
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
	admin = auth.IsAdmin(c)

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

	// Filtering is applied after the list comes back, for the same reason it is
	// sorted there: a datastore filter on tags next to the CreatedBy filter
	// would need a composite index.
	tag := strings.TrimSpace(c.Query("tag"))
	links = data.FilterByTag(links, tag)

	c.JSON(http.StatusOK, gin.H{"links": links, "admin": admin, "tag": tag})
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
		// A code nobody can be sent anywhere for is not worth an error page. The
		// visitor followed a link and wants to land somewhere, so they land on the
		// front of the site.
		if !errors.Is(err, data.NotFoundErr) {
			log.Printf("could not look up %s: %s", short, err.Error())
		}
		c.Redirect(http.StatusFound, "/")
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

	cleanTags, err := tags.Clean(newLink.Tags)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	newLink.Tags = cleanTags

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

	// The tags the link was labelled with are made real afterwards, so a tag can
	// be typed straight onto a link without visiting the tags page first. It
	// runs after the link is stored because a tag that failed to be created is
	// worth a log line, not a lost link - the label is on the link either way,
	// and the tags page will show it once the entity catches up.
	ensureTags(newLink.CreatedBy, newLink.Tags)

	newLink.Short = strings.ToUpper(newLink.Short)
	c.JSON(http.StatusCreated, newLink)
}

// ensureTags creates whichever of a link's tags its owner doesn't have yet.
// Failing to is not worth failing the request over, so it is logged instead.
func ensureTags(owner string, list string) {
	if owner == "" || list == "" {
		return
	}

	if err := data.EnsureTags(owner, tags.Parse(list)); err != nil {
		log.Printf("could not create tags for %s: %s", owner, err.Error())
	}
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

	if update.Long == "" && update.Short == "" && update.Tags == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "nothing to change"})
		return
	}

	if update.Short != "" && !shortcode.Valid(update.Short) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "short codes can only use letters, numbers, - and _, and can't be a word this site already uses"})
		return
	}

	if update.Tags != nil {
		cleanTags, err := tags.Clean(*update.Tags)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		update.Tags = &cleanTags
	}

	link, err := data.UpdateLink(c.Param("short"), update.Short, update.Long, update.Tags, owner)
	if err != nil {
		respond(c, err, admin)
		return
	}

	// New tags belong to whoever owns the link, not to the admin who may have
	// been the one to type them.
	if update.Tags != nil {
		ensureTags(link.CreatedBy, link.Tags)
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

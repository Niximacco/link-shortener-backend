package tag_handler

import (
	"errors"
	"log"
	"net/http"

	"github.com/anthonynixon/link-shortener-backend/internal/auth"
	data "github.com/anthonynixon/link-shortener-backend/internal/cloud"
	"github.com/anthonynixon/link-shortener-backend/internal/tags"
	"github.com/anthonynixon/link-shortener-backend/internal/types"
	"github.com/anthonynixon/link-shortener-backend/internal/web"
	"github.com/gin-gonic/gin"
)

func AddTagV1(router *gin.Engine) {
	router.GET("/tags", auth.RequiredPage(), ShowTags)
	router.GET("/api/tags", auth.Required(), ListTags)
	router.POST("/tags", auth.Required(), CreateTag)
	router.PATCH("/tags/:name", auth.Required(), UpdateTag)
	router.DELETE("/tags/:name", auth.Required(), DeleteTag)
}

// owner is whose tags a request is about. Tags are owned outright: unlike
// links, an admin does not get to reach into somebody else's set, because a tag
// is a private filing system rather than a piece of the service's state. An
// admin editing another person's link can still label it - the label lands in
// that person's tags, not the admin's.
func owner(c *gin.Context) string {
	return data.NormalizeEmail(auth.Email(c))
}

// respond turns a data-layer error into the right status.
func respond(c *gin.Context, err error) {
	switch {
	case errors.Is(err, data.TagNotFoundErr):
		c.JSON(http.StatusNotFound, gin.H{"error": "you don't have a tag by that name"})
	case errors.Is(err, data.AlreadyExistsErr), errors.Is(err, data.TagInUseErr):
		c.JSON(http.StatusConflict, gin.H{"error": "you already have a tag by that name"})
	default:
		log.Printf("tag operation failed: %s", err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	}
}

// ShowTags renders the page for managing your own tags.
func ShowTags(c *gin.Context) {
	page := web.New("Tags")
	page.Email = auth.Email(c)
	page.IsAdmin = auth.IsAdmin(c)
	page.Limit = data.TAG_LIST_LIMIT
	page.Palette = tags.Palette

	list, err := data.ListTags(owner(c), data.TAG_LIST_LIMIT)
	if err != nil {
		// Making a tag still works even when the list won't load.
		log.Printf("could not list tags: %s", err.Error())
		page.Error = "Your tags couldn't be loaded."
	}

	page.Tags = list

	// Counting is done here rather than in datastore: the links are already
	// loaded for the dashboard's own list, and a per-tag count query would be
	// one round trip per tag.
	links, err := data.ListLinks(owner(c), data.LINK_LIST_LIMIT)
	if err != nil {
		log.Printf("could not count links per tag: %s", err.Error())
	} else {
		page.TagCounts = countByTag(links, list)
	}

	web.Render(c, http.StatusOK, web.TagsPage, page)
}

// countByTag works out how many links carry each tag, keyed the way tags are
// compared so the lookup from a tag's name lands.
func countByTag(links []types.Link, list []types.Tag) map[string]int {
	counts := map[string]int{}
	for _, tag := range list {
		counts[tags.Key(tag.Name)] = 0
	}

	for _, link := range links {
		for _, name := range link.TagNames() {
			counts[tags.Key(name)]++
		}
	}

	return counts
}

// ListTags is the json view of the same list, for the dashboard's tag picker.
func ListTags(c *gin.Context) {
	list, err := data.ListTags(owner(c), data.TAG_LIST_LIMIT)
	if err != nil {
		respond(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"tags": list})
}

// newTag is the body of a create request.
type newTag struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

// CreateTag adds a tag to the caller's own set.
func CreateTag(c *gin.Context) {
	var request newTag
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	name := tags.Normalize(request.Name)
	if !tags.Valid(name) {
		c.JSON(http.StatusBadRequest, gin.H{"error": badNameMessage})
		return
	}

	color := request.Color
	if color == "" {
		// Nobody picked, so pick something they aren't already using.
		existing, err := data.ListTags(owner(c), data.TAG_LIST_LIMIT)
		if err != nil {
			respond(c, err)
			return
		}

		taken := make([]string, 0, len(existing))
		for _, tag := range existing {
			taken = append(taken, tag.Color)
		}

		color = tags.PickColor(taken)
	} else if !tags.ValidColor(color) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a colour has to be a hex value like #3b7dd8"})
		return
	}

	tag, err := data.NewTag(owner(c), name, color)
	if err != nil {
		respond(c, err)
		return
	}

	c.JSON(http.StatusCreated, tag)
}

// tagUpdate is the body of a tag PATCH. Both fields are optional; whichever is
// present gets changed.
type tagUpdate struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

const badNameMessage = "a tag name has to be 1 to 32 characters, with no commas or slashes"

// UpdateTag renames a tag, recolours it, or both. Renaming rewrites the name on
// every link of yours that carried it, so a tag never points at nothing.
func UpdateTag(c *gin.Context) {
	name := c.Param("name")

	var request tagUpdate
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	request.Name = tags.Normalize(request.Name)

	if request.Name == "" && request.Color == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "nothing to change"})
		return
	}

	if request.Name != "" && !tags.Valid(request.Name) {
		c.JSON(http.StatusBadRequest, gin.H{"error": badNameMessage})
		return
	}

	if request.Color != "" && !tags.ValidColor(request.Color) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a colour has to be a hex value like #3b7dd8"})
		return
	}

	tag, err := data.UpdateTag(owner(c), name, request.Name, request.Color)
	if err != nil {
		respond(c, err)
		return
	}

	c.JSON(http.StatusOK, tag)
}

// DeleteTag removes a tag and takes it off every link of yours that carried it.
// The links themselves are left alone otherwise.
func DeleteTag(c *gin.Context) {
	if err := data.DeleteTag(owner(c), c.Param("name")); err != nil {
		respond(c, err)
		return
	}

	c.Status(http.StatusNoContent)
}

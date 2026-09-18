package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// The routes are registered on one gin tree in the order main registers them,
// with a catch-all "/:short" already in place. Gin builds that tree at
// registration time and panics on a conflict, which would be a crash at
// startup rather than a failing request, so it is worth pinning down here.
func TestTheWholeRouteSetRegistersAndResolves(t *testing.T) {
	gin.SetMode(gin.TestMode)

	hit := ""
	mark := func(name string) gin.HandlerFunc {
		return func(c *gin.Context) { hit = name; c.Status(http.StatusOK) }
	}

	r := New()

	// link.AddLinkV1
	r.GET("/:short", mark("redirect"))
	r.GET("/links", mark("links"))
	r.GET("/link/:short", mark("link"))
	r.POST("/link", mark("create"))
	r.PATCH("/link/:short", mark("update"))
	r.DELETE("/link/:short", mark("delete"))

	// auth_handler.AddAuthV1
	r.GET("/", mark("dashboard"))
	r.GET("/login", mark("login"))
	r.POST("/login", mark("request"))
	r.GET("/auth/callback", mark("complete"))
	r.POST("/login/code", mark("code"))
	r.POST("/logout", mark("logout"))
	r.GET("/api/auth/session", mark("session"))

	// user_handler.AddUserV1
	r.GET("/users", mark("users"))
	r.POST("/users", mark("adduser"))
	r.PATCH("/users/:email", mark("updateuser"))

	// tag_handler.AddTagV1
	r.GET("/tags", mark("tags"))
	r.GET("/api/tags", mark("apitags"))
	r.POST("/tags", mark("addtag"))
	r.PATCH("/tags/:name", mark("updatetag"))
	r.DELETE("/tags/:name", mark("deletetag"))

	cases := []struct {
		method string
		path   string
		want   string
	}{
		// The tag routes have to win over the redirect catch-all, the way the
		// user and login routes already do.
		{http.MethodGet, "/tags", "tags"},
		{http.MethodGet, "/api/tags", "apitags"},
		{http.MethodPost, "/tags", "addtag"},
		{http.MethodPatch, "/tags/work", "updatetag"},
		{http.MethodDelete, "/tags/work", "deletetag"},
		// And a short code that looks like one must still redirect.
		{http.MethodGet, "/TAGS", "redirect"},
		{http.MethodGet, "/ABC123", "redirect"},
		{http.MethodGet, "/users", "users"},
		{http.MethodGet, "/api/auth/session", "session"},
		{http.MethodGet, "/", "dashboard"},
	}

	for _, test := range cases {
		hit = ""
		recorder := httptest.NewRecorder()
		r.ServeHTTP(recorder, httptest.NewRequest(test.method, test.path, nil))

		if hit != test.want {
			t.Errorf("%s %s reached %q, want %q (status %d)", test.method, test.path, hit, test.want, recorder.Code)
		}
	}
}

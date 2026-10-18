package main

import (
	"fmt"
	data "github.com/anthonynixon/link-shortener-backend/internal/cloud"
	auth_handler "github.com/anthonynixon/link-shortener-backend/internal/handlers/auth"
	"github.com/anthonynixon/link-shortener-backend/internal/handlers/link"
	user_handler "github.com/anthonynixon/link-shortener-backend/internal/handlers/user"
	"github.com/anthonynixon/link-shortener-backend/internal/handlers/tag"
	"github.com/anthonynixon/link-shortener-backend/internal/router"
	"github.com/gin-gonic/gin"
	"log"
	"net/http"
	"os"
)

var PORT = ""

func init() {
	PORT = os.Getenv("PORT")
	if PORT == "" {
		PORT = "8080"
	}

	data.Initialize()
}

func main() {
	if os.Getenv("mode") != "debug" {
		gin.SetMode(gin.ReleaseMode)
	}
	router := router.New()

	// The host checks this to tell whether the service is up, and a deploy
	// waits for it before it takes the previous build away. It takes no
	// session: the dashboard probing it is not signed in to anything.
	router.GET("/healthz", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	// Add Routes
	link.AddLinkV1(router)
	auth_handler.AddAuthV1(router)
	user_handler.AddUserV1(router)
	tag_handler.AddTagV1(router)

	log.Printf("Running link-shortener-backend on :%s...", PORT)
	err := router.Run(fmt.Sprintf(":%s", PORT))
	if err != nil {
		log.Fatal(err.Error())
	}
}

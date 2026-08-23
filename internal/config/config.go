package config

import (
	"log"
	"os"
	"strings"
)

var (
	// BASE_URL is the public origin the service is reached at. It is used to
	// build magic links and to show people their finished short links, so it has
	// to match what the browser actually sees.
	BASE_URL = "https://ajn.me"
	// SITE_NAME is what the pages and login emails call this service.
	SITE_NAME = "ajn.me"
)

func init() {
	if baseURL := os.Getenv("APP_BASE_URL"); baseURL != "" {
		BASE_URL = strings.TrimRight(baseURL, "/")
	} else {
		log.Printf("APP_BASE_URL is not set, magic links will point at %s", BASE_URL)
	}

	if siteName := os.Getenv("SITE_NAME"); siteName != "" {
		SITE_NAME = siteName
	}
}

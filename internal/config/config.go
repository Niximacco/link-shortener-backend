package config

import (
	"log"
	"os"
	"strings"
)

var (
	// BASE_URL is the public origin the service is reached at. It is used to
	// show people their finished short links, and it is what this site is
	// registered at auth.ajn.me under, so it has to match what the browser
	// actually sees.
	BASE_URL = "https://ajn.me"
	// SITE_NAME is what the pages call this service.
	SITE_NAME = "ajn.me"
)

func init() {
	if baseURL := os.Getenv("APP_BASE_URL"); baseURL != "" {
		BASE_URL = strings.TrimRight(baseURL, "/")
	} else {
		log.Printf("APP_BASE_URL is not set, this service will call itself %s", BASE_URL)
	}

	if siteName := os.Getenv("SITE_NAME"); siteName != "" {
		SITE_NAME = siteName
	}
}

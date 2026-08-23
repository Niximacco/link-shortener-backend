package email

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

const resendEndpoint = "https://api.resend.com/emails"

var (
	RESEND_API_KEY string
	MAIL_FROM      string

	client = &http.Client{Timeout: 10 * time.Second}
)

// ErrNotConfigured means the service has no Resend credentials, so nothing can
// be sent. Login is unavailable until RESEND_API_KEY and MAIL_FROM are set.
var ErrNotConfigured = errors.New("email sending is not configured")

func init() {
	RESEND_API_KEY = os.Getenv("RESEND_API_KEY")
	MAIL_FROM = os.Getenv("MAIL_FROM")

	if RESEND_API_KEY == "" || MAIL_FROM == "" {
		log.Print("WARNING: RESEND_API_KEY and/or MAIL_FROM are unset, magic link login is disabled")
	}
}

// Configured reports whether email can actually be sent.
func Configured() bool {
	return RESEND_API_KEY != "" && MAIL_FROM != ""
}

type Message struct {
	To      string
	Subject string
	HTML    string
	Text    string
}

type resendRequest struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	HTML    string   `json:"html"`
	Text    string   `json:"text"`
}

type resendResponse struct {
	Id      string `json:"id"`
	Message string `json:"message"`
	Name    string `json:"name"`
}

// Send delivers a single message through Resend. It blocks until Resend has
// accepted it: on Cloud Run the container's cpu is only guaranteed while a
// request is being handled, so this must not be moved to a goroutine.
func Send(message Message) error {
	if !Configured() {
		return ErrNotConfigured
	}

	body, err := json.Marshal(resendRequest{
		From:    MAIL_FROM,
		To:      []string{message.To},
		Subject: message.Subject,
		HTML:    message.HTML,
		Text:    message.Text,
	})
	if err != nil {
		return err
	}

	request, err := http.NewRequest(http.MethodPost, resendEndpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}

	request.Header.Set("Authorization", fmt.Sprintf("Bearer %s", RESEND_API_KEY))
	request.Header.Set("Content-Type", "application/json")

	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	// Cap the read: we only ever want the error message out of this.
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil {
		return err
	}

	var parsed resendResponse
	// A body we can't parse is not fatal on its own, the status code decides.
	_ = json.Unmarshal(responseBody, &parsed)

	if response.StatusCode < 200 || response.StatusCode > 299 {
		if parsed.Message != "" {
			return fmt.Errorf("resend returned %d: %s", response.StatusCode, parsed.Message)
		}
		return fmt.Errorf("resend returned %d", response.StatusCode)
	}

	log.Printf("sent email %s", parsed.Id)
	return nil
}

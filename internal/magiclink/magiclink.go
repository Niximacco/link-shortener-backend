package magiclink

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"log"
	"net/url"
	"strings"
	"time"

	data "github.com/anthonynixon/link-shortener-backend/internal/cloud"
	"github.com/anthonynixon/link-shortener-backend/internal/config"
	"github.com/anthonynixon/link-shortener-backend/internal/email"
)

const (
	// TOKEN_VALID_TIME is how long a magic link works for.
	TOKEN_VALID_TIME = 15 * time.Minute
	// SEND_THROTTLE is the minimum gap between two links for the same address.
	SEND_THROTTLE = 60 * time.Second

	// SEND_LIMIT_HOUR and SEND_LIMIT_DAY cap how many links one address can have
	// mailed to it inside SEND_WINDOW_HOUR and SEND_WINDOW_DAY.
	//
	// SEND_THROTTLE on its own only spaces sends out, it does not bound them: a
	// link a minute forever is 1440 emails a day to a single address, which is
	// enough to burn through a Resend plan and bury whoever owns that address,
	// off nothing more than a guessed email. These are what actually bound the
	// send budget, and because they are counted from state in datastore they
	// hold across every running instance.
	SEND_LIMIT_HOUR = 5
	SEND_LIMIT_DAY  = 15

	SEND_WINDOW_HOUR = time.Hour
	SEND_WINDOW_DAY  = 24 * time.Hour

	// tokenBytes is the amount of entropy behind a link.
	tokenBytes = 32
)

var (
	// ErrNotAllowed means the address has no user entity, or the user is
	// disabled. Callers must not tell the visitor which it was.
	ErrNotAllowed = errors.New("email address is not allowed to sign in")
	// ErrThrottled means a link was already sent to this address moments ago.
	ErrThrottled = errors.New("a login link was sent recently")
	// ErrInvalidEmail means the submitted address isn't a usable address.
	ErrInvalidEmail = errors.New("that doesn't look like an email address")
	// ErrBadToken covers every reason a magic link won't sign somebody in:
	// unknown, already used, or expired.
	ErrBadToken = errors.New("this login link is no longer valid")
)

// newToken returns the token that goes in the email and the hash that is stored
// in datastore. The plaintext token is never persisted.
func newToken() (token string, tokenHash string, err error) {
	buffer := make([]byte, tokenBytes)
	if _, err = rand.Read(buffer); err != nil {
		return "", "", err
	}

	token = base64.RawURLEncoding.EncodeToString(buffer)
	return token, hashToken(token), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Request mails a single-use login link to an allow-listed address. next is an
// optional path to land on after signing in.
func Request(address string, next string) error {
	address = data.NormalizeEmail(address)
	if !ValidAddress(address) {
		return ErrInvalidEmail
	}

	user, err := data.GetUser(address)
	if err != nil {
		if errors.Is(err, data.UserNotFoundErr) || errors.Is(err, data.UserDisabledErr) {
			log.Printf("login requested for address that cannot sign in")
			return ErrNotAllowed
		}
		return err
	}

	if user.LastLinkSent > 0 && time.Since(time.Unix(user.LastLinkSent, 0)) < SEND_THROTTLE {
		return ErrThrottled
	}

	// Same error as the throttle on purpose. The caller already renders both as
	// success, so being capped stays indistinguishable from a link going out and
	// the form cannot be used to work out which addresses are real.
	if OverSendLimit(user.RecentLinkSents, time.Now()) {
		log.Printf("magic link send limit reached for an address")
		return ErrThrottled
	}

	token, tokenHash, err := newToken()
	if err != nil {
		return err
	}

	expiresAt := time.Now().Add(TOKEN_VALID_TIME)
	if err = data.NewMagicLink(tokenHash, address, expiresAt); err != nil {
		return err
	}

	loginURL := buildURL(token, next)
	if err = email.Send(email.Message{
		To:      address,
		Subject: fmt.Sprintf("Your sign in link for %s", config.SITE_NAME),
		HTML:    htmlBody(loginURL),
		Text:    textBody(loginURL),
	}); err != nil {
		return err
	}

	// Only throttle once something was actually delivered, so a Resend outage
	// doesn't lock the user out for a minute at a time.
	if err = data.MarkLinkSent(address, time.Now(), SEND_WINDOW_DAY); err != nil {
		log.Printf("could not record magic link send time: %s", err.Error())
	}

	return nil
}

// OverSendLimit reports whether an address has had its allowance of links for
// now. Both windows are counted off the same list of send times, which
// data.MarkLinkSent keeps pruned to the longer of the two.
func OverSendLimit(sends []int64, now time.Time) bool {
	withinHour, withinDay := 0, 0

	for _, send := range sends {
		age := now.Sub(time.Unix(send, 0))
		if age < 0 {
			// A send stamped in the future: clock skew, or an entity edited by
			// hand. Count it against the tighter window rather than ignore it,
			// so a bad value cannot be used to unlock more sends.
			age = 0
		}

		if age < SEND_WINDOW_HOUR {
			withinHour++
		}

		if age < SEND_WINDOW_DAY {
			withinDay++
		}
	}

	return withinHour >= SEND_LIMIT_HOUR || withinDay >= SEND_LIMIT_DAY
}

// Consume redeems a magic link and returns the email address it belongs to. The
// link is spent whether or not the caller goes on to start a session.
func Consume(token string) (address string, err error) {
	if token == "" {
		return "", ErrBadToken
	}

	address, err = data.ConsumeMagicLink(hashToken(token))
	if err != nil {
		if errors.Is(err, data.TokenNotFoundErr) || errors.Is(err, data.TokenUsedErr) || errors.Is(err, data.TokenExpiredErr) {
			log.Printf("rejected magic link: %s", err.Error())
			return "", ErrBadToken
		}
		return "", err
	}

	// The link was valid, but access may have been revoked since it was sent.
	if _, err = data.GetUser(address); err != nil {
		if errors.Is(err, data.UserNotFoundErr) || errors.Is(err, data.UserDisabledErr) {
			return "", ErrNotAllowed
		}
		return "", err
	}

	if err = data.MarkLoggedIn(address, time.Now()); err != nil {
		log.Printf("could not record login time: %s", err.Error())
	}

	return address, nil
}

// ValidAddress is a deliberately loose check. The real check is whether the
// address exists in the user kind; this only catches obvious junk before we
// bother datastore with it.
func ValidAddress(address string) bool {
	if len(address) < 3 || len(address) > 254 {
		return false
	}

	at := strings.LastIndex(address, "@")
	if at < 1 || at == len(address)-1 {
		return false
	}

	domain := address[at+1:]
	if !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return false
	}

	return !strings.ContainsAny(address, " \t\r\n<>\"")
}

func buildURL(token string, next string) string {
	query := url.Values{}
	query.Set("token", token)
	if next != "" {
		query.Set("next", next)
	}

	return fmt.Sprintf("%s/auth/callback?%s", config.BASE_URL, query.Encode())
}

func textBody(loginURL string) string {
	return fmt.Sprintf(`Sign in to %s

Open this link to sign in:

%s

The link works once and expires in %d minutes. If you didn't ask to sign in, you can ignore this email.
`, config.SITE_NAME, loginURL, int(TOKEN_VALID_TIME.Minutes()))
}

func htmlBody(loginURL string) string {
	safeURL := html.EscapeString(loginURL)
	return fmt.Sprintf(`<!doctype html>
<html>
  <body style="margin:0;padding:32px;background:#f5f5f7;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Helvetica,Arial,sans-serif;color:#1d1d1f;">
    <table role="presentation" cellpadding="0" cellspacing="0" style="max-width:480px;margin:0 auto;background:#ffffff;border-radius:12px;padding:32px;">
      <tr><td>
        <h1 style="margin:0 0 8px;font-size:20px;">Sign in to %s</h1>
        <p style="margin:0 0 24px;font-size:15px;line-height:1.5;color:#4a4a4f;">Click the button below to finish signing in.</p>
        <p style="margin:0 0 24px;">
          <a href="%s" style="display:inline-block;background:#1d1d1f;color:#ffffff;text-decoration:none;padding:12px 20px;border-radius:8px;font-size:15px;font-weight:600;">Sign in</a>
        </p>
        <p style="margin:0 0 24px;font-size:13px;line-height:1.5;color:#6e6e73;">Or paste this into your browser:<br><span style="word-break:break-all;">%s</span></p>
        <p style="margin:0;font-size:13px;line-height:1.5;color:#6e6e73;">The link works once and expires in %d minutes. If you didn't ask to sign in, you can ignore this email.</p>
      </td></tr>
    </table>
  </body>
</html>`, html.EscapeString(config.SITE_NAME), safeURL, safeURL, int(TOKEN_VALID_TIME.Minutes()))
}

package auth

import (
	"errors"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	data "github.com/anthonynixon/link-shortener-backend/internal/cloud"
	"github.com/anthonynixon/link-shortener-backend/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

var (
	JWT_SIGNING_KEY []byte
	AUDIENCE        = os.Getenv("DATASTORE_NAMESPACE")

	COOKIE_NAME   = "ls_session"
	COOKIE_DOMAIN = ""
	COOKIE_SECURE = true
)

var (
	errorNoAuthHeader     = errors.New("no authorization header content present")
	errorAuthHeaderFormat = errors.New("authorization header format incorrect, should be 'bearer <token>`")
	errorNoCredentials    = errors.New("not signed in")
	errorInvalidToken     = errors.New("invalid token")
)

const (
	// SESSION_VALID_TIME is how long a session survives without the user coming
	// back. Every visit re-issues the token, so an active user never expires.
	SESSION_VALID_TIME = 30 * 24 * time.Hour
	// REFRESH_AFTER is how old a token has to be before a visit re-issues it.
	// It keeps us from writing a Set-Cookie header on every single request.
	REFRESH_AFTER = 1 * time.Hour
	ISSUER        = "link-shortener-backend-api"

	// contextEmailKey holds the authenticated email address on the gin context.
	contextEmailKey = "auth_email"
	// contextUserKey holds the loaded user entity on the gin context.
	contextUserKey = "auth_user"
)

func init() {
	log.Print("Initializing Authentication")
	signingKey := os.Getenv("JWT_SIGNING_KEY")
	if signingKey == "" {
		log.Fatal("No Signing Key Present.")
	}

	JWT_SIGNING_KEY = []byte(signingKey)

	if name := os.Getenv("SESSION_COOKIE_NAME"); name != "" {
		COOKIE_NAME = name
	}

	// Left empty the cookie is host-only, which is what we want when the API and
	// the pages share a hostname. Set it to a parent domain (".example.com") only
	// if the session has to be readable from a sibling subdomain.
	COOKIE_DOMAIN = os.Getenv("COOKIE_DOMAIN")

	// Secure cookies are the default; plain http local development needs this off.
	if strings.EqualFold(os.Getenv("COOKIE_SECURE"), "false") {
		log.Print("WARNING: COOKIE_SECURE=false, session cookies will be sent over plain http")
		COOKIE_SECURE = false
	}

	log.Print("done")
}

type Claims struct {
	// Username holds the user's email address. The name is kept for tokens that
	// were issued before magic-link login existed.
	Username string `json:"username"`
	jwt.RegisteredClaims
}

func New(username string) (tokenString string, err error) {
	expirationTime := time.Now().Add(SESSION_VALID_TIME)
	claims := Claims{
		Username: username,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    ISSUER,
			Subject:   username,
			Audience:  []string{AUDIENCE},
			ExpiresAt: jwt.NewNumericDate(expirationTime),
			NotBefore: jwt.NewNumericDate(time.Now()),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS512, claims)

	tokenString, err = token.SignedString(JWT_SIGNING_KEY)

	return

}

func parseHeader(header string) (token string, err error) {
	if header == "" {
		return "", errorNoAuthHeader
	}

	// Tokens will be of format "bearer <token>", split on ' ' space
	content := strings.Split(header, " ")
	if len(content) != 2 {
		return "", errorAuthHeaderFormat
	}

	token = content[1]
	return
}

// parse validates a raw token string and returns its claims. Anything that
// fails validation - bad signature, wrong algorithm, expired, or issued for
// somebody else's deployment - comes back as an error.
func parse(tokenString string) (claims *Claims, err error) {
	claims = &Claims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (any, error) {
		return JWT_SIGNING_KEY, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS512.Alg()}))

	if err != nil {
		return nil, err
	}

	if !token.Valid {
		return nil, errorInvalidToken
	}

	if claims.Issuer != ISSUER {
		return nil, errorInvalidToken
	}

	if !hasAudience(claims.Audience, AUDIENCE) {
		return nil, errorInvalidToken
	}

	if claims.Username == "" {
		return nil, errorInvalidToken
	}

	return claims, nil
}

func hasAudience(audiences jwt.ClaimStrings, want string) bool {
	for _, audience := range audiences {
		if audience == want {
			return true
		}
	}

	return false
}

// ParseToken validates an "Authorization: bearer <token>" header and returns the
// email address it was issued to.
func ParseToken(header string) (username string, err error) {
	tokenString, err := parseHeader(header)
	if err != nil {
		return "", err
	}

	claims, err := parse(tokenString)
	if err != nil {
		return "", err
	}

	return claims.Username, nil
}

// SetSessionCookie writes the session token as an http-only cookie.
func SetSessionCookie(c *gin.Context, token string) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(COOKIE_NAME, token, int(SESSION_VALID_TIME.Seconds()), "/", COOKIE_DOMAIN, COOKIE_SECURE, true)
}

// ClearSessionCookie expires the session cookie in the browser.
func ClearSessionCookie(c *gin.Context) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(COOKIE_NAME, "", -1, "/", COOKIE_DOMAIN, COOKIE_SECURE, true)
}

// StartSession issues a fresh token for an email address and sets it as a cookie.
func StartSession(c *gin.Context, email string) error {
	token, err := New(email)
	if err != nil {
		return err
	}

	SetSessionCookie(c, token)
	return nil
}

// resolve pulls a session out of the request: the cookie first, then a bearer
// header so existing API clients keep working. A cookie-backed session that is
// older than REFRESH_AFTER is re-issued, which is what keeps a regular visitor
// signed in indefinitely.
func resolve(c *gin.Context) (email string, err error) {
	if cookie, cookieErr := c.Cookie(COOKIE_NAME); cookieErr == nil && cookie != "" {
		claims, parseErr := parse(cookie)
		if parseErr != nil {
			// A cookie we can't validate is worse than no cookie: drop it so the
			// browser stops sending it and the user gets a clean login.
			ClearSessionCookie(c)
			return "", parseErr
		}

		if claims.IssuedAt != nil && time.Since(claims.IssuedAt.Time) > REFRESH_AFTER {
			if refreshed, refreshErr := New(claims.Username); refreshErr == nil {
				SetSessionCookie(c, refreshed)
			}
		}

		return claims.Username, nil
	}

	if header := c.GetHeader("Authorization"); header != "" {
		return ParseToken(header)
	}

	return "", errorNoCredentials
}

// authenticate resolves the session and confirms the account behind it is still
// allowed in. Access revoked in datastore therefore takes effect on the next
// request, rather than whenever a 30 day cookie happens to expire.
//
// The user is put on the context so the rest of the request can ask about
// admin rights without paying for a second lookup.
func authenticate(c *gin.Context) (email string, err error) {
	email, err = resolve(c)
	if err != nil {
		return "", err
	}

	user, err := data.GetUser(email)
	switch {
	case err == nil:

	case errors.Is(err, data.UserNotFoundErr), errors.Is(err, data.UserDisabledErr):
		// The account was removed or disabled while this session was alive.
		ClearSessionCookie(c)
		return "", err

	default:
		// Datastore having a bad moment must not sign everybody out. Only an
		// explicit revocation closes the door; anything else fails open.
		log.Printf("could not confirm the account behind a session: %s", err.Error())
		user = types.User{Email: email}
	}

	c.Set(contextEmailKey, email)
	c.Set(contextUserKey, user)

	return email, nil
}

// Optional attaches the signed-in user to the context when there is one, and
// lets the request through either way.
func Optional() gin.HandlerFunc {
	return func(c *gin.Context) {
		_, _ = authenticate(c)
		c.Next()
	}
}

// Required rejects unauthenticated requests with a JSON 401. Use it on the API.
func Required() gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, err := authenticate(c); err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
			return
		}

		c.Next()
	}
}

// RequiredPage sends unauthenticated browsers to the login page, remembering
// where they were headed. Use it on html routes.
func RequiredPage() gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, err := authenticate(c); err != nil {
			c.Redirect(http.StatusFound, "/login?next="+url.QueryEscape(c.Request.URL.RequestURI()))
			c.Abort()
			return
		}

		c.Next()
	}
}

// Email returns the authenticated email address for the request, or "" when the
// request is anonymous.
func Email(c *gin.Context) string {
	email, ok := c.Get(contextEmailKey)
	if !ok {
		return ""
	}

	if asString, ok := email.(string); ok {
		return asString
	}

	return ""
}

// User returns the account behind the request, or a zero user when the request
// is anonymous.
func User(c *gin.Context) types.User {
	user, ok := c.Get(contextUserKey)
	if !ok {
		return types.User{}
	}

	if asUser, ok := user.(types.User); ok {
		return asUser
	}

	return types.User{}
}

// IsAdmin reports whether the request came from an admin. It reads the user
// that authenticate already loaded, so asking is free.
func IsAdmin(c *gin.Context) bool {
	return User(c).Admin
}

// IsSignedIn reports whether the request carried a valid session.
func IsSignedIn(c *gin.Context) bool {
	return Email(c) != ""
}

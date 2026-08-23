package data

import (
	datastore "cloud.google.com/go/datastore"
	"context"
	"errors"
	"fmt"
	"github.com/anthonynixon/link-shortener-backend/internal/types"
	"log"
	"os"
	"strings"
	"time"
)

var datastoreClient *datastore.Client
var ctx context.Context
var namespace string

var AlreadyExistsErr error
var NotFoundErr = errors.New("link not found in datastore")

var (
	UserNotFoundErr  = errors.New("user not found in datastore")
	UserDisabledErr  = errors.New("user is disabled")
	TokenNotFoundErr = errors.New("login token not found")
	TokenUsedErr     = errors.New("login token has already been used")
	TokenExpiredErr  = errors.New("login token has expired")
)

const (
	userKind      = "user"
	magicLinkKind = "magic_link"
)

func Initialize() {
	projID := os.Getenv("DATASTORE_PROJECT_ID")
	if projID == "" {
		log.Fatal(`You need to set the environment variable "DATASTORE_PROJECT_ID"`)
	}

	namespace = os.Getenv("DATASTORE_NAMESPACE")
	if namespace == "" {
		log.Fatal(`You need to set the environment variable "DATASTORE_NAMESPACE"`)
	}

	ctx = context.Background()
	client, err := datastore.NewClient(ctx, projID)
	if err != nil {
		log.Fatalf("Could not create datastore client: %v", err)
	}

	datastoreClient = client

	AlreadyExistsErr = errors.New("entity already exists")
}

func GetLink(short string) (link types.Link, err error) {
	query := datastore.NewQuery("link").
		FilterField("Short", "=", strings.ToUpper(short)).
		Limit(1).
		Namespace(namespace)
	var links []types.Link
	_, err = datastoreClient.GetAll(ctx, query, &links)
	if err != nil {
		return link, err
	}

	fmt.Printf("%v\n", links)
	if len(links) > 0 {
		link = links[0]
	} else {
		return link, NotFoundErr
	}

	return
}

func NewLink(newLink types.Link) (err error) {
	if !strings.HasPrefix(newLink.Long, "https://") && !strings.HasPrefix(newLink.Long, "http://") {
		newLink.Long = fmt.Sprintf("https://%s", newLink.Long)
	}
	linkKey := datastore.NameKey("link", strings.ToUpper(newLink.Short), nil)
	linkKey.Namespace = namespace
	_, err = datastoreClient.RunInTransaction(ctx, func(tx *datastore.Transaction) error {
		// We first check that there is no entity stored with the given key.
		var empty types.Link
		if err = tx.Get(linkKey, &empty); err != datastore.ErrNoSuchEntity {
			fmt.Printf("empty?: %v\n", empty)
			return AlreadyExistsErr
		}

		// If there was no matching entity, store it now.
		newLink.Short = strings.ToUpper(newLink.Short)
		_, err = tx.Put(linkKey, &newLink)
		return err
	})

	return
}

// IncrementClicks records one click on a short code.
//
// The read and the write both happen inside the transaction: the copy of the
// link the redirect handler already has was fetched outside it, and two clicks
// landing at once would both increment the same stale count and lose one.
func IncrementClicks(short string) error {
	linkKey := datastore.NameKey("link", strings.ToUpper(short), nil)
	linkKey.Namespace = namespace

	_, err := datastoreClient.RunInTransaction(ctx, func(tx *datastore.Transaction) error {
		var link types.Link
		if err := tx.Get(linkKey, &link); err != nil {
			return err
		}

		link.Clicks++

		_, err := tx.Put(linkKey, &link)
		return err
	})

	if errors.Is(err, datastore.ErrNoSuchEntity) {
		// The link resolved by query but has no entity under the short code as its
		// key. Counting the click would mean writing a second, competing entity.
		return fmt.Errorf("no link entity keyed %s to count a click against", strings.ToUpper(short))
	}

	return err
}

// NormalizeEmail is how an email address is keyed everywhere: trimmed and
// lower-cased. Anything that looks up or stores a user goes through it.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func userKey(email string) *datastore.Key {
	key := datastore.NameKey(userKind, NormalizeEmail(email), nil)
	key.Namespace = namespace
	return key
}

func magicLinkKey(tokenHash string) *datastore.Key {
	key := datastore.NameKey(magicLinkKind, tokenHash, nil)
	key.Namespace = namespace
	return key
}

// GetUser returns the allow-listed user for an email address. UserNotFoundErr
// means the address may not log in.
func GetUser(email string) (user types.User, err error) {
	err = datastoreClient.Get(ctx, userKey(email), &user)
	if errors.Is(err, datastore.ErrNoSuchEntity) {
		return user, UserNotFoundErr
	}
	// A user entity created by hand in the console may carry properties this
	// struct doesn't know about. Everything it does know about still loaded, so
	// that isn't a reason to refuse the login.
	if err != nil && !isFieldMismatch(err) {
		return user, err
	}

	if user.Disabled {
		return user, UserDisabledErr
	}

	return user, nil
}

func isFieldMismatch(err error) bool {
	var mismatch *datastore.ErrFieldMismatch
	return errors.As(err, &mismatch)
}

// setUserProperty updates a single property on a user entity in place. It reads
// into a PropertyList rather than types.User so that any other properties on
// the entity - notes, a display name, whatever got added by hand - survive the
// write untouched.
func setUserProperty(email string, name string, value int64) error {
	key := userKey(email)
	_, err := datastoreClient.RunInTransaction(ctx, func(tx *datastore.Transaction) error {
		var properties datastore.PropertyList
		if err := tx.Get(key, &properties); err != nil {
			return err
		}

		updated := false
		for i := range properties {
			if properties[i].Name == name {
				properties[i].Value = value
				updated = true
				break
			}
		}

		if !updated {
			properties = append(properties, datastore.Property{Name: name, Value: value})
		}

		_, err := tx.Put(key, &properties)
		return err
	})

	return err
}

// MarkLinkSent records the time a magic link was mailed to a user, so repeated
// requests can be throttled.
func MarkLinkSent(email string, at time.Time) error {
	return setUserProperty(email, "LastLinkSent", at.Unix())
}

// MarkLoggedIn records a successful login on the user entity.
func MarkLoggedIn(email string, at time.Time) error {
	return setUserProperty(email, "LastLogin", at.Unix())
}

// NewMagicLink stores a pending login token. tokenHash is the SHA-256 of the
// token that was mailed out; the token itself is never written to datastore.
func NewMagicLink(tokenHash string, email string, expiresAt time.Time) error {
	link := types.MagicLink{
		Email:     NormalizeEmail(email),
		Created:   time.Now().Unix(),
		ExpiresAt: expiresAt.Unix(),
		Expires:   expiresAt,
	}

	_, err := datastoreClient.Put(ctx, magicLinkKey(tokenHash), &link)
	return err
}

// ConsumeMagicLink atomically marks a login token as used and returns the email
// address it was issued to. A token can only be consumed once; a second attempt
// returns TokenUsedErr.
func ConsumeMagicLink(tokenHash string) (email string, err error) {
	key := magicLinkKey(tokenHash)
	_, err = datastoreClient.RunInTransaction(ctx, func(tx *datastore.Transaction) error {
		var link types.MagicLink
		if err := tx.Get(key, &link); err != nil {
			if errors.Is(err, datastore.ErrNoSuchEntity) {
				return TokenNotFoundErr
			}
			return err
		}

		if link.Used {
			return TokenUsedErr
		}

		if time.Now().Unix() > link.ExpiresAt {
			return TokenExpiredErr
		}

		link.Used = true
		link.UsedAt = time.Now().Unix()
		email = link.Email

		_, err := tx.Put(key, &link)
		return err
	})

	if err != nil {
		return "", err
	}

	return email, nil
}

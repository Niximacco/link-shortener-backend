package data

import (
	datastore "cloud.google.com/go/datastore"
	"context"
	"errors"
	"fmt"
	"github.com/anthonynixon/link-shortener-backend/internal/types"
	"log"
	"os"
	"sort"
	"strings"
	"time"
)

var datastoreClient *datastore.Client
var ctx context.Context
var namespace string

var AlreadyExistsErr error
var NotFoundErr = errors.New("link not found in datastore")
var NotOwnedErr = errors.New("that link belongs to somebody else")

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
	newLink.Long = normalizeLong(newLink.Long)
	key := linkKey(newLink.Short)
	_, err = datastoreClient.RunInTransaction(ctx, func(tx *datastore.Transaction) error {
		// We first check that there is no entity stored with the given key.
		var empty types.Link
		if err = tx.Get(key, &empty); err != datastore.ErrNoSuchEntity {
			return AlreadyExistsErr
		}

		// If there was no matching entity, store it now.
		newLink.Short = strings.ToUpper(newLink.Short)
		_, err = tx.Put(key, &newLink)
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
	key := linkKey(short)

	_, err := datastoreClient.RunInTransaction(ctx, func(tx *datastore.Transaction) error {
		var link types.Link
		if err := tx.Get(key, &link); err != nil {
			return err
		}

		link.Clicks++

		_, err := tx.Put(key, &link)
		return err
	})

	if errors.Is(err, datastore.ErrNoSuchEntity) {
		// The link resolved by query but has no entity under the short code as its
		// key. Counting the click would mean writing a second, competing entity.
		return fmt.Errorf("no link entity keyed %s to count a click against", strings.ToUpper(short))
	}

	return err
}

// LINK_LIST_LIMIT caps how many links a dashboard page will load. The query
// carries no sort order on purpose: sorting in datastore alongside the
// CreatedBy filter would need a composite index, and at this size sorting the
// results here is free. Past a few thousand links, add the index instead.
const LINK_LIST_LIMIT = 500

// normalizeLong makes sure a destination is something a browser can be sent to.
func normalizeLong(long string) string {
	long = strings.TrimSpace(long)
	if long != "" && !strings.HasPrefix(long, "https://") && !strings.HasPrefix(long, "http://") {
		long = fmt.Sprintf("https://%s", long)
	}

	return long
}

func linkKey(short string) *datastore.Key {
	key := datastore.NameKey("link", strings.ToUpper(short), nil)
	key.Namespace = namespace
	return key
}

// ListLinks returns links newest first. An empty createdBy returns everybody's,
// which is what an admin viewing the whole service gets.
func ListLinks(createdBy string, limit int) (links []types.Link, err error) {
	query := datastore.NewQuery("link").Namespace(namespace)
	if createdBy != "" {
		query = query.FilterField("CreatedBy", "=", createdBy)
	}
	if limit > 0 {
		query = query.Limit(limit)
	}

	_, err = datastoreClient.GetAll(ctx, query, &links)
	if err = tolerateFieldMismatch(err); err != nil {
		return nil, err
	}

	if links == nil {
		links = []types.Link{}
	}

	sort.Slice(links, func(i, j int) bool {
		if links[i].Created != links[j].Created {
			return links[i].Created > links[j].Created
		}
		return links[i].Short < links[j].Short
	})

	return links, nil
}

// UpdateLink changes a link's destination, its short code, or both, and returns
// what the link looks like afterwards.
//
// requireOwner is the ownership check: pass the caller's email to limit them to
// their own links, or "" for an admin who may change anything. It is enforced
// inside the transaction, so ownership can't change between the check and the
// write.
//
// Renaming means moving the entity, since the short code is its key. Clicks and
// the original creation details come along, and the old code stops resolving.
func UpdateLink(short string, newShort string, newLong string, requireOwner string) (link types.Link, err error) {
	oldKey := linkKey(short)
	renaming := newShort != "" && !strings.EqualFold(newShort, short)

	_, err = datastoreClient.RunInTransaction(ctx, func(tx *datastore.Transaction) error {
		var stored types.Link
		if err := tx.Get(oldKey, &stored); err != nil {
			if errors.Is(err, datastore.ErrNoSuchEntity) {
				return NotFoundErr
			}
			if !isFieldMismatch(err) {
				return err
			}
		}

		if requireOwner != "" && stored.CreatedBy != requireOwner {
			return NotOwnedErr
		}

		if newLong != "" {
			stored.Long = normalizeLong(newLong)
		}

		writeKey := oldKey
		if renaming {
			writeKey = linkKey(newShort)

			var occupant types.Link
			if err := tx.Get(writeKey, &occupant); err != datastore.ErrNoSuchEntity {
				if err == nil || isFieldMismatch(err) {
					return AlreadyExistsErr
				}
				return err
			}

			stored.Short = strings.ToUpper(newShort)
		}

		if _, err := tx.Put(writeKey, &stored); err != nil {
			return err
		}

		if renaming {
			if err := tx.Delete(oldKey); err != nil {
				return err
			}
		}

		link = stored
		return nil
	})

	if err != nil {
		return types.Link{}, err
	}

	return link, nil
}

// DeleteLink removes a link. requireOwner works the same as in UpdateLink.
func DeleteLink(short string, requireOwner string) error {
	key := linkKey(short)

	_, err := datastoreClient.RunInTransaction(ctx, func(tx *datastore.Transaction) error {
		var stored types.Link
		if err := tx.Get(key, &stored); err != nil {
			if errors.Is(err, datastore.ErrNoSuchEntity) {
				return NotFoundErr
			}
			if !isFieldMismatch(err) {
				return err
			}
		}

		if requireOwner != "" && stored.CreatedBy != requireOwner {
			return NotOwnedErr
		}

		return tx.Delete(key)
	})

	return err
}

// tolerateFieldMismatch drops errors that only say an entity carried a property
// this struct doesn't have. Everything the struct does know about still loaded.
func tolerateFieldMismatch(err error) error {
	if err == nil || isFieldMismatch(err) {
		return nil
	}

	// GetAll reports per-entity problems as a MultiError, which doesn't unwrap.
	var multi datastore.MultiError
	if errors.As(err, &multi) {
		for _, single := range multi {
			if single != nil && !isFieldMismatch(single) {
				return err
			}
		}
		return nil
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

// mutateUser rewrites properties on a user entity inside a transaction. change
// is handed the entity as it currently stands and returns the properties to
// write, so an update can be built from what is already there.
//
// It works on a PropertyList rather than types.User so that any other
// properties on the entity - notes, a display name, whatever got added by hand
// - survive the write untouched.
func mutateUser(email string, change func(current datastore.PropertyList) ([]datastore.Property, error)) error {
	key := userKey(email)
	_, err := datastoreClient.RunInTransaction(ctx, func(tx *datastore.Transaction) error {
		var properties datastore.PropertyList
		if err := tx.Get(key, &properties); err != nil {
			if errors.Is(err, datastore.ErrNoSuchEntity) {
				return UserNotFoundErr
			}
			return err
		}

		writes, err := change(properties)
		if err != nil {
			return err
		}

		// A write replaces the whole property, indexing included, so what ends
		// up on the entity is what the caller asked for rather than a mix of
		// that and however the property happened to be stored before.
		for _, write := range writes {
			updated := false
			for i := range properties {
				if properties[i].Name == write.Name {
					properties[i] = write
					updated = true
					break
				}
			}

			if !updated {
				properties = append(properties, write)
			}
		}

		_, err = tx.Put(key, &properties)
		return err
	})

	return err
}

// setUserProperties updates properties on a user entity in place, leaving every
// other property alone.
func setUserProperties(email string, values map[string]interface{}) error {
	return mutateUser(email, func(datastore.PropertyList) ([]datastore.Property, error) {
		writes := make([]datastore.Property, 0, len(values))
		for name, value := range values {
			writes = append(writes, datastore.Property{Name: name, Value: value})
		}

		return writes, nil
	})
}

// UpdateUser changes a user's role or whether they're blocked. A nil pointer
// means "leave this alone", so a caller can flip one without touching the other.
func UpdateUser(email string, admin *bool, disabled *bool) (user types.User, err error) {
	email = NormalizeEmail(email)

	values := map[string]interface{}{}
	if admin != nil {
		values["Admin"] = *admin
	}
	if disabled != nil {
		values["Disabled"] = *disabled
	}

	if len(values) == 0 {
		return types.User{}, nil
	}

	if err = setUserProperties(email, values); err != nil {
		return types.User{}, err
	}

	// Read it back so the caller gets what is actually stored. GetUser refuses
	// to return a disabled user, so build that case from what we just wrote.
	user, err = GetUser(email)
	if errors.Is(err, UserDisabledErr) {
		user.Email = email
		user.Disabled = true
		return user, nil
	}

	return user, err
}

// USER_LIST_LIMIT caps the access page. Same reasoning as LINK_LIST_LIMIT: the
// query carries no sort order, so no composite index is needed.
const USER_LIST_LIMIT = 500

// ListUsers returns everyone who is allowed to sign in, by address.
//
// The Email property is filled in from the key for any entity that doesn't
// carry one - a user created by hand in the console only needs a key name, so
// the property is often missing, and the key is the address of record either
// way.
func ListUsers(limit int) (users []types.User, err error) {
	query := datastore.NewQuery(userKind).Namespace(namespace)
	if limit > 0 {
		query = query.Limit(limit)
	}

	keys, err := datastoreClient.GetAll(ctx, query, &users)
	if err = tolerateFieldMismatch(err); err != nil {
		return nil, err
	}

	if users == nil {
		users = []types.User{}
	}

	for i := range users {
		if users[i].Email == "" && i < len(keys) {
			users[i].Email = keys[i].Name
		}
	}

	sort.Slice(users, func(i, j int) bool {
		return users[i].Email < users[j].Email
	})

	return users, nil
}

// NewUser adds an address to the allow list. It fails with AlreadyExistsErr
// rather than overwriting, so re-adding somebody can never quietly reset their
// admin flag or their login history.
func NewUser(email string, admin bool) (user types.User, err error) {
	email = NormalizeEmail(email)
	key := userKey(email)

	user = types.User{
		Email:   email,
		Created: time.Now().Unix(),
		Admin:   admin,
	}

	_, err = datastoreClient.RunInTransaction(ctx, func(tx *datastore.Transaction) error {
		var existing types.User
		if err := tx.Get(key, &existing); err != datastore.ErrNoSuchEntity {
			if err == nil || isFieldMismatch(err) {
				return AlreadyExistsErr
			}
			return err
		}

		_, err := tx.Put(key, &user)
		return err
	})

	if err != nil {
		return types.User{}, err
	}

	return user, nil
}

// MAX_RECENT_LINK_SENTS is a ceiling on how many send times are kept on one
// user, whatever retain works out to. Pruning by age already bounds the list to
// however many sends the caller's own caps allow in that time; this only stops
// an entity that somehow got past those from growing without limit.
const MAX_RECENT_LINK_SENTS = 64

// MarkLinkSent records the time a magic link was mailed to a user. It moves the
// throttle forward and appends to the list of recent sends that the caller's
// rate caps are counted from, dropping anything older than retain on the way
// past so the list stays a bounded handful of numbers however long an account
// lives.
func MarkLinkSent(email string, at time.Time, retain time.Duration) error {
	return mutateUser(email, func(current datastore.PropertyList) ([]datastore.Property, error) {
		sends := append(recentLinkSents(current, at.Add(-retain)), at.Unix())
		if len(sends) > MAX_RECENT_LINK_SENTS {
			sends = sends[len(sends)-MAX_RECENT_LINK_SENTS:]
		}

		// A repeated property is written as a slice of interface{}, one entry
		// per value.
		values := make([]interface{}, 0, len(sends))
		for _, send := range sends {
			values = append(values, send)
		}

		return []datastore.Property{
			{Name: "LastLinkSent", Value: at.Unix()},
			{Name: "RecentLinkSents", Value: values, NoIndex: true},
		}, nil
	})
}

// recentLinkSents reads the send times off a user entity, keeping only those at
// or after cutoff. A repeated property comes back as a slice of interface{},
// but an entity carrying exactly one value can present it bare, so both shapes
// are read.
func recentLinkSents(properties datastore.PropertyList, cutoff time.Time) []int64 {
	var sends []int64

	for _, property := range properties {
		if property.Name != "RecentLinkSents" {
			continue
		}

		switch value := property.Value.(type) {
		case []interface{}:
			for _, entry := range value {
				if at, ok := entry.(int64); ok {
					sends = append(sends, at)
				}
			}
		case int64:
			sends = append(sends, value)
		}

		break
	}

	kept := sends[:0]
	for _, at := range sends {
		if !time.Unix(at, 0).Before(cutoff) {
			kept = append(kept, at)
		}
	}

	return kept
}

// MarkLoggedIn records a successful login on the user entity.
func MarkLoggedIn(email string, at time.Time) error {
	return setUserProperties(email, map[string]interface{}{"LastLogin": at.Unix()})
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

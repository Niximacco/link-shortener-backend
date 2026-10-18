// Package data is this service's database: who may sign in, every link, and
// the tags their owners label them with.
//
// It is a SQLite file of the service's own, on the host. The deploy makes it
// and its tables - declared in pillar.json - before the container starts, and
// says where it is in DATABASE_PATH. Nothing here creates a table; a file
// without the tables is a deploy that did not make them, and Open says so.
//
// This replaced Datastore. The function signatures are the ones the handlers
// already called, so the move was this package and nothing else, and the
// shapes still show where they came from: a link's primary key is its short
// code upper-cased, and a tag's is the "<owner>:<lower-cased name>" its
// Datastore key was.
package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/anthonynixon/link-shortener-backend/internal/tags"
	"github.com/anthonynixon/link-shortener-backend/internal/types"

	// SQLite in pure Go, so the container's CGO_ENABLED=0 build keeps working.
	_ "modernc.org/sqlite"
)

var db *sql.DB
var ctx = context.Background()

var (
	AlreadyExistsErr = errors.New("entity already exists")
	NotFoundErr      = errors.New("link not found")
	NotOwnedErr      = errors.New("that link belongs to somebody else")
)

var (
	UserNotFoundErr = errors.New("user not found")
	UserDisabledErr = errors.New("user is disabled")
)

// tables are the tables this package reads and writes. Open refuses a file
// that is missing one.
var tableNames = []string{"links", "tags", "users"}

func Initialize() {
	path := os.Getenv("DATABASE_PATH")
	if path == "" {
		log.Fatal(`You need to set the environment variable "DATABASE_PATH"`)
	}

	if err := Open(path); err != nil {
		log.Fatalf("Could not open the database at %s: %v", path, err)
	}
}

// Open opens the database at path and checks that its tables are there. That
// check is better failed here, where it stops the container coming up and the
// deploy puts the previous build back, than on every request afterwards.
//
// busy_timeout has a writer wait for another rather than fail. _txlock=
// immediate takes the write lock when a transaction begins instead of at its
// first write, so the read-then-write transactions below - renaming a link,
// changing a user - queue behind each other rather than deadlocking when two
// arrive together.
func Open(path string) error {
	opened, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_txlock=immediate")
	if err != nil {
		return err
	}

	for _, table := range tableNames {
		if _, err := opened.ExecContext(ctx, `SELECT 1 FROM `+table+` LIMIT 1`); err != nil {
			opened.Close()
			return err
		}
	}

	if db != nil {
		db.Close()
	}
	db = opened

	return nil
}

// Healthy reports whether the database is still there and readable. It is what
// /healthz answers on, beyond the process being up.
//
// Open makes the same check once, at startup, and the container does not come up
// if it fails - so this is not about a deploy that never had a database. It is
// about one that stops having a working one while it runs: the file removed
// under it, the mount gone read-only, the disk full. Without it the container
// would keep reporting healthy and every page would fail.
//
// One table is enough. They are in one file, made by the same deploy, so a
// failure that leaves one of them readable and not the others is not a thing
// that happens, and this is on the path of every health check.
func Healthy() error {
	if db == nil {
		return errors.New("the database is not open")
	}

	if err := db.PingContext(ctx); err != nil {
		return err
	}

	_, err := db.ExecContext(ctx, `SELECT 1 FROM links LIMIT 1`)
	return err
}

// scanner is whatever a row can be read out of, so one scan function serves
// both a single row and a row of a list.
type scanner interface{ Scan(...any) error }

// inTransaction runs f in a transaction, committed if f returns nil and rolled
// back if not.
func inTransaction(f func(tx *sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	if err := f(tx); err != nil {
		tx.Rollback()
		return err
	}

	return tx.Commit()
}

// changed is whether a statement changed a row, for telling an insert that
// found the key taken from one that worked.
func changed(result sql.Result, err error) (bool, error) {
	if err != nil {
		return false, err
	}

	n, err := result.RowsAffected()
	return n > 0, err
}

// insertNew is the statement that writes a row only if its key is free.
func insertNew(table string, columns []string, key string) string {
	return `INSERT INTO ` + table + ` (` + strings.Join(columns, ", ") + `) VALUES (` + placeholders(len(columns)) +
		`) ON CONFLICT (` + key + `) DO NOTHING`
}

// update is the statement that writes every column of one row, found by its
// key. The key is written too, which is how a link or a tag is renamed: the
// row moves rather than being copied and deleted.
func update(table string, columns []string, key string) string {
	set := make([]string, 0, len(columns))
	for _, column := range columns {
		set = append(set, column+" = ?")
	}

	return `UPDATE ` + table + ` SET ` + strings.Join(set, ", ") + ` WHERE ` + key + ` = ?`
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

// ------------------------------------------------------------------- links ---

// LINK_LIST_LIMIT caps how many links a dashboard page will load. Past a few
// thousand links, page in SQL rather than reading the whole scope.
const LINK_LIST_LIMIT = 500

var linkColumns = []string{"short", "long", "created", "created_by", "clicks", "tags"}

var (
	selectLink = `SELECT ` + strings.Join(linkColumns, ", ") + ` FROM links`
	insertLink = insertNew("links", linkColumns, "short")
	saveLink   = update("links", linkColumns, "short")
)

func scanLink(row scanner) (link types.Link, err error) {
	err = row.Scan(&link.Short, &link.Long, &link.Created, &link.CreatedBy, &link.Clicks, &link.Tags)
	return link, err
}

func linkValues(link types.Link) []any {
	return []any{link.Short, link.Long, link.Created, link.CreatedBy, link.Clicks, link.Tags}
}

// normalizeLong makes sure a destination is something a browser can be sent to.
func normalizeLong(long string) string {
	long = strings.TrimSpace(long)
	if long != "" && !strings.HasPrefix(long, "https://") && !strings.HasPrefix(long, "http://") {
		long = fmt.Sprintf("https://%s", long)
	}

	return long
}

// GetLink resolves a short code. Codes are keyed upper-cased, so this is how
// a code is case insensitive.
func GetLink(short string) (link types.Link, err error) {
	link, err = scanLink(db.QueryRowContext(ctx, selectLink+` WHERE short = ?`, strings.ToUpper(short)))
	if errors.Is(err, sql.ErrNoRows) {
		return types.Link{}, NotFoundErr
	}

	return link, err
}

func NewLink(newLink types.Link) (err error) {
	newLink.Short = strings.ToUpper(newLink.Short)
	newLink.Long = normalizeLong(newLink.Long)
	newLink.Tags = tags.Join(tags.Parse(newLink.Tags))

	added, err := changed(db.ExecContext(ctx, insertLink, linkValues(newLink)...))
	if err != nil {
		return err
	}
	if !added {
		return AlreadyExistsErr
	}

	return nil
}

// IncrementClicks records one click on a short code.
//
// It is one statement, so the count it adds to is the count in the row rather
// than one read a moment earlier. That is what stops two clicks landing at
// once from both adding to the same stale number and losing one.
func IncrementClicks(short string) error {
	short = strings.ToUpper(short)

	counted, err := changed(db.ExecContext(ctx, `UPDATE links SET clicks = clicks + 1 WHERE short = ?`, short))
	if err != nil {
		return err
	}

	if !counted {
		return fmt.Errorf("no link keyed %s to count a click against", short)
	}

	return nil
}

// ListLinks returns links newest first. An empty createdBy returns everybody's,
// which is what an admin viewing the whole service gets.
func ListLinks(createdBy string, limit int) (links []types.Link, err error) {
	query := selectLink
	args := []any{}

	if createdBy != "" {
		query += ` WHERE created_by = ?`
		args = append(args, createdBy)
	}

	query += ` ORDER BY created DESC, short ASC`

	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	links = []types.Link{}
	for rows.Next() {
		link, err := scanLink(rows)
		if err != nil {
			return nil, err
		}
		links = append(links, link)
	}

	return links, rows.Err()
}

// UpdateLink changes a link's destination, its short code, its tags, or any
// combination, and returns what the link looks like afterwards.
//
// newTags is a pointer because clearing a link's tags and leaving them alone
// are both requests to make: nil leaves them, and a pointer to "" removes them
// all. The destination and short code have no such ambiguity - there is no such
// thing as a link with no destination - so they stay plain strings.
//
// requireOwner is the ownership check: pass the caller's email to limit them to
// their own links, or "" for an admin who may change anything. It is enforced
// inside the transaction, so ownership can't change between the check and the
// write.
//
// Renaming moves the row, since the short code is its primary key. Clicks and
// the original creation details come along, and the old code stops resolving.
func UpdateLink(short string, newShort string, newLong string, newTags *string, requireOwner string) (link types.Link, err error) {
	short = strings.ToUpper(short)
	renaming := newShort != "" && !strings.EqualFold(newShort, short)

	err = inTransaction(func(tx *sql.Tx) error {
		stored, err := scanLink(tx.QueryRowContext(ctx, selectLink+` WHERE short = ?`, short))
		if errors.Is(err, sql.ErrNoRows) {
			return NotFoundErr
		}
		if err != nil {
			return err
		}

		if requireOwner != "" && stored.CreatedBy != requireOwner {
			return NotOwnedErr
		}

		if newLong != "" {
			stored.Long = normalizeLong(newLong)
		}

		if newTags != nil {
			stored.Tags = tags.Join(tags.Parse(*newTags))
		}

		if renaming {
			taken, err := occupied(tx, `SELECT 1 FROM links WHERE short = ?`, strings.ToUpper(newShort))
			if err != nil {
				return err
			}
			if taken {
				return AlreadyExistsErr
			}

			stored.Short = strings.ToUpper(newShort)
		}

		if _, err := tx.ExecContext(ctx, saveLink, append(linkValues(stored), short)...); err != nil {
			return err
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
	short = strings.ToUpper(short)

	return inTransaction(func(tx *sql.Tx) error {
		stored, err := scanLink(tx.QueryRowContext(ctx, selectLink+` WHERE short = ?`, short))
		if errors.Is(err, sql.ErrNoRows) {
			return NotFoundErr
		}
		if err != nil {
			return err
		}

		if requireOwner != "" && stored.CreatedBy != requireOwner {
			return NotOwnedErr
		}

		_, err = tx.ExecContext(ctx, `DELETE FROM links WHERE short = ?`, short)
		return err
	})
}

// occupied reports whether a one column existence query matched, which is how
// a rename finds out that the name it wants is taken.
func occupied(tx *sql.Tx, query string, args ...any) (bool, error) {
	var one int
	err := tx.QueryRowContext(ctx, query, args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	return true, nil
}

// ------------------------------------------------------------------- users ---

// USER_LIST_LIMIT caps the access page.
const USER_LIST_LIMIT = 500

var userColumns = []string{"email", "created", "last_login", "admin", "disabled"}

var (
	selectUser = `SELECT ` + strings.Join(userColumns, ", ") + ` FROM users`
	insertUser = insertNew("users", userColumns, "email")
	saveUser   = update("users", userColumns, "email")
)

func scanUser(row scanner) (user types.User, err error) {
	err = row.Scan(&user.Email, &user.Created, &user.LastLogin, &user.Admin, &user.Disabled)
	return user, err
}

func userValues(user types.User) []any {
	return []any{user.Email, user.Created, user.LastLogin, user.Admin, user.Disabled}
}

// NormalizeEmail is how an email address is keyed everywhere: trimmed and
// lower-cased. Anything that looks up or stores a user goes through it.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// ValidAddress is a deliberately loose check. The real check is whether the
// address is in the users table; this only catches obvious junk before we
// bother the database, or auth.ajn.me, with it.
func ValidAddress(email string) bool {
	if len(email) < 3 || len(email) > 254 {
		return false
	}

	at := strings.LastIndex(email, "@")
	if at < 1 || at == len(email)-1 {
		return false
	}

	domain := email[at+1:]
	if !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return false
	}

	return !strings.ContainsAny(email, " \t\r\n<>\"")
}

// GetUser returns the allow-listed user for an email address. UserNotFoundErr
// means the address may not log in.
func GetUser(email string) (user types.User, err error) {
	user, err = scanUser(db.QueryRowContext(ctx, selectUser+` WHERE email = ?`, NormalizeEmail(email)))
	if errors.Is(err, sql.ErrNoRows) {
		return types.User{}, UserNotFoundErr
	}
	if err != nil {
		return types.User{}, err
	}

	if user.Disabled {
		return user, UserDisabledErr
	}

	return user, nil
}

// NewUser adds an address to the allow list. It fails with AlreadyExistsErr
// rather than overwriting, so re-adding somebody can never quietly reset their
// admin flag or their login history.
func NewUser(email string, admin bool) (user types.User, err error) {
	user = types.User{
		Email:   NormalizeEmail(email),
		Created: time.Now().Unix(),
		Admin:   admin,
	}

	added, err := changed(db.ExecContext(ctx, insertUser, userValues(user)...))
	if err != nil {
		return types.User{}, err
	}
	if !added {
		return types.User{}, AlreadyExistsErr
	}

	return user, nil
}

// ListUsers returns everyone who is allowed to sign in, by address.
func ListUsers(limit int) (users []types.User, err error) {
	query := selectUser + ` ORDER BY email ASC`
	args := []any{}

	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	users = []types.User{}
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}

	return users, rows.Err()
}

// mutateUser applies a change to one user inside a transaction, and hands back
// the row as it ends up.
//
// On Datastore this had to work on a raw property list, so that properties the
// struct didn't know about survived the write. A row has the columns the table
// declares and nothing else, so that is no longer a thing to be careful of.
func mutateUser(email string, change func(current *types.User)) (user types.User, err error) {
	email = NormalizeEmail(email)

	err = inTransaction(func(tx *sql.Tx) error {
		current, err := scanUser(tx.QueryRowContext(ctx, selectUser+` WHERE email = ?`, email))
		if errors.Is(err, sql.ErrNoRows) {
			return UserNotFoundErr
		}
		if err != nil {
			return err
		}

		change(&current)
		user = current

		_, err = tx.ExecContext(ctx, saveUser, append(userValues(current), email)...)
		return err
	})

	if err != nil {
		return types.User{}, err
	}

	return user, nil
}

// UpdateUser changes a user's role or whether they're blocked. A nil pointer
// means "leave this alone", so a caller can flip one without touching the other.
func UpdateUser(email string, admin *bool, disabled *bool) (user types.User, err error) {
	if admin == nil && disabled == nil {
		return types.User{}, nil
	}

	return mutateUser(email, func(current *types.User) {
		if admin != nil {
			current.Admin = *admin
		}
		if disabled != nil {
			current.Disabled = *disabled
		}
	})
}

// MarkLoggedIn records a successful login on the user's row.
func MarkLoggedIn(email string, at time.Time) error {
	_, err := mutateUser(email, func(current *types.User) {
		current.LastLogin = at.Unix()
	})

	return err
}

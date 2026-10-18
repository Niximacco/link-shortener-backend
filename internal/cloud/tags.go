package data

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/anthonynixon/link-shortener-backend/internal/tags"
	"github.com/anthonynixon/link-shortener-backend/internal/types"
)

var (
	TagNotFoundErr = errors.New("tag not found")
	// TagInUseErr is what a rename hits when the owner already has a tag under
	// the new name. Merging the two would be a guess at what was meant.
	TagInUseErr = errors.New("that tag name is already taken")
)

// TAG_LIST_LIMIT caps the tags page.
const TAG_LIST_LIMIT = 200

var tagColumns = []string{"id", "owner", "name", "color", "created"}

var (
	selectTag = `SELECT ` + strings.Join(tagColumns, ", ") + ` FROM tags`
	insertTag = insertNew("tags", tagColumns, "id")
	saveTag   = update("tags", tagColumns, "id")
)

// tagID is the primary key a tag is stored under, and was its Datastore key.
// Putting the owner in the key is what makes a tag owned: two people can each
// have a "work" tag, and neither can end up with two of them, without a
// uniqueness query on the way in.
func tagID(owner string, name string) string {
	return NormalizeEmail(owner) + ":" + tags.Key(name)
}

// scanTag reads a row. The key is not kept on the struct: it is the owner and
// the name, which are columns of their own, and deriving it on every write in
// tagValues is what keeps the three from disagreeing.
func scanTag(row scanner) (tag types.Tag, err error) {
	var id string
	err = row.Scan(&id, &tag.Owner, &tag.Name, &tag.Color, &tag.Created)
	return tag, err
}

func tagValues(tag types.Tag) []any {
	return []any{tagID(tag.Owner, tag.Name), tag.Owner, tag.Name, tag.Color, tag.Created}
}

// ListTags returns a user's tags, by name. An empty owner returns everybody's,
// which is what an admin looking at the whole service gets.
func ListTags(owner string, limit int) (list []types.Tag, err error) {
	query := selectTag
	args := []any{}

	if owner != "" {
		query += ` WHERE owner = ?`
		args = append(args, NormalizeEmail(owner))
	}

	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list = []types.Tag{}
	for rows.Next() {
		tag, err := scanTag(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, tag)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// A tag row imported from Datastore can arrive without a colour, or with
	// something that isn't one. Both are settled here rather than at each place
	// a colour gets used: the colour ends up in a style attribute on the page
	// and in the json the dashboard script builds its pills from, and one gate
	// on the way out is easier to be sure of than a check at every use.
	for i := range list {
		if !tags.ValidColor(list[i].Color) {
			list[i].Color = tags.DefaultColor
		}
	}

	// Ordered here rather than in SQL because the order is by tags.Key, which
	// is the name folded the way this service folds it, and at this size
	// sorting the page is free.
	sort.Slice(list, func(i, j int) bool {
		if left, right := tags.Key(list[i].Name), tags.Key(list[j].Name); left != right {
			return left < right
		}
		return list[i].Owner < list[j].Owner
	})

	return list, nil
}

// NewTag creates a tag for a user. It fails with AlreadyExistsErr rather than
// overwriting, so making a tag that already exists can never quietly change the
// colour of the one already in use.
func NewTag(owner string, name string, color string) (tag types.Tag, err error) {
	tag = types.Tag{
		Name:    tags.Normalize(name),
		Color:   color,
		Owner:   NormalizeEmail(owner),
		Created: time.Now().Unix(),
	}

	added, err := changed(db.ExecContext(ctx, insertTag, tagValues(tag)...))
	if err != nil {
		return types.Tag{}, err
	}
	if !added {
		return types.Tag{}, AlreadyExistsErr
	}

	return tag, nil
}

// EnsureTags creates any of names the owner doesn't have yet, so a tag typed
// straight onto a link works without a trip to the tags page first. Tags that
// already exist are left exactly as they are, colour included.
//
// Each new tag gets a colour the owner is not already using, so a set of tags
// built this way is still tellable apart at a glance.
func EnsureTags(owner string, names []string) error {
	if owner == "" || len(names) == 0 {
		return nil
	}

	existing, err := ListTags(owner, TAG_LIST_LIMIT)
	if err != nil {
		return err
	}

	have := map[string]bool{}
	taken := make([]string, 0, len(existing))
	for _, tag := range existing {
		have[tags.Key(tag.Name)] = true
		taken = append(taken, tag.Color)
	}

	for _, name := range names {
		if have[tags.Key(name)] {
			continue
		}

		color := tags.PickColor(taken)
		if _, err := NewTag(owner, name, color); err != nil && !errors.Is(err, AlreadyExistsErr) {
			return err
		}

		have[tags.Key(name)] = true
		taken = append(taken, color)
	}

	return nil
}

// UpdateTag changes a tag's name, its colour, or both, and returns what the tag
// looks like afterwards.
//
// Renaming moves the row, since the name is part of its key, and then rewrites
// the name on every link of the owner's that carried it. Those link writes are
// not in the tag's transaction: holding one across a tag and an unbounded
// number of links is not something to do to a single writer database, and a
// link pointing at a name that no longer exists is the state worth avoiding, so
// the tag moves first and the links follow.
func UpdateTag(owner string, name string, newName string, newColor string) (tag types.Tag, err error) {
	owner = NormalizeEmail(owner)
	newName = tags.Normalize(newName)
	oldID := tagID(owner, name)
	renaming := newName != "" && tags.Key(newName) != tags.Key(name)

	err = inTransaction(func(tx *sql.Tx) error {
		stored, err := scanTag(tx.QueryRowContext(ctx, selectTag+` WHERE id = ?`, oldID))
		if errors.Is(err, sql.ErrNoRows) {
			return TagNotFoundErr
		}
		if err != nil {
			return err
		}

		if stored.Owner == "" {
			stored.Owner = owner
		}

		if newColor != "" {
			stored.Color = newColor
		}

		if renaming {
			taken, err := occupied(tx, `SELECT 1 FROM tags WHERE id = ?`, tagID(owner, newName))
			if err != nil {
				return err
			}
			if taken {
				return TagInUseErr
			}

			stored.Name = newName
		} else if newName != "" {
			// The same tag under different casing. Worth keeping: the casing is
			// what gets shown, and the key does not change.
			stored.Name = newName
		}

		if _, err := tx.ExecContext(ctx, saveTag, append(tagValues(stored), oldID)...); err != nil {
			return err
		}

		tag = stored
		return nil
	})

	if err != nil {
		return types.Tag{}, err
	}

	if renaming {
		if err := retagLinks(owner, name, newName); err != nil {
			return tag, fmt.Errorf("renamed the tag but could not relabel the links: %w", err)
		}
	}

	return tag, nil
}

// DeleteTag removes a tag and takes it off the owner's links.
func DeleteTag(owner string, name string) error {
	owner = NormalizeEmail(owner)
	id := tagID(owner, name)

	err := inTransaction(func(tx *sql.Tx) error {
		if _, err := scanTag(tx.QueryRowContext(ctx, selectTag+` WHERE id = ?`, id)); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return TagNotFoundErr
			}
			return err
		}

		_, err := tx.ExecContext(ctx, `DELETE FROM tags WHERE id = ?`, id)
		return err
	})

	if err != nil {
		return err
	}

	if err := retagLinks(owner, name, ""); err != nil {
		return fmt.Errorf("deleted the tag but could not relabel the links: %w", err)
	}

	return nil
}

// retagLinks rewrites one tag name across an owner's links. An empty newName
// removes the tag instead, which is what deleting one does.
//
// Only the links actually carrying the tag are written, and each is written in
// its own transaction that re-reads the row, so a link edited at the same time
// keeps whichever change landed last rather than being reverted wholesale to
// what the list read a moment earlier.
func retagLinks(owner string, name string, newName string) error {
	if owner == "" {
		return nil
	}

	links, err := ListLinks(owner, LINK_LIST_LIMIT)
	if err != nil {
		return err
	}

	for _, link := range links {
		if !tags.Has(link.Tags, name) {
			continue
		}

		if err := relabelLink(link.Short, name, newName); err != nil {
			return err
		}
	}

	return nil
}

// relabelLink applies one tag rename to one link, inside a transaction. Only
// the tags column is written, so a destination or a click counted in between is
// not rolled back by this.
func relabelLink(short string, name string, newName string) error {
	return inTransaction(func(tx *sql.Tx) error {
		var stored string
		err := tx.QueryRowContext(ctx, `SELECT tags FROM links WHERE short = ?`, strings.ToUpper(short)).Scan(&stored)
		if errors.Is(err, sql.ErrNoRows) {
			// The link went away between the list and here. Nothing to do.
			return nil
		}
		if err != nil {
			return err
		}

		relabelled, altered := tags.Rename(stored, name, newName)
		if !altered {
			return nil
		}

		_, err = tx.ExecContext(ctx, `UPDATE links SET tags = ? WHERE short = ?`, relabelled, strings.ToUpper(short))
		return err
	})
}

// FilterByTag keeps only the links carrying a tag. Filtering happens here
// rather than in the query because a link's tags are one comma separated
// column, and the page is already bounded by LINK_LIST_LIMIT.
func FilterByTag(links []types.Link, tag string) []types.Link {
	if strings.TrimSpace(tag) == "" {
		return links
	}

	kept := make([]types.Link, 0, len(links))
	for _, link := range links {
		if tags.Has(link.Tags, tag) {
			kept = append(kept, link)
		}
	}

	return kept
}

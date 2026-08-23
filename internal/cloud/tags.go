package data

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	datastore "cloud.google.com/go/datastore"
	"github.com/anthonynixon/link-shortener-backend/internal/tags"
	"github.com/anthonynixon/link-shortener-backend/internal/types"
)

const tagKind = "tag"

var (
	TagNotFoundErr = errors.New("tag not found in datastore")
	// TagInUseErr is what a rename hits when the owner already has a tag under
	// the new name. Merging the two would be a guess at what was meant.
	TagInUseErr = errors.New("that tag name is already taken")
)

// TAG_LIST_LIMIT caps the tags page. Same reasoning as LINK_LIST_LIMIT: the
// query carries no sort order, so no composite index is needed.
const TAG_LIST_LIMIT = 200

// tagKey builds the key a tag is stored under. Putting the owner in the key is
// what makes a tag owned: two people can each have a "work" tag, and neither can
// end up with two of them, without a uniqueness query on the way in.
func tagKey(owner string, name string) *datastore.Key {
	key := datastore.NameKey(tagKind, NormalizeEmail(owner)+":"+tags.Key(name), nil)
	key.Namespace = namespace
	return key
}

// ListTags returns a user's tags, by name. An empty owner returns everybody's,
// which is what an admin looking at the whole service gets.
func ListTags(owner string, limit int) (list []types.Tag, err error) {
	query := datastore.NewQuery(tagKind).Namespace(namespace)
	if owner != "" {
		query = query.FilterField("Owner", "=", NormalizeEmail(owner))
	}
	if limit > 0 {
		query = query.Limit(limit)
	}

	_, err = datastoreClient.GetAll(ctx, query, &list)
	if err = tolerateFieldMismatch(err); err != nil {
		return nil, err
	}

	if list == nil {
		list = []types.Tag{}
	}

	// A tag entity written by hand in the console can arrive without a colour,
	// or with something that isn't one. Both are settled here rather than at
	// each place a colour gets used: the colour ends up in a style attribute on
	// the page and in the json the dashboard script builds its pills from, and
	// one gate on the way out of datastore is easier to be sure of than a check
	// at every use.
	for i := range list {
		if !tags.ValidColor(list[i].Color) {
			list[i].Color = tags.DefaultColor
		}
	}

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
	owner = NormalizeEmail(owner)
	name = tags.Normalize(name)
	key := tagKey(owner, name)

	tag = types.Tag{
		Name:    name,
		Color:   color,
		Owner:   owner,
		Created: time.Now().Unix(),
	}

	_, err = datastoreClient.RunInTransaction(ctx, func(tx *datastore.Transaction) error {
		var existing types.Tag
		if err := tx.Get(key, &existing); err != datastore.ErrNoSuchEntity {
			if err == nil || isFieldMismatch(err) {
				return AlreadyExistsErr
			}
			return err
		}

		_, err := tx.Put(key, &tag)
		return err
	})

	if err != nil {
		return types.Tag{}, err
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
// Renaming means moving the entity, since the name is part of its key, and then
// rewriting the name on every link of the owner's that carried it. Those link
// writes are not in the tag's transaction: no datastore transaction can span a
// tag and an unbounded number of links, and a link pointing at a name that no
// longer exists is the state worth avoiding, so the tag moves first and the
// links follow.
func UpdateTag(owner string, name string, newName string, newColor string) (tag types.Tag, err error) {
	owner = NormalizeEmail(owner)
	newName = tags.Normalize(newName)
	oldKey := tagKey(owner, name)
	renaming := newName != "" && tags.Key(newName) != tags.Key(name)

	_, err = datastoreClient.RunInTransaction(ctx, func(tx *datastore.Transaction) error {
		var stored types.Tag
		if err := tx.Get(oldKey, &stored); err != nil {
			if errors.Is(err, datastore.ErrNoSuchEntity) {
				return TagNotFoundErr
			}
			if !isFieldMismatch(err) {
				return err
			}
		}

		if stored.Owner == "" {
			stored.Owner = owner
		}

		if newColor != "" {
			stored.Color = newColor
		}

		writeKey := oldKey
		if renaming {
			writeKey = tagKey(owner, newName)

			var occupant types.Tag
			if err := tx.Get(writeKey, &occupant); err != datastore.ErrNoSuchEntity {
				if err == nil || isFieldMismatch(err) {
					return TagInUseErr
				}
				return err
			}

			stored.Name = newName
		} else if newName != "" {
			// The same tag under different casing. Worth keeping: the casing is
			// what gets shown, and the key does not change.
			stored.Name = newName
		}

		if _, err := tx.Put(writeKey, &stored); err != nil {
			return err
		}

		if renaming {
			if err := tx.Delete(oldKey); err != nil {
				return err
			}
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
	key := tagKey(owner, name)

	_, err := datastoreClient.RunInTransaction(ctx, func(tx *datastore.Transaction) error {
		var stored types.Tag
		if err := tx.Get(key, &stored); err != nil {
			if errors.Is(err, datastore.ErrNoSuchEntity) {
				return TagNotFoundErr
			}
			if !isFieldMismatch(err) {
				return err
			}
		}

		return tx.Delete(key)
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
// its own transaction that re-reads the entity, so a link edited at the same
// time keeps whichever change landed last rather than being reverted wholesale
// to what the list read a moment earlier.
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

// relabelLink applies one tag rename to one link, inside a transaction.
func relabelLink(short string, name string, newName string) error {
	key := linkKey(short)

	_, err := datastoreClient.RunInTransaction(ctx, func(tx *datastore.Transaction) error {
		var stored types.Link
		if err := tx.Get(key, &stored); err != nil {
			if errors.Is(err, datastore.ErrNoSuchEntity) {
				// The link went away between the list and here. Nothing to do.
				return nil
			}
			if !isFieldMismatch(err) {
				return err
			}
		}

		relabelled, changed := tags.Rename(stored.Tags, name, newName)
		if !changed {
			return nil
		}

		stored.Tags = relabelled
		_, err := tx.Put(key, &stored)
		return err
	})

	return err
}

// FilterByTag keeps only the links carrying a tag. Filtering happens here
// rather than in the query for the same reason the list is sorted here: a
// datastore filter on tags alongside the CreatedBy filter would need a
// composite index, and the page is already bounded by LINK_LIST_LIMIT.
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

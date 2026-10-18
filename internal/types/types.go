package types

import (
	"github.com/anthonynixon/link-shortener-backend/internal/tags"
)

type Link struct {
	Short     string `json:"short"`
	Long      string `json:"long"`
	Created   int64  `json:"created"`
	CreatedBy string `json:"created_by"`
	Clicks    int    `json:"clicks"`
	// Tags is the link's labels as one comma separated string, which is how the
	// column is stored: a link's whole labelling arrives with the row, and
	// there is no second table to read per link.
	Tags string `json:"tags"`
}

// TagNames is the link's tags as a list, for anything that renders or compares
// them one at a time.
func (l Link) TagNames() []string {
	return tags.Parse(l.Tags)
}

// Tag is a label in the tags table, owned by the user who made it. Its primary
// key is "<owner email>:<lower-cased name>", which is what its datastore key
// was, so two people can each have a tag called "work" and neither can have two
// of them.
//
// Links carry tags by name rather than by key. Renaming or deleting a tag is
// carried through to its owner's links at that point.
type Tag struct {
	Name    string `json:"name"`
	Color   string `json:"color"`
	Owner   string `json:"owner"`
	Created int64  `json:"created"`
}

// User is a row in the users table. Presence of a row is what grants access: if
// there is no user for an email address, auth.ajn.me is never asked for a link.
// The primary key is the lower-cased email address.
type User struct {
	Email     string `json:"email"`
	Created   int64  `json:"created"`
	LastLogin int64  `json:"last_login"`
	// Entities written before auth.ajn.me existed also carried LastLinkSent and
	// RecentLinkSents, which were the per-address magic link send caps. Those
	// caps belong to the service now and nothing here read them, so the move
	// off datastore gave them no columns: they are the `ignore` list under the
	// user kind in pillar.json, which is how the import was told to leave them
	// behind rather than refusing to run.
	// Admin lets this user see and change every link, not just their own.
	Admin bool `json:"admin"`
	// Disabled keeps the entity around for history while blocking logins.
	Disabled bool `json:"disabled"`
}


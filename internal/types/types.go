package types

import "time"

type Link struct {
	Short     string `json:"short"`
	Long      string `json:"long"`
	Created   int64  `json:"created"`
	CreatedBy string `json:"created_by"`
	Clicks    int    `json:"clicks"`
}

// User is an entity in the "user" kind. Presence of an entity is what grants
// access: if there is no user for an email address, no magic link is ever sent.
// The datastore key name is the lower-cased email address.
type User struct {
	Email     string `json:"email"`
	Created   int64  `json:"created"`
	LastLogin int64  `json:"last_login"`
	// LastLinkSent is used to throttle how often magic links can be requested.
	LastLinkSent int64 `json:"last_link_sent"`
	// RecentLinkSents holds the unix times of the magic links mailed to this
	// address inside the last day, oldest first. The hourly and daily send caps
	// are counted from it. It is left out of the indexes: nothing queries on it,
	// and a repeated indexed property costs an index row per entry.
	RecentLinkSents []int64 `json:"recent_link_sents" datastore:",noindex"`
	// Admin lets this user see and change every link, not just their own.
	Admin bool `json:"admin"`
	// Disabled keeps the entity around for history while blocking logins.
	Disabled bool `json:"disabled"`
}

// MagicLink is a single-use login token in the "magic_link" kind. The datastore
// key name is the SHA-256 hash of the token, so the plaintext token only ever
// exists in the email we send.
type MagicLink struct {
	Email     string `json:"email"`
	Created   int64  `json:"created"`
	ExpiresAt int64  `json:"expires_at"`
	Used      bool   `json:"used"`
	UsedAt    int64  `json:"used_at"`
	// Expires duplicates ExpiresAt as a timestamp so a datastore TTL policy can
	// sweep consumed and abandoned tokens. See README.
	Expires time.Time `json:"-"`
}

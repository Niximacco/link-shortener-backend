package data

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anthonynixon/link-shortener-backend/internal/types"
)

// useTestDatabase points the package at a new file in a temporary directory,
// with the tables pillar.json declares made in it the way the deploy makes
// them. A column this package reads that the file does not declare fails here
// rather than on a deploy.
//
// On Datastore none of this could be tested without the emulator, so none of it
// was.
func useTestDatabase(t *testing.T) {
	t.Helper()

	pillar, err := os.ReadFile(filepath.Join("..", "..", "pillar.json"))
	if err != nil {
		t.Fatal(err)
	}
	statements, err := Tables(pillar)
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "link-shortener-backend.sqlite")
	made, err := sql.Open("sqlite", path+"?_pragma=journal_mode(wal)")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range statements {
		if _, err := made.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	made.Close()

	if err := Open(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		db = nil
	})
}

// ValidAddress stands in front of both the users table and the call to
// auth.ajn.me, and the header injection case at the bottom of the invalid list
// is the reason it refuses control characters.
func TestValidAddress(t *testing.T) {
	valid := []string{"a@b.co", "anthony@nixon.dev", "first.last+tag@sub.example.com"}
	for _, address := range valid {
		if !ValidAddress(address) {
			t.Errorf("ValidAddress(%q) = false, want true", address)
		}
	}

	invalid := []string{
		"",
		"nobody",
		"@example.com",
		"nobody@",
		"nobody@localhost",
		"nobody@.com",
		"nobody@example.",
		"no body@example.com",
		"a@b.co\r\nbcc: someone@else.com",
		strings.Repeat("a", 250) + "@example.com",
	}
	for _, address := range invalid {
		if ValidAddress(address) {
			t.Errorf("ValidAddress(%q) = true, want false", address)
		}
	}
}

func TestADatabaseWithoutItsTablesIsNotOpened(t *testing.T) {
	if err := Open(filepath.Join(t.TempDir(), "empty.sqlite")); err == nil {
		t.Error("a file with no tables in it was opened as the database")
	}
}

func TestAShortCodeResolvesWhateverItsCasing(t *testing.T) {
	useTestDatabase(t)

	if err := NewLink(types.Link{Short: "abc123", Long: "example.com", CreatedBy: "someone@example.com"}); err != nil {
		t.Fatal(err)
	}

	for _, asked := range []string{"abc123", "ABC123", "AbC123"} {
		link, err := GetLink(asked)
		if err != nil {
			t.Fatalf("GetLink(%q): %v", asked, err)
		}
		if link.Short != "ABC123" {
			t.Errorf("GetLink(%q) came back as %q, want the code upper-cased", asked, link.Short)
		}
		// A destination with no scheme is not something a browser can be sent
		// to, so it is given one on the way in.
		if link.Long != "https://example.com" {
			t.Errorf("GetLink(%q).Long = %q", asked, link.Long)
		}
	}

	if _, err := GetLink("nothing"); !errors.Is(err, NotFoundErr) {
		t.Errorf("a code nobody claimed is %v, want NotFoundErr", err)
	}
}

func TestAShortCodeCannotBeClaimedTwice(t *testing.T) {
	useTestDatabase(t)

	if err := NewLink(types.Link{Short: "TAKEN", Long: "https://example.com"}); err != nil {
		t.Fatal(err)
	}

	// The same code in the other casing is the same code.
	if err := NewLink(types.Link{Short: "taken", Long: "https://elsewhere.com"}); !errors.Is(err, AlreadyExistsErr) {
		t.Fatalf("claiming a taken code is %v, want AlreadyExistsErr", err)
	}

	link, err := GetLink("TAKEN")
	if err != nil {
		t.Fatal(err)
	}
	if link.Long != "https://example.com" {
		t.Errorf("the first link's destination was overwritten: %q", link.Long)
	}
}

func TestClicksAreCounted(t *testing.T) {
	useTestDatabase(t)

	if err := NewLink(types.Link{Short: "CLICKS", Long: "https://example.com"}); err != nil {
		t.Fatal(err)
	}

	for range 3 {
		if err := IncrementClicks("clicks"); err != nil {
			t.Fatal(err)
		}
	}

	link, err := GetLink("CLICKS")
	if err != nil {
		t.Fatal(err)
	}
	if link.Clicks != 3 {
		t.Errorf("three clicks counted as %d", link.Clicks)
	}

	if err := IncrementClicks("GONE"); err == nil {
		t.Error("counting a click against a code with no link did not say so")
	}
}

func TestListLinksIsNewestFirstAndScopedToTheOwner(t *testing.T) {
	useTestDatabase(t)

	links := []types.Link{
		{Short: "OLD", Long: "https://example.com/old", Created: 100, CreatedBy: "a@example.com"},
		{Short: "NEW", Long: "https://example.com/new", Created: 300, CreatedBy: "a@example.com"},
		{Short: "MID", Long: "https://example.com/mid", Created: 200, CreatedBy: "b@example.com"},
	}
	for _, link := range links {
		if err := NewLink(link); err != nil {
			t.Fatal(err)
		}
	}

	everybodys, err := ListLinks("", LINK_LIST_LIMIT)
	if err != nil {
		t.Fatal(err)
	}
	if got := codes(everybodys); got != "NEW,MID,OLD" {
		t.Errorf("an admin sees %q, want newest first", got)
	}

	theirs, err := ListLinks("a@example.com", LINK_LIST_LIMIT)
	if err != nil {
		t.Fatal(err)
	}
	if got := codes(theirs); got != "NEW,OLD" {
		t.Errorf("one owner sees %q, want only their own", got)
	}

	// An owner with nothing gets an empty list rather than nil, which is what
	// the dashboard's json and its templates expect.
	none, err := ListLinks("nobody@example.com", LINK_LIST_LIMIT)
	if err != nil {
		t.Fatal(err)
	}
	if none == nil || len(none) != 0 {
		t.Errorf("an owner with no links got %#v", none)
	}
}

func TestUpdateLinkRenamesAndKeepsTheClicks(t *testing.T) {
	useTestDatabase(t)

	if err := NewLink(types.Link{Short: "BEFORE", Long: "https://example.com", CreatedBy: "owner@example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := IncrementClicks("BEFORE"); err != nil {
		t.Fatal(err)
	}

	moved, err := UpdateLink("BEFORE", "after", "elsewhere.com", nil, "owner@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if moved.Short != "AFTER" || moved.Long != "https://elsewhere.com" || moved.Clicks != 1 {
		t.Errorf("the renamed link is %+v", moved)
	}

	if _, err := GetLink("BEFORE"); !errors.Is(err, NotFoundErr) {
		t.Errorf("the old code still resolves: %v", err)
	}

	kept, err := GetLink("AFTER")
	if err != nil {
		t.Fatal(err)
	}
	if kept.Clicks != 1 || kept.CreatedBy != "owner@example.com" {
		t.Errorf("the move lost something: %+v", kept)
	}
}

func TestUpdateLinkWillNotRenameOntoATakenCode(t *testing.T) {
	useTestDatabase(t)

	for _, short := range []string{"ONE", "TWO"} {
		if err := NewLink(types.Link{Short: short, Long: "https://example.com/" + short}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := UpdateLink("ONE", "TWO", "", nil, ""); !errors.Is(err, AlreadyExistsErr) {
		t.Fatalf("renaming onto a taken code is %v, want AlreadyExistsErr", err)
	}

	// Both have to still be there, and still be themselves.
	for _, short := range []string{"ONE", "TWO"} {
		link, err := GetLink(short)
		if err != nil {
			t.Fatalf("%s went away: %v", short, err)
		}
		if link.Long != "https://example.com/"+short {
			t.Errorf("%s is now %q", short, link.Long)
		}
	}
}

func TestOnlyTheOwnerOrAnAdminMayChangeALink(t *testing.T) {
	useTestDatabase(t)

	if err := NewLink(types.Link{Short: "MINE", Long: "https://example.com", CreatedBy: "owner@example.com"}); err != nil {
		t.Fatal(err)
	}

	if _, err := UpdateLink("MINE", "", "https://somewhere.else", nil, "stranger@example.com"); !errors.Is(err, NotOwnedErr) {
		t.Errorf("a stranger editing a link is %v, want NotOwnedErr", err)
	}
	if err := DeleteLink("MINE", "stranger@example.com"); !errors.Is(err, NotOwnedErr) {
		t.Errorf("a stranger deleting a link is %v, want NotOwnedErr", err)
	}

	// Nothing was written while it was being refused.
	link, err := GetLink("MINE")
	if err != nil {
		t.Fatal(err)
	}
	if link.Long != "https://example.com" {
		t.Errorf("a refused edit still landed: %q", link.Long)
	}

	// An empty requireOwner is an admin, who may.
	if err := DeleteLink("MINE", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := GetLink("MINE"); !errors.Is(err, NotFoundErr) {
		t.Errorf("the link survived its deletion: %v", err)
	}
}

func TestTheAllowList(t *testing.T) {
	useTestDatabase(t)

	first, err := NewUser("  Someone@Example.com ", true)
	if err != nil {
		t.Fatal(err)
	}
	if first.Email != "someone@example.com" || !first.Admin {
		t.Errorf("the new user is %+v", first)
	}

	// Re-adding somebody must not quietly reset their admin flag or their
	// login history.
	if _, err := NewUser("someone@example.com", false); !errors.Is(err, AlreadyExistsErr) {
		t.Fatalf("adding an address twice is %v, want AlreadyExistsErr", err)
	}

	if _, err := GetUser("nobody@example.com"); !errors.Is(err, UserNotFoundErr) {
		t.Errorf("an address nobody allowed is %v, want UserNotFoundErr", err)
	}

	at := time.Unix(1700000000, 0)
	if err := MarkLoggedIn("SOMEONE@example.com", at); err != nil {
		t.Fatal(err)
	}

	user, err := GetUser("someone@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if user.LastLogin != at.Unix() {
		t.Errorf("LastLogin = %d, want %d", user.LastLogin, at.Unix())
	}
	if !user.Admin {
		t.Error("signing in cleared the admin flag")
	}
}

func TestADisabledUserMayNotSignIn(t *testing.T) {
	useTestDatabase(t)

	if _, err := NewUser("someone@example.com", false); err != nil {
		t.Fatal(err)
	}

	disabled := true
	updated, err := UpdateUser("someone@example.com", nil, &disabled)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Disabled {
		t.Errorf("the blocked user came back as %+v", updated)
	}

	// The row stays for the history; what changes is that the login is
	// refused, and refused differently from an address nobody allowed.
	user, err := GetUser("someone@example.com")
	if !errors.Is(err, UserDisabledErr) {
		t.Fatalf("a disabled user signing in is %v, want UserDisabledErr", err)
	}
	if user.Email != "someone@example.com" {
		t.Errorf("the refusal came back without the user: %+v", user)
	}

	// A nil for both is "change nothing", not "clear both".
	if _, err := UpdateUser("someone@example.com", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := GetUser("someone@example.com"); !errors.Is(err, UserDisabledErr) {
		t.Error("a no-op update un-blocked the user")
	}
}

func TestTwoPeopleCanEachHaveATagCalledWork(t *testing.T) {
	useTestDatabase(t)

	for _, owner := range []string{"a@example.com", "b@example.com"} {
		if _, err := NewTag(owner, "Work", "#112233"); err != nil {
			t.Fatalf("%s could not have a work tag: %v", owner, err)
		}
	}

	// And neither can have two of them, whatever the casing.
	if _, err := NewTag("a@example.com", "work", "#445566"); !errors.Is(err, AlreadyExistsErr) {
		t.Errorf("a second work tag is %v, want AlreadyExistsErr", err)
	}

	theirs, err := ListTags("a@example.com", TAG_LIST_LIMIT)
	if err != nil {
		t.Fatal(err)
	}
	if len(theirs) != 1 || theirs[0].Name != "Work" || theirs[0].Owner != "a@example.com" {
		t.Errorf("one owner's tags are %+v", theirs)
	}

	everybodys, err := ListTags("", TAG_LIST_LIMIT)
	if err != nil {
		t.Fatal(err)
	}
	if len(everybodys) != 2 {
		t.Errorf("an admin sees %d tags, want 2", len(everybodys))
	}
}

func TestRenamingATagRelabelsTheOwnersLinks(t *testing.T) {
	useTestDatabase(t)

	owner := "owner@example.com"
	if _, err := NewTag(owner, "work", "#112233"); err != nil {
		t.Fatal(err)
	}
	if err := NewLink(types.Link{Short: "TAGGED", Long: "https://example.com", CreatedBy: owner, Tags: "work"}); err != nil {
		t.Fatal(err)
	}
	// Somebody else's link carrying the same name must not be touched.
	if err := NewLink(types.Link{Short: "THEIRS", Long: "https://example.com", CreatedBy: "other@example.com", Tags: "work"}); err != nil {
		t.Fatal(err)
	}

	if _, err := UpdateTag(owner, "work", "job", ""); err != nil {
		t.Fatal(err)
	}

	mine, err := GetLink("TAGGED")
	if err != nil {
		t.Fatal(err)
	}
	if mine.Tags != "job" {
		t.Errorf("the owner's link is tagged %q, want job", mine.Tags)
	}

	theirs, err := GetLink("THEIRS")
	if err != nil {
		t.Fatal(err)
	}
	if theirs.Tags != "work" {
		t.Errorf("somebody else's link was relabelled to %q", theirs.Tags)
	}

	// The tag itself moved rather than being duplicated.
	list, err := ListTags(owner, TAG_LIST_LIMIT)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "job" {
		t.Errorf("the owner's tags after the rename are %+v", list)
	}
}

func TestDeletingATagTakesItOffTheLinks(t *testing.T) {
	useTestDatabase(t)

	owner := "owner@example.com"
	if _, err := NewTag(owner, "work", "#112233"); err != nil {
		t.Fatal(err)
	}
	if err := NewLink(types.Link{Short: "BOTH", Long: "https://example.com", CreatedBy: owner, Tags: "work,home"}); err != nil {
		t.Fatal(err)
	}

	if err := DeleteTag(owner, "work"); err != nil {
		t.Fatal(err)
	}

	link, err := GetLink("BOTH")
	if err != nil {
		t.Fatal(err)
	}
	if link.Tags != "home" {
		t.Errorf("the link is tagged %q, want home", link.Tags)
	}

	if err := DeleteTag(owner, "work"); !errors.Is(err, TagNotFoundErr) {
		t.Errorf("deleting it twice is %v, want TagNotFoundErr", err)
	}
}

func TestRenamingATagOntoOneTheOwnerHasIsRefused(t *testing.T) {
	useTestDatabase(t)

	owner := "owner@example.com"
	for _, name := range []string{"work", "home"} {
		if _, err := NewTag(owner, name, "#112233"); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := UpdateTag(owner, "work", "home", ""); !errors.Is(err, TagInUseErr) {
		t.Fatalf("merging two tags is %v, want TagInUseErr", err)
	}

	list, err := ListTags(owner, TAG_LIST_LIMIT)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Errorf("a refused rename left %+v", list)
	}
}

func TestEnsureTagsMakesOnlyWhatIsMissing(t *testing.T) {
	useTestDatabase(t)

	owner := "owner@example.com"
	if _, err := NewTag(owner, "work", "#112233"); err != nil {
		t.Fatal(err)
	}

	if err := EnsureTags(owner, []string{"work", "home", "play"}); err != nil {
		t.Fatal(err)
	}

	list, err := ListTags(owner, TAG_LIST_LIMIT)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("the owner has %+v, want three tags", list)
	}

	colors := map[string]bool{}
	for _, tag := range list {
		// The tag that was already there keeps the colour it was given.
		if tag.Name == "work" && tag.Color != "#112233" {
			t.Errorf("the existing tag's colour changed to %q", tag.Color)
		}
		colors[tag.Color] = true
	}
	if len(colors) != 3 {
		t.Errorf("three tags share %d colours, want one each", len(colors))
	}
}

func codes(links []types.Link) string {
	short := make([]string, 0, len(links))
	for _, link := range links {
		short = append(short, link.Short)
	}

	return strings.Join(short, ",")
}

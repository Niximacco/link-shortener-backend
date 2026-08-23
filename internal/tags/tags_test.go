package tags

import (
	"strings"
	"testing"
)

func TestParseTrimsDeduplicatesAndKeepsOrder(t *testing.T) {
	cases := map[string][]string{
		"":                    nil,
		"   ":                 nil,
		",,,":                 nil,
		"work":                {"work"},
		" work , urgent ":     {"work", "urgent"},
		"work,WORK,Work":      {"work"},
		"a,,b":                {"a", "b"},
		"two   words, second": {"two words", "second"},
	}

	for list, want := range cases {
		got := Parse(list)
		if len(got) != len(want) {
			t.Errorf("Parse(%q) = %v, want %v", list, got, want)
			continue
		}

		for i := range want {
			if got[i] != want[i] {
				t.Errorf("Parse(%q) = %v, want %v", list, got, want)
				break
			}
		}
	}
}

func TestValidAcceptsOrdinaryNames(t *testing.T) {
	for _, name := range []string{"work", "Work", "two words", "side-project", "q1_2026", "50%", "café"} {
		if !Valid(name) {
			t.Errorf("Valid(%q) = false, want true", name)
		}
	}
}

func TestValidRejectsNamesThatWouldNotSurviveStorage(t *testing.T) {
	bad := []string{
		"",
		"   ",
		"has,comma", // the separator on the link
		"has/slash", // the tag name is a url path segment
		`has\slash`, // and datastore keys are joined on one too
		"bell\x07",  // a control character has no business in a label
		"null\x00byte",
		strings.Repeat("x", MAX_NAME_LEN+1),
	}

	for _, name := range bad {
		if Valid(name) {
			t.Errorf("Valid(%q) = true, want false", name)
		}
	}
}

// A newline is whitespace, so it is collapsed like any other run of it rather
// than rejected. Worth pinning down: it is the one control character that
// reaches Valid already dealt with, and "work\nurgent" pasted into the field
// should read as one tag rather than as an error.
func TestValidTreatsWhitespaceAsWhitespace(t *testing.T) {
	if !Valid("new\nline") {
		t.Error(`Valid("new\nline") = false, want it collapsed to "new line"`)
	}

	if got := Normalize("new\nline"); got != "new line" {
		t.Errorf("Normalize = %q, want %q", got, "new line")
	}
}

func TestCleanRoundTrips(t *testing.T) {
	got, err := Clean("  work ,, URGENT , work ")
	if err != nil {
		t.Fatalf("Clean returned %s", err)
	}

	if got != "work,URGENT" {
		t.Errorf("Clean = %q, want %q", got, "work,URGENT")
	}
}

func TestCleanRejectsBadNamesAndTooMany(t *testing.T) {
	if _, err := Clean("fine,has/slash"); err == nil {
		t.Error("Clean accepted a name with a slash in it")
	}

	var many []string
	for i := 0; i < MAX_PER_LINK+1; i++ {
		many = append(many, string(rune('a'+i)))
	}

	if _, err := Clean(Join(many)); err == nil {
		t.Errorf("Clean accepted %d tags on one link", len(many))
	}
}

func TestHasIgnoresCaseAndSpacing(t *testing.T) {
	list := "work, side project"

	for _, name := range []string{"work", "WORK", " work ", "side project", "Side Project"} {
		if !Has(list, name) {
			t.Errorf("Has(%q, %q) = false, want true", list, name)
		}
	}

	for _, name := range []string{"", "  ", "wor", "project"} {
		if Has(list, name) {
			t.Errorf("Has(%q, %q) = true, want false", list, name)
		}
	}
}

func TestRenameReplacesInPlace(t *testing.T) {
	got, changed := Rename("work,urgent,personal", "URGENT", "later")
	if !changed {
		t.Fatal("Rename reported no change")
	}

	if got != "work,later,personal" {
		t.Errorf("Rename = %q, want %q", got, "work,later,personal")
	}
}

func TestRenameOntoAnExistingTagDoesNotDuplicateIt(t *testing.T) {
	got, changed := Rename("work,urgent", "urgent", "work")
	if !changed {
		t.Fatal("Rename reported no change")
	}

	if got != "work" {
		t.Errorf("Rename = %q, want %q - the link ended up carrying the tag twice", got, "work")
	}
}

func TestRenameToEmptyRemovesTheTag(t *testing.T) {
	got, changed := Rename("work,urgent", "work", "")
	if !changed || got != "urgent" {
		t.Errorf("Rename = %q (changed=%v), want %q", got, changed, "urgent")
	}
}

func TestRenameLeavesAListWithoutTheTagAlone(t *testing.T) {
	got, changed := Rename("work,urgent", "personal", "later")
	if changed {
		t.Error("Rename reported a change it didn't make")
	}

	if got != "work,urgent" {
		t.Errorf("Rename = %q, want the list untouched", got)
	}
}

func TestValidColorTakesSixDigitHexOnly(t *testing.T) {
	for _, color := range []string{"#3b7dd8", "#FFFFFF", "#000000", "#AbCdEf"} {
		if !ValidColor(color) {
			t.Errorf("ValidColor(%q) = false, want true", color)
		}
	}

	// Every one of these would otherwise be written into a style attribute.
	bad := []string{
		"",
		"3b7dd8",
		"#3b7dd",
		"#3b7dd80",
		"#gggggg",
		"red",
		"#3b7dd8;background:url(x)",
		"expression(alert(1))",
	}

	for _, color := range bad {
		if ValidColor(color) {
			t.Errorf("ValidColor(%q) = true, want false", color)
		}
	}
}

func TestReadablePicksContrastingText(t *testing.T) {
	if got := Readable("#ffffff"); got != "#1d1d1f" {
		t.Errorf("Readable(white) = %q, want dark text", got)
	}

	if got := Readable("#000000"); got != "#ffffff" {
		t.Errorf("Readable(black) = %q, want light text", got)
	}

	// A colour that isn't one falls back to the default rather than to nothing.
	if got := Readable("nonsense"); got != Readable(DefaultColor) {
		t.Errorf("Readable(nonsense) = %q, want the default's answer", got)
	}
}

func TestEveryPaletteColorIsUsable(t *testing.T) {
	for _, color := range Palette {
		if !ValidColor(color) {
			t.Errorf("palette colour %q is not a valid hex value", color)
		}
	}

	if !ValidColor(DefaultColor) {
		t.Errorf("default colour %q is not a valid hex value", DefaultColor)
	}
}

func TestPickColorAvoidsWhatIsAlreadyUsed(t *testing.T) {
	if got := PickColor(nil); got != Palette[0] {
		t.Errorf("PickColor(nil) = %q, want the first palette colour", got)
	}

	if got := PickColor([]string{Palette[0]}); got != Palette[1] {
		t.Errorf("PickColor = %q, want the first unused palette colour", got)
	}

	// Casing is not what makes two colours different.
	if got := PickColor([]string{strings.ToUpper(Palette[0])}); got != Palette[1] {
		t.Errorf("PickColor = %q, want an upper-cased colour to count as used", got)
	}

	// Once they are all spoken for it wraps rather than returning nothing.
	if got := PickColor(Palette); !ValidColor(got) {
		t.Errorf("PickColor with a full palette = %q, want a usable colour", got)
	}
}

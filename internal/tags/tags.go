// Package tags handles the names and colours links are labelled with.
//
// A link carries its tags as one comma separated string, so the whole of a
// link's labelling is a single datastore property that comes back with the
// entity. Everything here is about getting from that string to a clean list and
// back again: splitting, trimming, dropping duplicates and rejecting anything
// that would not survive the round trip.
package tags

import (
	"fmt"
	"strings"
	"unicode"
)

// MAX_NAME_LEN caps a tag name. Tags are rendered as pills next to a link, and
// something longer than this is a description rather than a label.
const MAX_NAME_LEN = 32

// MAX_PER_LINK caps how many tags one link can carry. The stored property is a
// single string, so this is also what keeps it a sensible size.
const MAX_PER_LINK = 10

// Palette is the set of colours a tag can be given. Keeping it a fixed list
// means the pills on the dashboard stay legible in both themes, and it gives
// something to pick from when a tag is created on the fly.
var Palette = []string{
	"#3b7dd8", // blue
	"#2e9e6b", // green
	"#c9752c", // amber
	"#b4453c", // red
	"#8257c5", // purple
	"#2b8f9e", // teal
	"#a8477f", // magenta
	"#5f6b7a", // slate
}

// DefaultColor is what a tag gets when nobody picked a colour.
const DefaultColor = "#5f6b7a"

// Normalize puts a name in the form it is stored and compared in: no
// surrounding space, and no runs of whitespace inside.
func Normalize(name string) string {
	return strings.Join(strings.Fields(name), " ")
}

// Key is what two names are compared by. Tags are case insensitive to the
// person using them - "Work" and "work" are the same tag - but the casing they
// were created with is what gets displayed.
func Key(name string) string {
	return strings.ToLower(Normalize(name))
}

// Valid reports whether a name can be used as a tag.
//
// A comma is out because it is the separator on the link, and the control
// characters are out because a name ends up in html, in a datastore key and in
// a url path segment.
func Valid(name string) bool {
	name = Normalize(name)
	if name == "" || len(name) > MAX_NAME_LEN {
		return false
	}

	for _, character := range name {
		switch {
		case character == ',' || character == '/' || character == '\\':
			return false
		case unicode.IsControl(character):
			return false
		}
	}

	return true
}

// Parse turns the stored comma separated string into a list of names: trimmed,
// with the empties and the duplicates dropped, in the order they were written.
//
// It is deliberately forgiving. The string is a property on an entity that can
// be edited by hand in the datastore console, so " work ,, WORK" should read as
// one tag rather than as an error.
func Parse(list string) []string {
	var names []string
	seen := map[string]bool{}

	for _, part := range strings.Split(list, ",") {
		name := Normalize(part)
		if name == "" {
			continue
		}

		key := Key(name)
		if seen[key] {
			continue
		}

		seen[key] = true
		names = append(names, name)
	}

	return names
}

// Join writes a list of names back to the stored form.
func Join(names []string) string {
	return strings.Join(names, ",")
}

// Clean is Parse and Join together: it takes whatever was submitted and returns
// what should be stored, or an error naming the first thing wrong with it.
func Clean(list string) (string, error) {
	names := Parse(list)

	if len(names) > MAX_PER_LINK {
		return "", fmt.Errorf("a link can carry at most %d tags", MAX_PER_LINK)
	}

	for _, name := range names {
		if !Valid(name) {
			return "", fmt.Errorf("%q can't be used as a tag name: up to %d characters, and no commas or slashes", name, MAX_NAME_LEN)
		}
	}

	return Join(names), nil
}

// Has reports whether a stored tag list contains a name, comparing the way Key
// does.
func Has(list string, name string) bool {
	want := Key(name)
	if want == "" {
		return false
	}

	for _, tag := range Parse(list) {
		if Key(tag) == want {
			return true
		}
	}

	return false
}

// Rename swaps one name for another in a stored list and reports whether it
// changed anything. Passing an empty replacement removes the tag, which is what
// deleting one does to the links carrying it.
func Rename(list string, from string, to string) (string, bool) {
	want := Key(from)
	names := Parse(list)

	changed := false
	kept := make([]string, 0, len(names))
	for _, name := range names {
		if Key(name) != want {
			kept = append(kept, name)
			continue
		}

		changed = true
		if to != "" {
			kept = append(kept, to)
		}
	}

	if !changed {
		return list, false
	}

	// Renaming onto a tag the link already has would leave it listed twice.
	return Join(Parse(Join(kept))), true
}

// ValidColor reports whether a colour is a six digit hex value. Anything else
// is refused rather than corrected: the value is written straight into a style
// attribute, so it is not a place to be relaxed about what goes in.
func ValidColor(color string) bool {
	if len(color) != 7 || color[0] != '#' {
		return false
	}

	for _, character := range color[1:] {
		switch {
		case character >= '0' && character <= '9':
		case character >= 'a' && character <= 'f':
		case character >= 'A' && character <= 'F':
		default:
			return false
		}
	}

	return true
}

// Readable returns the text colour to draw on top of a background: near-black
// on a light colour, white on a dark one. It keeps a tag pill legible whatever
// colour was picked for it, including a colour set by hand on the entity.
//
// The weights are the usual perceived-brightness ones - the eye reads green as
// much brighter than blue at the same value - and the threshold is the middle
// of that range.
func Readable(color string) string {
	if !ValidColor(color) {
		color = DefaultColor
	}

	component := func(at int) int {
		var value int
		fmt.Sscanf(color[at:at+2], "%02x", &value)
		return value
	}

	brightness := (component(1)*299 + component(3)*587 + component(5)*114) / 1000
	if brightness > 150 {
		return "#1d1d1f"
	}

	return "#ffffff"
}

// PickColor chooses a colour for a tag that is being created without one,
// preferring one the caller is not already using so a new tag stands out from
// the tags beside it. taken is the colours already in use.
func PickColor(taken []string) string {
	used := map[string]bool{}
	for _, color := range taken {
		used[strings.ToLower(color)] = true
	}

	for _, color := range Palette {
		if !used[color] {
			return color
		}
	}

	// Every colour is spoken for, so the palette just wraps around.
	return Palette[len(taken)%len(Palette)]
}

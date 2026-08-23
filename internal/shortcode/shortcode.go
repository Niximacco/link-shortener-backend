package shortcode

import (
	"math/rand"
	"strings"
)

var letters = []rune("ABCDEFGHIJKLMNOPQRSTUVWXYZ123456789")
var default_len = 6

type ShortCode struct {
	// required fields
	Len int
}

type Option func(f *ShortCode)

func Len(len int) Option {
	return func(f *ShortCode) {
		f.Len = len
	}
}

func New(opts ...Option) string {
	short := &ShortCode{Len: default_len}
	for _, opt := range opts {
		opt(short)
	}

	b := make([]rune, default_len)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

// reserved are the paths the service serves itself. A short code matching one
// would be shadowed by that route and unreachable, so they can't be claimed.
var reserved = map[string]bool{
	"link":   true,
	"links":  true,
	"user":   true,
	"users":  true,
	"tag":    true,
	"tags":   true,
	"login":  true,
	"logout": true,
	"auth":   true,
	"api":    true,
	"static": true,
}

const max_len = 64

// Valid reports whether a short code can be used. Codes end up as a single path
// segment, so anything that would change the shape of the url - a slash, a
// query, a space - is out.
func Valid(short string) bool {
	if len(short) == 0 || len(short) > max_len {
		return false
	}

	if reserved[strings.ToLower(short)] {
		return false
	}

	for _, character := range short {
		switch {
		case character >= 'a' && character <= 'z':
		case character >= 'A' && character <= 'Z':
		case character >= '0' && character <= '9':
		case character == '-' || character == '_':
		default:
			return false
		}
	}

	return true
}

package router

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// The Go version this module needs is stated in two files - the `go` directive
// in go.mod and the builder image in the Dockerfile - and nothing makes them
// agree. Since modernc.org/sqlite came in, the floor is 1.26 rather than the
// 1.25 this service pinned on Datastore, and an older builder refuses the
// module rather than downloading a newer toolchain.
//
// That failure is invisible to everything else in the workflow: `go vet` and
// `go test` read go.mod, so they pass, and the container build is the only step
// that reads the Dockerfile. It fails after authenticating and pushing nothing,
// on a one line version complaint, having spent the whole build getting there.
//
// This is that check, run where the rest of the tests are. It lives beside the
// route set test because both are about the same class of failure: things that
// break at build time or at startup rather than on a request, which are exactly
// the ones a test suite otherwise never sees.
//
// It is a comparison rather than an equality: a builder newer than the floor in
// go.mod is fine, and is what a bump looks like halfway through.
func TestTheBuilderImageCanBuildThisModule(t *testing.T) {
	root := repoRoot(t)

	required := goDirective(t, filepath.Join(root, "go.mod"))
	builder := builderImage(t, filepath.Join(root, "Dockerfile"))

	if builder.major < required.major ||
		(builder.major == required.major && builder.minor < required.minor) {
		t.Fatalf("the Dockerfile builds with Go %d.%d and go.mod requires %d.%d - "+
			"the container build will fail at `go mod download` with nothing else noticing first",
			builder.major, builder.minor, required.major, required.minor)
	}
}

type version struct {
	major int
	minor int
}

// goDirective reads the `go` line out of go.mod, which is the floor this module
// needs.
func goDirective(t *testing.T, path string) version {
	t.Helper()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("could not read go.mod: %s", err.Error())
	}

	found := regexp.MustCompile(`(?m)^go (\d+)\.(\d+)`).FindSubmatch(body)
	if found == nil {
		t.Fatal("go.mod has no `go` directive")
	}

	return version{atoi(t, string(found[1])), atoi(t, string(found[2]))}
}

// builderImage reads the golang tag off the build stage of the Dockerfile.
func builderImage(t *testing.T, path string) version {
	t.Helper()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("could not read the Dockerfile: %s", err.Error())
	}

	// Only a FROM line counts. The version is written in the comment above it
	// as well, and a regex loose enough to match prose would be matching the
	// explanation rather than the instruction.
	found := regexp.MustCompile(`(?m)^FROM golang:(\d+)\.(\d+)`).FindSubmatch(body)
	if found == nil {
		t.Fatal("the Dockerfile has no `FROM golang:<major>.<minor>` build stage")
	}

	return version{atoi(t, string(found[1])), atoi(t, string(found[2]))}
}

func atoi(t *testing.T, text string) int {
	t.Helper()

	number, err := strconv.Atoi(text)
	if err != nil {
		t.Fatalf("%q is not a number", text)
	}

	return number
}

// repoRoot walks up from this package to the directory holding go.mod, so the
// test does not care how deep it is or what the checkout is called.
func repoRoot(t *testing.T) string {
	t.Helper()

	at, err := os.Getwd()
	if err != nil {
		t.Fatalf("could not find the working directory: %s", err.Error())
	}

	for range 10 {
		if _, err := os.Stat(filepath.Join(at, "go.mod")); err == nil {
			return at
		}

		parent := filepath.Dir(at)
		if parent == at {
			break
		}

		at = parent
	}

	t.Fatal("could not find the repository root from this package")
	return ""
}

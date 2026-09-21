package testing_test

import (
	"os"
	"regexp"
	"slices"
	"testing"
)

// The README's roster names every fixture package and nothing else:
// a package added here is placed in the README before it is imported.
// The roster's shape is the README's contract: a fixture's bullet
// opens with its name in backticks and a space, and no other bullet
// opens that way; every subdirectory here is a fixture.
func TestReadmeNamesEveryFixture(t *testing.T) {
	text, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	var named []string
	for _, m := range regexp.MustCompile("(?m)^- `([a-z0-9_]+)` ").FindAllStringSubmatch(string(text), -1) {
		named = append(named, m[1])
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var present []string
	for _, e := range entries {
		if e.IsDir() {
			present = append(present, e.Name())
		}
	}
	slices.Sort(named)
	slices.Sort(present)
	if !slices.Equal(named, present) {
		t.Fatalf("README.md names %v; the directory holds %v", named, present)
	}
}

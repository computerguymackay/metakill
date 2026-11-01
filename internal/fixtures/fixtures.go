// Package fixtures generates small files with known metadata. The handler
// tests, the golden-file tests (via cmd/genfixtures) and the hidden --selftest
// flag all use these generators, so no opaque binary blobs are needed.
package fixtures

import "sort"

// Fixture is one generated file.
type Fixture struct {
	Name   string // file name, e.g. "photo.jpg"
	Format string // expected handler name
	Data   []byte
	// Clean is true for fixtures that carry no metadata at all.
	Clean bool
	// Skipped is true for fixtures the handler recognises but deliberately
	// leaves alone (signed packages).
	Skipped bool
}

var generators = map[string]func() Fixture{}

func register(name string, gen func() Fixture) { generators[name] = gen }

// All returns every fixture sorted by name.
func All() []Fixture {
	names := make([]string, 0, len(generators))
	for n := range generators {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]Fixture, 0, len(names))
	for _, n := range names {
		out = append(out, generators[n]())
	}
	return out
}

// Get returns one fixture by name.
func Get(name string) (Fixture, bool) {
	g, ok := generators[name]
	if !ok {
		return Fixture{}, false
	}
	return g(), true
}

func init() {
	register("notes.md", func() Fixture {
		return Fixture{Name: "notes.md", Format: "text", Data: []byte("# Notes\n\nNothing to see here.\n"), Clean: true}
	})
}

//go:generate go run ./genfixtures ../../testdata/fixtures

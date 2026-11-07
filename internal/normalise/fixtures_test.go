package normalise

import (
	"bytes"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
)

// fixtureDir reads testkit's recorded fixtures straight from testkit/testdata.
// These tests cannot import package testkit: it imports the root package,
// which imports this one, and an in-package test may not close that cycle.
// The methods mirror testkit.Fixture, FixtureNames, Chunks and PathFor.
type fixtureDir struct{ fsys fs.FS }

// fx is the fixture directory the tests in this package read.
var fx = fixtureDir{os.DirFS("../../testkit/testdata")}

var fixtureExts = []string{".sse", ".json"}

// file resolves a fixture name to its file name, or "" if there is none.
func (d fixtureDir) file(name string) string {
	for _, ext := range fixtureExts {
		if _, err := fs.Stat(d.fsys, name+ext); err == nil {
			return name + ext
		}
	}
	return ""
}

// Fixture returns the bytes of a fixture.
func (d fixtureDir) Fixture(name string) ([]byte, error) {
	f := d.file(name)
	if f == "" {
		return nil, fs.ErrNotExist
	}
	return fs.ReadFile(d.fsys, f)
}

// FixtureNames returns every fixture name, sorted.
func (d fixtureDir) FixtureNames() []string {
	var names []string
	_ = fs.WalkDir(d.fsys, ".", func(p string, e fs.DirEntry, err error) error {
		if err == nil && !e.IsDir() {
			names = append(names, strings.TrimSuffix(p, path.Ext(p)))
		}
		return err
	})
	sort.Strings(names)
	return names
}

// Chunks splits a fixture one SSE event per chunk, or whole for JSON; nil if unknown.
func (d fixtureDir) Chunks(name string) [][]byte {
	f := d.file(name)
	if f == "" {
		return nil
	}
	data, err := fs.ReadFile(d.fsys, f)
	if err != nil {
		return nil
	}
	if path.Ext(f) != ".sse" {
		return [][]byte{data}
	}
	var chunks [][]byte
	for _, c := range bytes.SplitAfter(data, []byte("\n\n")) {
		if len(c) > 0 {
			chunks = append(chunks, c)
		}
	}
	return chunks
}

// PathFor is the upstream API path a fixture's provider serves.
func (d fixtureDir) PathFor(name string) string {
	if strings.HasPrefix(name, "anthropic/") {
		return "/v1/messages"
	}
	return "/v1/chat/completions"
}

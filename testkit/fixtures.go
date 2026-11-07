package testkit

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
)

// fixtureFS holds recorded, scrubbed provider responses and request bodies.
// Layout: testdata/<provider>/<name>.sse|.json. Fixture names drop the
// testdata/ prefix and the extension: "openai/chat-stream-text".
//
//go:embed testdata
var fixtureFS embed.FS

// ErrUnknownFixture is returned by Fixture for a name with no file.
var ErrUnknownFixture = errors.New("testkit: unknown fixture")

// fixtureExts are tried in order when resolving a name to a file.
var fixtureExts = []string{".sse", ".json"}

// FixtureNames returns every fixture name, sorted. Names containing
// "/request-" are request bodies (inputs); the rest are upstream responses.
func FixtureNames() []string {
	var names []string
	_ = fs.WalkDir(fixtureFS, "testdata", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		names = append(names, strings.TrimSuffix(strings.TrimPrefix(p, "testdata/"), path.Ext(p)))
		return nil
	})
	sort.Strings(names)
	return names
}

// Fixture returns the raw bytes of a fixture, exactly as replayed.
func Fixture(name string) ([]byte, error) {
	file, err := fixtureFile(name)
	if err != nil {
		return nil, err
	}
	return fixtureFS.ReadFile(file)
}

// Chunks returns a fixture split the way FakeUpstream writes it: one chunk per
// SSE event (each ending in its blank line) for .sse fixtures, the whole body
// for .json fixtures. It returns nil for an unknown fixture.
func Chunks(name string) [][]byte {
	file, err := fixtureFile(name)
	if err != nil {
		return nil
	}
	data, err := fixtureFS.ReadFile(file)
	if err != nil {
		return nil
	}
	if path.Ext(file) != ".sse" {
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

func fixtureFile(name string) (string, error) {
	for _, ext := range fixtureExts {
		file := "testdata/" + name + ext
		if _, err := fs.Stat(fixtureFS, file); err == nil {
			return file, nil
		}
	}
	return "", fmt.Errorf("%w: %q", ErrUnknownFixture, name)
}

// fixtureStatus is 200, or N for a fixture named "<provider>/error-N".
func fixtureStatus(name string) int {
	_, base := path.Split(name)
	if code, ok := strings.CutPrefix(base, "error-"); ok {
		if n, err := strconv.Atoi(code); err == nil {
			return n
		}
	}
	return 200
}

// fixtureContentType is text/event-stream for .sse fixtures, else JSON.
func fixtureContentType(name string) string {
	if file, err := fixtureFile(name); err == nil && path.Ext(file) == ".sse" {
		return "text/event-stream"
	}
	return "application/json"
}

// Provider API paths the fake serves without a FixtureHeader.
const (
	openAIChatPath        = "/v1/chat/completions"
	anthropicMessagesPath = "/v1/messages"
)

// PathFor returns the upstream API path a fixture's provider serves:
// "/v1/messages" for "anthropic/..." fixtures, "/v1/chat/completions" for
// the rest. A proxy test prefixes it with the provider, e.g.
// "/anthropic" + PathFor(name).
func PathFor(fixture string) string {
	if strings.HasPrefix(fixture, "anthropic/") {
		return anthropicMessagesPath
	}
	return openAIChatPath
}

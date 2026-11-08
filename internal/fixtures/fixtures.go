package fixtures

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

// fixtureFS holds the fixtures. Layout: testdata/<provider>/<name>.sse|.json.
// Fixture names drop the testdata/ prefix and the extension:
// "openai/chat-stream-text".
//
//go:embed testdata
var fixtureFS embed.FS

// ErrUnknown is returned by Bytes for a name with no file.
var ErrUnknown = errors.New("testkit: unknown fixture")

// exts are tried in order when resolving a name to a file.
var exts = []string{".sse", ".json"}

// Provider API paths a fixture's provider serves.
const (
	OpenAIChatPath        = "/v1/chat/completions"
	OpenAIResponsesPath   = "/v1/responses"
	AnthropicMessagesPath = "/v1/messages"
)

// Names returns every fixture name, sorted. Names containing "/request-" are
// request bodies (inputs); the rest are upstream responses.
func Names() []string {
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

// IsResponse reports whether name is a known upstream response fixture (not
// a "request-" input).
func IsResponse(name string) bool {
	_, err := file(name)
	return err == nil && !strings.Contains(name, "/request-")
}

// Bytes returns the raw bytes of a fixture, exactly as replayed.
func Bytes(name string) ([]byte, error) {
	f, err := file(name)
	if err != nil {
		return nil, err
	}
	return fixtureFS.ReadFile(f)
}

// Chunks returns a fixture split the way a fake upstream writes it: one chunk
// per SSE event (each ending in its blank line) for .sse fixtures, the whole
// body for .json fixtures. It returns nil for an unknown fixture.
func Chunks(name string) [][]byte {
	f, err := file(name)
	if err != nil {
		return nil
	}
	data, err := fixtureFS.ReadFile(f)
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

func file(name string) (string, error) {
	for _, ext := range exts {
		f := "testdata/" + name + ext
		if _, err := fs.Stat(fixtureFS, f); err == nil {
			return f, nil
		}
	}
	return "", fmt.Errorf("%w: %q", ErrUnknown, name)
}

// Status is 200, or N for a fixture named "<provider>/error-N".
func Status(name string) int {
	_, base := path.Split(name)
	if code, ok := strings.CutPrefix(base, "error-"); ok {
		if n, err := strconv.Atoi(code); err == nil {
			return n
		}
	}
	return 200
}

// IsStream reports whether name is an event-stream (.sse) fixture.
func IsStream(name string) bool {
	f, err := file(name)
	return err == nil && path.Ext(f) == ".sse"
}

// ContentType is text/event-stream for .sse fixtures, else JSON.
func ContentType(name string) string {
	if IsStream(name) {
		return "text/event-stream"
	}
	return "application/json"
}

// Provider is the path prefix of a fixture's provider: "anthropic" for
// "anthropic/..." fixtures, "openai" for the rest.
func Provider(name string) string {
	if strings.HasPrefix(name, "anthropic/") {
		return "anthropic"
	}
	return "openai"
}

// isResponses reports whether name is an OpenAI Responses API fixture:
// "openai/responses..." or "openai/request-responses...".
func isResponses(name string) bool {
	_, base := path.Split(name)
	return Provider(name) == "openai" &&
		(strings.HasPrefix(base, "responses") || strings.HasPrefix(base, "request-responses"))
}

// PathFor returns the upstream API path a fixture's wire serves.
func PathFor(name string) string {
	switch {
	case Provider(name) == "anthropic":
		return AnthropicMessagesPath
	case isResponses(name):
		return OpenAIResponsesPath
	}
	return OpenAIChatPath
}

// Request returns a request body in the fixture's wire format that matches
// it: "stream": true for event-stream fixtures, false otherwise.
func Request(name string) []byte {
	stream := IsStream(name)
	if Provider(name) == "anthropic" {
		return fmt.Appendf(nil,
			`{"model":"claude-sonnet-4-5","max_tokens":256,"stream":%t,"messages":[{"role":"user","content":"Say hello."}]}`, stream)
	}
	if isResponses(name) {
		return fmt.Appendf(nil,
			`{"model":"gpt-4.1-mini","stream":%t,"instructions":"You are a coding agent.","input":"Say hello."}`, stream)
	}
	return fmt.Appendf(nil,
		`{"model":"gpt-4.1-mini","stream":%t,"messages":[{"role":"system","content":"You are a coding agent."},{"role":"user","content":"Say hello."}]}`, stream)
}

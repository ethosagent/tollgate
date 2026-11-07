package testkit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// FixtureHeader selects the fixture a FakeUpstream replays for one request,
// e.g. "openai/chat-stream-tools". Test-only: a real upstream ignores it.
const FixtureHeader = "x-fixture"

// maxRecordedBody caps how much of each request body the fake keeps.
const maxRecordedBody = 16 << 20

// Options shape how a FakeUpstream replays fixtures. The zero value replays
// immediately and completely.
type Options struct {
	FirstByteDelay  time.Duration // wait before sending status and headers
	ChunkDelay      time.Duration // wait between chunks (SSE events)
	FailAfterChunks int           // cut the connection after N chunks; 0 never cuts
	Status          int           // override the fixture's status; 0 keeps it
}

// Request is one request the fake received.
type Request struct {
	Method   string
	Path     string
	RawQuery string
	Header   http.Header
	Body     []byte
	Fixture  string // the fixture chosen for it ("" if none matched)
	Canceled bool   // the client went away before the response finished
}

// FakeUpstream is an httptest server that replays recorded provider responses.
// It is safe for concurrent use.
type FakeUpstream struct {
	URL string // base URL, e.g. "http://127.0.0.1:53712"

	srv  *httptest.Server
	opts Options

	mu   sync.Mutex
	reqs []Request
}

// NewFakeUpstream starts a fake upstream that is closed when the test ends.
//
// Without a FixtureHeader it picks by path: a path ending in
// /chat/completions replays "openai/chat-stream-text" for a body with
// "stream": true and "openai/chat" otherwise; /messages does the same with
// the anthropic fixtures. Anything else is a 404.
func NewFakeUpstream(t testing.TB, opts Options) *FakeUpstream {
	t.Helper()
	f, stop := NewServer(opts)
	t.Cleanup(stop)
	return f
}

// NewServer starts the same fake upstream as NewFakeUpstream without needing a
// testing.TB, for tools such as benchmarks. Call the returned function to stop
// the server; it waits for in-flight requests.
func NewServer(opts Options) (*FakeUpstream, func()) {
	f := &FakeUpstream{opts: opts}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	f.URL = f.srv.URL
	return f, f.srv.Close
}

// Client returns an HTTP client for the fake (no proxy, no shared state).
func (f *FakeUpstream) Client() *http.Client { return f.srv.Client() }

// Requests returns a copy of every request received so far, in arrival order.
func (f *FakeUpstream) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request(nil), f.reqs...)
}

func (f *FakeUpstream) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, maxRecordedBody))
	name := r.Header.Get(FixtureHeader)
	if name == "" {
		name = defaultFixture(r.URL.Path, body)
	}
	chunks := Chunks(name)
	idx := f.record(r, body, name, chunks != nil)
	if chunks == nil {
		http.Error(w, "testkit: no fixture for "+r.URL.Path+" "+name, http.StatusNotFound)
		return
	}
	if !wait(r.Context(), f.opts.FirstByteDelay) {
		f.markCanceled(idx)
		return
	}
	w.Header().Set("Content-Type", fixtureContentType(name))
	w.WriteHeader(f.status(name))
	f.writeChunks(r.Context(), w, idx, chunks)
}

func (f *FakeUpstream) writeChunks(ctx context.Context, w http.ResponseWriter, idx int, chunks [][]byte) {
	flusher, _ := w.(http.Flusher)
	for i, c := range chunks {
		if f.opts.FailAfterChunks > 0 && i == f.opts.FailAfterChunks {
			// Abort without finishing the chunked body: the client sees a cut stream.
			panic(http.ErrAbortHandler)
		}
		if i > 0 && !wait(ctx, f.opts.ChunkDelay) {
			f.markCanceled(idx)
			return
		}
		if _, err := w.Write(c); err != nil {
			f.markCanceled(idx)
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func (f *FakeUpstream) status(name string) int {
	if f.opts.Status != 0 {
		return f.opts.Status
	}
	return fixtureStatus(name)
}

func (f *FakeUpstream) record(r *http.Request, body []byte, name string, found bool) int {
	if !found {
		name = ""
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, Request{
		Method:   r.Method,
		Path:     r.URL.Path,
		RawQuery: r.URL.RawQuery,
		Header:   r.Header.Clone(),
		Body:     body,
		Fixture:  name,
	})
	return len(f.reqs) - 1
}

func (f *FakeUpstream) markCanceled(idx int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs[idx].Canceled = true
}

// defaultFixture picks a fixture from the path and the body's stream flag.
func defaultFixture(path string, body []byte) string {
	var req struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(body, &req) // a body that is not JSON counts as stream:false
	switch {
	case strings.HasSuffix(path, openAIChatPath) && req.Stream:
		return "openai/chat-stream-text"
	case strings.HasSuffix(path, openAIChatPath):
		return "openai/chat"
	case strings.HasSuffix(path, anthropicMessagesPath) && req.Stream:
		return "anthropic/messages-stream-text"
	case strings.HasSuffix(path, anthropicMessagesPath):
		return "anthropic/messages"
	}
	return ""
}

// wait sleeps for d, returning false if ctx ends first. d <= 0 returns at once.
func wait(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

package core_test

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

	"github.com/ethosagent/tollgate/internal/fixtures"
)

// This is a trimmed copy of testkit's FakeUpstream (same replay rules, no
// write timing). Rule D3 keeps internal/* tests off testkit, which imports
// the root package (ADR 0016); the recorded data is shared through
// internal/fixtures.

// fixtureHeader selects the fixture the fake replays for one request, as
// testkit.FixtureHeader does.
const fixtureHeader = "x-fixture"

// upstreamOptions shape how a fakeUpstream replays fixtures. The zero value
// replays immediately and completely.
type upstreamOptions struct {
	FirstByteDelay  time.Duration // wait before sending status and headers
	ChunkDelay      time.Duration // wait between chunks (SSE events)
	FailAfterChunks int           // cut the connection after N chunks; 0 never cuts
	Status          int           // override the fixture's status; 0 keeps it
}

// upstreamRequest is one request the fake received.
type upstreamRequest struct {
	Method   string
	Path     string
	RawQuery string
	Header   http.Header
	Body     []byte
	Fixture  string // the fixture chosen for it ("" if none matched)
	Canceled bool   // the client went away before the response finished
}

// fakeUpstream is an httptest server that replays recorded provider responses.
type fakeUpstream struct {
	URL string

	srv  *httptest.Server
	opts upstreamOptions

	mu   sync.Mutex
	reqs []upstreamRequest
}

// newFakeUpstream starts a fake upstream that is closed when the test ends.
// Without a fixtureHeader it picks by path and the body's stream flag.
func newFakeUpstream(t testing.TB, opts upstreamOptions) *fakeUpstream {
	t.Helper()
	f := &fakeUpstream{opts: opts}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	f.URL = f.srv.URL
	t.Cleanup(f.srv.Close)
	return f
}

// Client returns an HTTP client for the fake.
func (f *fakeUpstream) Client() *http.Client { return f.srv.Client() }

// Requests returns a copy of every request received so far, in arrival order.
func (f *fakeUpstream) Requests() []upstreamRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]upstreamRequest(nil), f.reqs...)
}

func (f *fakeUpstream) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	name := r.Header.Get(fixtureHeader)
	if name == "" {
		name = defaultFixture(r.URL.Path, body)
	}
	chunks := fixtures.Chunks(name)
	idx := f.record(r, body, name, chunks != nil)
	if chunks == nil {
		http.Error(w, "fake upstream: no fixture for "+r.URL.Path+" "+name, http.StatusNotFound)
		return
	}
	if !wait(r.Context(), f.opts.FirstByteDelay) {
		f.markCanceled(idx)
		return
	}
	status := fixtures.Status(name)
	if f.opts.Status != 0 {
		status = f.opts.Status
	}
	w.Header().Set("Content-Type", fixtures.ContentType(name))
	w.WriteHeader(status)
	f.writeChunks(r.Context(), w, idx, chunks)
}

func (f *fakeUpstream) writeChunks(ctx context.Context, w http.ResponseWriter, idx int, chunks [][]byte) {
	flusher, _ := w.(http.Flusher)
	for i, c := range chunks {
		if f.opts.FailAfterChunks > 0 && i == f.opts.FailAfterChunks {
			panic(http.ErrAbortHandler) // the client sees a cut stream
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

func (f *fakeUpstream) record(r *http.Request, body []byte, name string, found bool) int {
	if !found {
		name = ""
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, upstreamRequest{Method: r.Method, Path: r.URL.Path, RawQuery: r.URL.RawQuery,
		Header: r.Header.Clone(), Body: body, Fixture: name})
	return len(f.reqs) - 1
}

func (f *fakeUpstream) markCanceled(idx int) {
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
	case strings.HasSuffix(path, fixtures.OpenAIChatPath) && req.Stream:
		return "openai/chat-stream-text"
	case strings.HasSuffix(path, fixtures.OpenAIChatPath):
		return "openai/chat"
	case strings.HasSuffix(path, fixtures.AnthropicMessagesPath) && req.Stream:
		return "anthropic/messages-stream-text"
	case strings.HasSuffix(path, fixtures.AnthropicMessagesPath):
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

package testkit

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ethosagent/tollgate/internal/fixtures"
)

// send sends a request to the fake and returns the response.
func send(t *testing.T, f *FakeUpstream, method, path, fixture, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, f.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if fixture != "" {
		req.Header.Set(FixtureHeader, fixture)
	}
	resp, err := f.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestFakeUpstream_ReplaysEveryResponseFixtureByteExact(t *testing.T) {
	f := NewFakeUpstream(t, Options{})
	for _, name := range FixtureNames() {
		if strings.Contains(name, "/request-") {
			continue // request bodies are inputs, not responses
		}
		t.Run(name, func(t *testing.T) {
			want, err := Fixture(name)
			if err != nil {
				t.Fatal(err)
			}
			resp := send(t, f, http.MethodPost, "/v1/anything", name, `{}`)
			got, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("replayed bytes differ from fixture %s:\n got %q\nwant %q", name, got, want)
			}
		})
	}
}

func TestFakeUpstream_StatusAndContentTypeFromFixtureName(t *testing.T) {
	f := NewFakeUpstream(t, Options{})
	tests := []struct {
		fixture, wantType string
		wantStatus        int
	}{
		{"openai/chat-stream-text", "text/event-stream", 200},
		{"openai/chat", "application/json", 200},
		{"openai/error-429", "application/json", 429},
		{"anthropic/error-401", "application/json", 401},
		{"anthropic/error-500", "application/json", 500},
	}
	for _, tc := range tests {
		t.Run(tc.fixture, func(t *testing.T) {
			resp := send(t, f, http.MethodPost, "/x", tc.fixture, `{}`)
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
			if ct := resp.Header.Get("Content-Type"); ct != tc.wantType {
				t.Fatalf("content-type = %q, want %q", ct, tc.wantType)
			}
		})
	}
}

func TestFakeUpstream_DefaultFixtureByPathAndStreamFlag(t *testing.T) {
	f := NewFakeUpstream(t, Options{})
	tests := []struct {
		path, body, want string
	}{
		{"/v1/chat/completions", `{"stream":true}`, "openai/chat-stream-text"},
		{"/v1/chat/completions", `{"stream":false}`, "openai/chat"},
		{"/v1/chat/completions", `not json`, "openai/chat"},
		{"/v1/messages", `{"stream":true}`, "anthropic/messages-stream-text"},
		{"/v1/messages", `{}`, "anthropic/messages"},
	}
	for _, tc := range tests {
		t.Run(tc.path+" "+tc.body, func(t *testing.T) {
			want, _ := Fixture(tc.want)
			resp := send(t, f, http.MethodPost, tc.path, "", tc.body)
			got, _ := io.ReadAll(resp.Body)
			if !bytes.Equal(got, want) {
				t.Fatalf("got %q, want fixture %s", got, tc.want)
			}
		})
	}
}

func TestFakeUpstream_UnknownFixtureOrPathIs404(t *testing.T) {
	f := NewFakeUpstream(t, Options{})
	for _, tc := range []struct{ path, fixture string }{
		{"/v1/chat/completions", "openai/nope"},
		{"/v1/embeddings", ""},
	} {
		resp := send(t, f, http.MethodPost, tc.path, tc.fixture, `{}`)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s %q: status = %d, want 404", tc.path, tc.fixture, resp.StatusCode)
		}
	}
	got := f.Requests()
	if len(got) != 2 {
		t.Fatalf("recorded %d requests, want both unmatched ones", len(got))
	}
	for i, r := range got {
		if r.Fixture != "" || r.Path == "" {
			t.Errorf("request %d = %+v, want a recorded path and no fixture", i, r)
		}
	}
}

func TestPathFor(t *testing.T) {
	for name, want := range map[string]string{
		"anthropic/messages-stream-text": "/v1/messages",
		"openai/chat":                    "/v1/chat/completions",
	} {
		if got := PathFor(name); got != want {
			t.Errorf("PathFor(%q) = %q, want %q", name, got, want)
		}
		if got := fixtures.Default(want, []byte(`{}`)); !strings.HasPrefix(got, strings.Split(name, "/")[0]+"/") {
			t.Errorf("PathFor(%q) is not served by the same provider (%q)", name, got)
		}
	}
}

func TestFakeUpstream_StatusOverride(t *testing.T) {
	f := NewFakeUpstream(t, Options{Status: http.StatusBadGateway})
	resp := send(t, f, http.MethodPost, "/v1/chat/completions", "openai/chat", `{}`)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
}

func TestFakeUpstream_RecordsRequests(t *testing.T) {
	f := NewFakeUpstream(t, Options{})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, f.URL+"/v1/messages?beta=true", strings.NewReader(`{"stream":false}`))
	req.Header.Set("x-api-key", "test-key-not-real")
	resp, err := f.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	got := f.Requests()
	if len(got) != 1 {
		t.Fatalf("recorded %d requests, want 1", len(got))
	}
	r := got[0]
	if r.Method != http.MethodPost || r.Path != "/v1/messages" || r.RawQuery != "beta=true" {
		t.Fatalf("recorded %s %s?%s", r.Method, r.Path, r.RawQuery)
	}
	if r.Header.Get("x-api-key") != "test-key-not-real" || string(r.Body) != `{"stream":false}` {
		t.Fatalf("recorded header/body wrong: %v %q", r.Header, r.Body)
	}
	if r.Fixture != "anthropic/messages" || r.Canceled {
		t.Fatalf("fixture=%q canceled=%v", r.Fixture, r.Canceled)
	}
}

func TestFakeUpstream_ChunkDelaySpacesEvents(t *testing.T) {
	const delay = 30 * time.Millisecond
	f := NewFakeUpstream(t, Options{ChunkDelay: delay})
	start := time.Now()
	resp := send(t, f, http.MethodPost, "/v1/chat/completions", "openai/chat-stream-text", `{}`)
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatal(err)
	}
	chunks := len(Chunks("openai/chat-stream-text"))
	if atLeast := time.Duration(chunks-1) * delay; time.Since(start) < atLeast {
		t.Fatalf("stream took %v, want at least %v for %d chunks", time.Since(start), atLeast, chunks)
	}
}

func TestFakeUpstream_FirstByteDelay(t *testing.T) {
	const delay = 40 * time.Millisecond
	f := NewFakeUpstream(t, Options{FirstByteDelay: delay})
	start := time.Now()
	send(t, f, http.MethodPost, "/v1/chat/completions", "openai/chat", `{}`)
	if took := time.Since(start); took < delay {
		t.Fatalf("headers after %v, want at least %v", took, delay)
	}
}

func TestFakeUpstream_FailAfterChunksCutsTheStream(t *testing.T) {
	const keep = 2
	f := NewFakeUpstream(t, Options{FailAfterChunks: keep})
	resp := send(t, f, http.MethodPost, "/v1/chat/completions", "openai/chat-stream-text", `{}`)
	got, err := io.ReadAll(resp.Body)
	if err == nil {
		t.Fatal("want a read error after the connection is cut, got none")
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Logf("read error: %v", err) // the exact error depends on the transport; any error proves the cut
	}
	want := bytes.Join(Chunks("openai/chat-stream-text")[:keep], nil)
	if !bytes.Equal(got, want) {
		t.Fatalf("got %q before the cut, want exactly the first %d chunks %q", got, keep, want)
	}
}

func TestFakeUpstream_RecordsClientCancel(t *testing.T) {
	f := NewFakeUpstream(t, Options{ChunkDelay: 200 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, f.URL+"/v1/chat/completions", strings.NewReader(`{"stream":true}`))
	resp, err := f.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1)
	if _, err := resp.Body.Read(buf); err != nil { // the first chunk has arrived
		t.Fatal(err)
	}
	cancel()
	_ = resp.Body.Close()
	waitFor(t, func() bool { r := f.Requests(); return len(r) == 1 && r[0].Canceled })
}

// Fixtures must never carry real credentials or real provider ids.
func TestFixtures_HaveNoRealKeysOrIDs(t *testing.T) {
	keyRe := regexp.MustCompile(`sk-[A-Za-z0-9_-]{16,}|sk-ant-`)
	idRe := regexp.MustCompile(`(chatcmpl-|msg_|toolu_|call_|resp_)[A-Za-z0-9]+`)
	// Field names of the Responses API that merely start like an id.
	fieldNames := map[string]bool{"call_id": true, "call_arguments": true, "call_output": true}
	names := FixtureNames()
	if len(names) < 18 {
		t.Fatalf("found %d fixtures, want at least 18", len(names))
	}
	for _, name := range names {
		data, err := Fixture(name)
		if err != nil {
			t.Fatal(err)
		}
		if keyRe.Match(data) {
			t.Errorf("%s contains something that looks like an API key", name)
		}
		for _, id := range idRe.FindAll(data, -1) {
			if !bytes.Contains(id, []byte("fixture")) && !fieldNames[string(id)] {
				t.Errorf("%s contains id %q; fixture ids must contain \"fixture\"", name, id)
			}
		}
	}
}

func TestChunks_SplitsSSEPerEventAndJSONWhole(t *testing.T) {
	if n := len(Chunks("openai/chat-stream-text")); n != 6 {
		t.Fatalf("openai text stream has %d chunks, want 6", n)
	}
	if n := len(Chunks("anthropic/messages-stream-text")); n != 8 {
		t.Fatalf("anthropic text stream has %d chunks, want 8", n)
	}
	if n := len(Chunks("openai/chat")); n != 1 {
		t.Fatalf("json fixture has %d chunks, want 1", n)
	}
	if Chunks("openai/nope") != nil {
		t.Fatal("unknown fixture must have no chunks")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 3s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

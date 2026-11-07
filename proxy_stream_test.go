package tollgate_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ethosagent/tollgate"
	"github.com/ethosagent/tollgate/testkit"
)

const streamFixture = "openai/chat-stream-text"

func TestProxy_StreamFlushesEveryChunk(t *testing.T) {
	chunks := testkit.Chunks(streamFixture)
	if len(chunks) < 3 {
		t.Fatalf("fixture has %d chunks", len(chunks))
	}
	const delay = 400 * time.Millisecond
	up := testkit.NewFakeUpstream(t, testkit.Options{ChunkDelay: delay})
	base := newProxyServer(t, tollgate.Options{})
	resp := post(t, base, "/openai/v1/chat/completions", []byte(`{"stream":true}`), via(up))
	defer resp.Body.Close()

	arrived := make([]time.Time, 0, 2)
	for _, c := range chunks[:2] {
		buf := make([]byte, len(c))
		if _, err := io.ReadFull(resp.Body, buf); err != nil {
			t.Fatalf("read chunk: %v", err)
		}
		if !bytes.Equal(buf, c) {
			t.Fatalf("chunk = %q, want %q", buf, c)
		}
		arrived = append(arrived, time.Now())
	}
	// A buffering proxy delivers both chunks together; a flushing one
	// delivers the second about one delay after the first.
	if gap := arrived[1].Sub(arrived[0]); gap < delay/2 {
		t.Fatalf("chunk 2 arrived %v after chunk 1; the relay is buffering", gap)
	}
}

func TestProxy_ClientDisconnectCancelsUpstream(t *testing.T) {
	up := testkit.NewFakeUpstream(t, testkit.Options{ChunkDelay: 30 * time.Second})
	base := newProxyServer(t, tollgate.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/openai/v1/chat/completions", strings.NewReader(`{"stream":true}`))
	req.Header.Set("x-upstream-url", up.URL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	first := testkit.Chunks(streamFixture)[0]
	if _, err := io.ReadFull(resp.Body, make([]byte, len(first))); err != nil {
		t.Fatal(err)
	}
	cancel()
	resp.Body.Close()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if reqs := up.Requests(); len(reqs) == 1 && reqs[0].Canceled {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("upstream request was not cancelled after the client disconnected")
}

func TestProxy_UpstreamDropMidStreamEndsWithErrorEvent(t *testing.T) {
	const keep = 2
	tests := []struct {
		name, path, fixture, wantEvent string
	}{
		{"openai", "/openai/v1/chat/completions", streamFixture, `"server_error"`},
		{"anthropic", "/anthropic/v1/messages", "anthropic/messages-stream-text", "event: error\ndata: "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			up := testkit.NewFakeUpstream(t, testkit.Options{FailAfterChunks: keep})
			base := newProxyServer(t, tollgate.Options{})
			resp := post(t, base, tt.path, []byte(`{"stream":true}`), via(up))
			got, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				t.Fatalf("client saw a cut, want a clean end after the error event: %v", err)
			}
			sent := bytes.Join(testkit.Chunks(tt.fixture)[:keep], nil)
			if !bytes.HasPrefix(got, sent) || !bytes.Contains(got[len(sent):], []byte(tt.wantEvent)) {
				t.Fatalf("body = %q, want the first %d chunks then an error event with %q", got, keep, tt.wantEvent)
			}
		})
	}
}

func TestProxy_UpstreamFailures(t *testing.T) {
	up := testkit.NewFakeUpstream(t, testkit.Options{FirstByteDelay: 500 * time.Millisecond})
	tests := []struct {
		name string
		opts tollgate.Options
		hdr  map[string]string
		want int
	}{
		{"first byte timeout is 504", tollgate.Options{Limits: tollgate.Limits{FirstByteTimeout: 50 * time.Millisecond}}, via(up), 504},
		{"unreachable upstream is 502", tollgate.Options{}, map[string]string{"x-upstream-url": "http://127.0.0.1:1"}, 502},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := &syncBuf{}
			tt.opts.Logger = slog.New(slog.NewTextHandler(logs, nil))
			base := newProxyServer(t, tt.opts)
			resp := post(t, base, "/anthropic/v1/messages", []byte(`{}`), tt.hdr)
			if resp.StatusCode != tt.want {
				t.Fatalf("status=%d, want %d; log %s", resp.StatusCode, tt.want, logs.String())
			}
			body := errorBody(t, resp)
			if resp.StatusCode != tt.want || body["type"] != "error" {
				t.Fatalf("status=%d body=%v", resp.StatusCode, body)
			}
		})
	}
}

func TestProxy_LogsMetadataOnly(t *testing.T) {
	up := testkit.NewFakeUpstream(t, testkit.Options{})
	var buf syncBuf
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	base := newProxyServer(t, tollgate.Options{Logger: logger, Auth: allowLocal("acme-corp")})
	const secretKey, secretPrompt = "sk-live-SECRET-KEY", "PROMPT-TEXT-SECRET"
	resp := post(t, base, "/openai/v1/chat/completions?api_key=QUERYSECRET",
		[]byte(`{"messages":[{"role":"user","content":"`+secretPrompt+`"}]}`),
		via(up, "Authorization", "Bearer "+secretKey, "x-api-key", secretKey, "x-proxy-key", "TENANT-SECRET"))
	_ = readAll(t, resp)

	logs := buf.String()
	for _, secret := range []string{secretKey, secretPrompt, "TENANT-SECRET", "QUERYSECRET", "Bearer"} {
		if strings.Contains(logs, secret) {
			t.Errorf("log leaks %q:\n%s", secret, logs)
		}
	}
	var line map[string]any
	if err := json.Unmarshal([]byte(strings.SplitN(logs, "\n", 2)[0]), &line); err != nil {
		t.Fatalf("no JSON log line: %v\n%s", err, logs)
	}
	if line["tenant"] != "acme-corp" || line["provider"] != "openai" || line["status"] != float64(200) {
		t.Errorf("log line = %v", line)
	}
	if _, ok := line["duration_ms"]; !ok {
		t.Errorf("log line has no duration: %v", line)
	}
}

func TestProxy_LogsFailuresWithoutURLs(t *testing.T) {
	var buf syncBuf
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	base := newProxyServer(t, tollgate.Options{Logger: logger})
	resp := post(t, base, "/openai/v1/chat/completions?key=QUERYSECRET", []byte(`{}`), map[string]string{"x-upstream-url": "http://127.0.0.1:1"})
	_ = readAll(t, resp)
	if strings.Contains(buf.String(), "QUERYSECRET") || !strings.Contains(buf.String(), `"status":502`) {
		t.Fatalf("log = %s", buf.String())
	}
}

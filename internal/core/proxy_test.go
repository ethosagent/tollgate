package core_test

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/ethosagent/tollgate/internal/core"
	"github.com/ethosagent/tollgate/internal/fixtures"
)

// responseFixtures are the upstream responses (not the request bodies).
func responseFixtures() []string {
	var out []string
	for _, n := range fixtures.Names() {
		if !strings.Contains(n, "/request-") {
			out = append(out, n)
		}
	}
	return out
}

func requestBody(t testing.TB, fixture string) []byte {
	t.Helper()
	name := "openai/request-chat-stream"
	switch {
	case strings.HasPrefix(fixture, "anthropic/"):
		name = "anthropic/request-messages-stream"
	case fixtures.PathFor(fixture) == fixtures.OpenAIResponsesPath:
		name = "openai/request-responses-stream"
	}
	b, err := fixtures.Bytes(name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestProxy_PassThroughByteExact(t *testing.T) {
	names := responseFixtures()
	if len(names) < 10 {
		t.Fatalf("expected the full fixture set, got %v", names)
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			up := newFakeUpstream(t, upstreamOptions{})
			base := newProxyServer(t, core.Options{})
			want, err := fixtures.Bytes(name)
			if err != nil {
				t.Fatal(err)
			}
			resp := post(t, base, pathFor(name), requestBody(t, name), via(up, fixtureHeader, name))
			got := readAll(t, resp)
			if !bytes.Equal(got, want) {
				t.Fatalf("body differs from fixture\n got %q\nwant %q", got, want)
			}
			wantStatus := http.StatusOK
			if strings.Contains(name, "/error-") {
				wantStatus = map[string]int{"401": 401, "429": 429, "500": 500}[name[strings.LastIndex(name, "-")+1:]]
			}
			if resp.StatusCode != wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, wantStatus)
			}
			wantCT := "application/json"
			if strings.HasSuffix(name, "text") || strings.Contains(name, "stream") {
				wantCT = "text/event-stream"
			}
			if ct := resp.Header.Get("Content-Type"); ct != wantCT {
				t.Fatalf("content-type = %q, want %q", ct, wantCT)
			}
		})
	}
}

func TestProxy_OtherPathsPassThrough(t *testing.T) {
	up := newFakeUpstream(t, upstreamOptions{})
	base := newProxyServer(t, core.Options{})
	resp := post(t, base, "/openai/v1/embeddings?dim=8", []byte(`{"input":"x"}`), via(up, fixtureHeader, "openai/chat"))
	_ = readAll(t, resp)
	reqs := up.Requests()
	if len(reqs) != 1 {
		t.Fatalf("upstream saw %d requests", len(reqs))
	}
	if reqs[0].Path != "/v1/embeddings" || reqs[0].RawQuery != "dim=8" || reqs[0].Method != http.MethodPost {
		t.Fatalf("upstream request = %+v", reqs[0])
	}
	if string(reqs[0].Body) != `{"input":"x"}` {
		t.Fatalf("body = %q", reqs[0].Body)
	}
}

func TestProxy_ForwardsCredentialsAndStripsOwnHeaders(t *testing.T) {
	up := newFakeUpstream(t, upstreamOptions{})
	base := newProxyServer(t, core.Options{})
	resp := post(t, base, "/anthropic/v1/messages", []byte(`{}`), via(up,
		"Authorization", "Bearer sk-verbatim",
		"x-api-key", "ak-verbatim",
		"x-proxy-key", "tenant-secret",
		"x-session-id", "s1",
		"Keep-Alive", "timeout=5",
		"Connection", "X-Custom-Hop",
		"X-Custom-Hop", "1",
		"Proxy-Authorization", "Basic zzz",
		"anthropic-version", "2023-06-01"))
	_ = readAll(t, resp)
	h := up.Requests()[0].Header
	for k, want := range map[string]string{"Authorization": "Bearer sk-verbatim", "X-Api-Key": "ak-verbatim", "Anthropic-Version": "2023-06-01"} {
		if got := h.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	for _, k := range []string{"X-Proxy-Key", "X-Session-Id", "X-Upstream-Url", "Keep-Alive", "X-Custom-Hop", "Proxy-Authorization"} {
		if v := h.Get(k); v != "" {
			t.Errorf("header %s leaked upstream: %q", k, v)
		}
	}
}

func TestProxy_CustomHeaderNamesAreStripped(t *testing.T) {
	up := newFakeUpstream(t, upstreamOptions{})
	base := newProxyServer(t, core.Options{Headers: core.HeaderNames{TenantKey: "x-acme-key", Session: "x-acme-run", UpstreamURL: "x-acme-up"}})
	resp := post(t, base, "/openai/v1/chat/completions", []byte(`{}`), map[string]string{
		"x-acme-key": "k", "x-acme-run": "r", "x-acme-up": up.URL, "x-keep": "yes",
	})
	_ = readAll(t, resp)
	h := up.Requests()[0].Header
	for _, k := range []string{"X-Acme-Key", "X-Acme-Run", "X-Acme-Up"} {
		if h.Get(k) != "" {
			t.Errorf("own header %s leaked upstream", k)
		}
	}
	if h.Get("X-Keep") != "yes" {
		t.Errorf("ordinary header dropped: %v", h)
	}
}

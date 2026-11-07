package concurrency_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ethosagent/tollgate"
	"github.com/ethosagent/tollgate/ext/admission/concurrency"
	"github.com/ethosagent/tollgate/ext/auth/statickeys"
	"github.com/ethosagent/tollgate/testkit"
)

func chat(t *testing.T, base, upstream, key string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, base+"/openai/v1/chat/completions", strings.NewReader(`{"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-proxy-key", key)
	req.Header.Set("x-upstream-url", upstream)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	return resp
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestAdmission_SaturatedTenantDoesNotAffectOther(t *testing.T) {
	up := testkit.NewFakeUpstream(t, testkit.Options{ChunkDelay: 100 * time.Millisecond})
	auth, err := statickeys.New(statickeys.Config{Keys: map[string]statickeys.Tenant{
		"key-a": {ID: "a", AllowedUpstreams: []string{"127.0.0.0/8"}},
		"key-b": {ID: "b", AllowedUpstreams: []string{"127.0.0.0/8"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	adm, err := concurrency.New(concurrency.Config{DefaultMax: 1, RetryAfter: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	p, err := tollgate.New(tollgate.Options{Auth: auth, Admission: adm})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(p.Handler())
	t.Cleanup(srv.Close)

	// Tenant A holds its only slot with a slow stream.
	held := chat(t, srv.URL, up.URL, "key-a")
	defer held.Body.Close()
	waitFor(t, "A's stream to reach the upstream", func() bool { return len(up.Requests()) == 1 })

	over := chat(t, srv.URL, up.URL, "key-a")
	io.Copy(io.Discard, over.Body)
	over.Body.Close()
	if over.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("saturated tenant A: status %d, want 429", over.StatusCode)
	}
	if got := over.Header.Get("Retry-After"); got != "2" {
		t.Fatalf("Retry-After = %q, want 2", got)
	}

	other := chat(t, srv.URL, up.URL, "key-b")
	io.Copy(io.Discard, other.Body)
	other.Body.Close()
	if other.StatusCode != http.StatusOK {
		t.Fatalf("tenant B: status %d, want 200", other.StatusCode)
	}

	if _, err := io.Copy(io.Discard, held.Body); err != nil || held.StatusCode != http.StatusOK {
		t.Fatalf("A's held stream: status %d, err %v", held.StatusCode, err)
	}
}

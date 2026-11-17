package tollgate_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethosagent/tollgate"
	"github.com/ethosagent/tollgate/testkit"
)

// router is a Before hook that swaps the client's model for a cheaper one.
type router struct{ tollgate.NoopHook }

func (router) Name() string { return "router" }

func (router) Before(_ context.Context, c *tollgate.Call) (*tollgate.Response, error) {
	c.Body = bytes.ReplaceAll(c.Body, []byte(`"gpt-big"`), []byte(`"gpt-cheap"`))
	return nil, nil
}

// TestProxy_FallbackResendsTheOriginalRequest: the upstream refuses the
// routed body with a 400 and accepts the client's own; with
// FallbackStatuses {400} the client gets the 200 byte for byte and After
// sees the fallback.
func TestProxy_FallbackResendsTheOriginalRequest(t *testing.T) {
	const fixture = "openai/chat"
	want, err := testkit.Fixture(fixture)
	if err != nil {
		t.Fatal(err)
	}
	original := `{"model":"gpt-big","messages":[{"role":"user","content":"hi"}]}`
	var mu sync.Mutex
	var sent []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		sent = append(sent, string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(b), "gpt-cheap") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"tools need reasoning off"}}`)
			return
		}
		_, _ = w.Write(want)
	}))
	t.Cleanup(up.Close)

	p := newProxy(t, tollgate.Options{Auth: localAuth{}, FallbackStatuses: []int{400}})
	h := &afterHook{}
	p.Use(router{}, h)
	srv := httptest.NewServer(p.Handler())
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/openai"+testkit.PathFor(fixture), strings.NewReader(original))
	req.Header.Set("x-upstream-url", up.URL)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK || !bytes.Equal(got, want) {
		t.Fatalf("status %d, err %v, body %q", resp.StatusCode, err, got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 2 || !strings.Contains(sent[0], "gpt-cheap") || sent[1] != original {
		t.Fatalf("upstream got %q", sent)
	}
	deadline := time.Now().Add(5 * time.Second) // After runs once the handler returns
	for len(h.metrics()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	ms := h.metrics()
	if len(ms) != 1 || !ms[0].Fallback || ms[0].FallbackFromStatus != 400 || ms[0].Status != 200 || ms[0].Finish != tollgate.FinishOK {
		t.Fatalf("metrics = %+v", ms)
	}
}

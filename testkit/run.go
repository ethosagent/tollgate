package testkit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/ethosagent/tollgate"
	"github.com/ethosagent/tollgate/internal/fixtures"
)

// runTimeout bounds every wait inside Run, so a stuck plugin fails the test
// instead of hanging it.
const runTimeout = 10 * time.Second

// sessionHeader names the session every fixture call of a Run belongs to.
const sessionHeader = "x-session-id"

// Config shapes RunWith. Only Plugins and Fixtures are required.
type Config struct {
	// Plugins are the extensions under test. Each may implement any of
	// tollgate.Authenticator, tollgate.Hook, tollgate.Finalizer and
	// tollgate.MetricSink; one value may implement several.
	Plugins []any
	// Fixtures are upstream response names (see FixtureNames), replayed one
	// call each, in order, over one session. A name must be a response, not a
	// "request-" input.
	Fixtures []string
	// Headers are sent with every call, e.g. the tenant key an Authenticator
	// reads. Credential headers are used but never recorded.
	Headers http.Header
	// Upstream shapes the fake upstream (latency, cut streams, status).
	Upstream Options
	// Limits are passed to tollgate.New. Zero fields take their defaults,
	// except MetricFlushInterval, which Run sets to an hour so a MetricSink
	// receives one batch, at the end.
	Limits tollgate.Limits
}

// Run drives one plugin through a real tollgate.New and a FakeUpstream over
// loopback, one call per fixture, and returns everything the plugin received.
// It is RunWith for the common case. The plugin may be a tollgate.Authenticator,
// Hook, Finalizer or MetricSink (or several at once); anything else, or an
// unknown fixture, fails t.
//
// Run is deterministic: calls are made one at a time, and each is waited for
// (by the proxy's own call counter, not a sleep) until its After hook, events
// and metric have been delivered. At the end it closes every session so
// OnSessionClosed runs, then closes the proxy so a MetricSink receives its
// batch. Everything is stopped when Run returns.
func Run(t testing.TB, plugin any, fixtures ...string) *Recording {
	t.Helper()
	return RunWith(t, Config{Plugins: []any{plugin}, Fixtures: fixtures})
}

// RunWith is Run with a Config.
func RunWith(t testing.TB, cfg Config) *Recording {
	t.Helper()
	for _, f := range cfg.Fixtures {
		if !fixtures.IsResponse(f) {
			t.Fatalf("testkit.Run: unknown fixture %q (see FixtureNames; request- inputs are not responses)", f)
		}
	}
	up := NewFakeUpstream(t, cfg.Upstream)
	rec := &recorder{creds: map[string]bool{}}
	w := wirePlugins(t, cfg.Plugins, rec)
	sess := newSessions()
	base, err := url.Parse(up.URL)
	if err != nil {
		t.Fatalf("testkit.Run: upstream URL: %v", err)
	}
	limits := cfg.Limits
	if limits.MetricFlushInterval == 0 {
		limits.MetricFlushInterval = time.Hour
	}
	p, err := tollgate.New(tollgate.Options{Auth: w.auth, Providers: fixedResolver{base}, Sessions: sess,
		Metrics: w.sink, Finalizers: w.finalizers, Limits: limits})
	if err != nil {
		t.Fatalf("testkit.Run: tollgate.New: %v", err)
	}
	p.Use(w.hooks...)
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()
	if err := p.Start(ctx); err != nil {
		t.Fatalf("testkit.Run: Start: %v", err)
	}
	srv := httptest.NewServer(p.Handler())
	t.Cleanup(srv.Close)
	replies := drive(ctx, t, srv, p, cfg)
	if len(w.hooks) > 0 {
		sess.closeAll()
		awaitRun(t, "OnSessionClosed", func() bool { return rec.count(StageSessionClosed) >= sess.emitted() })
	}
	srv.Close() // no call in flight when the queue is flushed
	if err := p.Close(ctx); err != nil {
		t.Fatalf("testkit.Run: Close: %v", err)
	}
	return &Recording{Calls: rec.snapshot(), Replies: replies}
}

// drive sends one call per fixture and waits for each to be fully accounted.
func drive(ctx context.Context, t testing.TB, srv *httptest.Server, p *tollgate.Proxy, cfg Config) []Reply {
	t.Helper()
	admin := p.AdminHandler()
	var replies []Reply
	for i, fx := range cfg.Fixtures {
		r, err := sendFixture(ctx, srv, fx, cfg.Headers)
		if err != nil {
			t.Fatalf("testkit.Run: fixture %q: %v", fx, err)
		}
		replies = append(replies, r)
		awaitRun(t, fmt.Sprintf("call %d (%s) to be accounted", i+1, fx), func() bool { return callsSeen(ctx, admin) >= int64(i+1) })
	}
	return replies
}

// send makes one call for fixture fx and reads the whole response.
func sendFixture(ctx context.Context, srv *httptest.Server, fx string, extra http.Header) (Reply, error) {
	provider, body := RequestFor(fx)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/"+provider+PathFor(fx), bytes.NewReader(body))
	if err != nil {
		return Reply{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(FixtureHeader, fx)
	req.Header.Set(sessionHeader, "testkit-session")
	for k, v := range extra {
		req.Header[http.CanonicalHeaderKey(k)] = v
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		return Reply{}, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return Reply{Fixture: fx, Status: resp.StatusCode, Body: b}, err
}

// callsSeen reads the proxy's own count of finished calls from /statusz.
func callsSeen(ctx context.Context, admin http.Handler) int64 {
	w := httptest.NewRecorder()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "/statusz", nil)
	if err != nil {
		return 0 // cannot happen for a constant method and path; the wait would time out
	}
	admin.ServeHTTP(w, req)
	var doc struct {
		Calls struct {
			Total int64 `json:"total"`
		} `json:"calls"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &doc) // a bad body reads as zero calls and the wait times out
	return doc.Calls.Total
}

// awaitRun polls cond until it holds, failing t after runTimeout. It orders
// nothing: it only waits for work already in flight to land.
func awaitRun(t testing.TB, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(runTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("testkit.Run: timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

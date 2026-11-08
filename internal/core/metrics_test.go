package core_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethosagent/tollgate/internal/core"
	"github.com/ethosagent/tollgate/internal/model"
)

// collectSink records every metric it is written; fail makes Write return an error.
type collectSink struct {
	mu   sync.Mutex
	got  []model.Metric
	size []int
	fail atomic.Bool
}

func (s *collectSink) Write(_ context.Context, b []model.Metric) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.size = append(s.size, len(b))
	if s.fail.Load() {
		return errors.New("sink down")
	}
	s.got = append(s.got, b...)
	return nil
}

func (s *collectSink) metrics() []model.Metric {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]model.Metric(nil), s.got...)
}

func (s *collectSink) batchSizes() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.size...)
}

// stuckSink blocks every Write until released, ignoring its context.
type stuckSink struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newStuckSink() *stuckSink {
	return &stuckSink{entered: make(chan struct{}), release: make(chan struct{})}
}

func (s *stuckSink) Write(context.Context, []model.Metric) error {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return nil
}

// sinkProxy starts a proxy with sink and closes it when the test ends.
func sinkProxy(t *testing.T, sink model.MetricSink, l core.Limits) (*core.Proxy, string) {
	t.Helper()
	if l.MetricFlushInterval == 0 {
		l.MetricFlushInterval = 10 * time.Millisecond
	}
	p, err := core.New(core.Options{Auth: allowLocal("acme"), Metrics: sink, Limits: l})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := p.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return p, serve(t, p)
}

func TestMetrics_SinkReceivesOneMetricPerCallInBatches(t *testing.T) {
	up := newFakeUpstream(t, upstreamOptions{})
	sink := &collectSink{}
	_, base := sinkProxy(t, sink, core.Limits{MetricBatch: 3})
	for range 7 {
		_ = readAll(t, post(t, base, chatPath, []byte(plainRequest), via(up)))
	}
	eventually(t, "7 metrics in the sink", func() bool { return len(sink.metrics()) == 7 })
	seen := map[string]bool{}
	for _, m := range sink.metrics() {
		if seen[m.CallID] || m.Provider != "openai" || m.Finish != model.FinishOK || m.Usage.InputTokens == 0 {
			t.Fatalf("metric = %+v (duplicate or incomplete)", m)
		}
		seen[m.CallID] = true
	}
	for _, n := range sink.batchSizes() {
		if n > 3 {
			t.Fatalf("batch of %d, want <= MetricBatch 3", n)
		}
	}
}

func TestMetrics_CloseFlushesTheQueue(t *testing.T) {
	up := newFakeUpstream(t, upstreamOptions{})
	sink := &collectSink{}
	p, err := core.New(core.Options{Auth: allowLocal("acme"), Metrics: sink, Limits: core.Limits{MetricFlushInterval: time.Hour}})
	if err != nil {
		t.Fatal(err)
	}
	base := serve(t, p)
	for range 4 {
		_ = readAll(t, post(t, base, chatPath, []byte(plainRequest), via(up)))
	}
	admin := adminServer(t, p)
	eventually(t, "the metrics queued", func() bool { return getStatusz(t, admin).Calls.Total == 4 })
	if err := p.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(sink.metrics()); n != 4 {
		t.Fatalf("sink has %d metrics after Close, want 4", n)
	}
	if err := p.Close(context.Background()); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestMetrics_CloseWithoutASinkIsANoOp(t *testing.T) {
	p, err := core.New(core.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestMetrics_NoSinkAndNoAdminMeansNoMetricWork(t *testing.T) {
	p, err := core.New(core.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if p.MetricsWired() {
		t.Fatal("metrics wired with the default DiscardSink and no admin handler")
	}
	_ = p.AdminHandler()
	if !p.MetricsWired() {
		t.Fatal("AdminHandler did not turn the metrics on")
	}
}

func TestMetrics_ReadyzFailsAfterConsecutiveSinkFailuresAndRecovers(t *testing.T) {
	up := newFakeUpstream(t, upstreamOptions{})
	sink := &collectSink{}
	sink.fail.Store(true)
	_, base := sinkProxy(t, sink, core.Limits{MetricBatch: 1})
	status := func() int { return getStatus(t, base+"/readyz") }
	if status() != 200 {
		t.Fatal("not ready before any failure")
	}
	for range 3 {
		_ = readAll(t, post(t, base, chatPath, []byte(plainRequest), via(up)))
	}
	eventually(t, "/readyz 503", func() bool { return status() == 503 })
	if getStatus(t, base+"/healthz") != 200 {
		t.Fatal("healthz must stay 200 while the sink is down")
	}
	sink.fail.Store(false)
	_ = readAll(t, post(t, base, chatPath, []byte(plainRequest), via(up)))
	eventually(t, "/readyz back to 200", func() bool { return status() == 200 })
}

// TestMetrics_StuckSinkNeverSlowsCalls is the milestone demo: a sink that
// blocks forever changes neither the calls' success nor their latency; the
// overflow is dropped and counted.
func TestMetrics_StuckSinkNeverSlowsCalls(t *testing.T) {
	const calls = 60
	up := newFakeUpstream(t, upstreamOptions{})
	run := func(base string) (total time.Duration) {
		start := time.Now()
		for range calls {
			if r := post(t, base, chatPath, []byte(plainRequest), via(up)); r.StatusCode != 200 {
				t.Fatalf("status = %d", r.StatusCode)
			} else {
				_ = readAll(t, r)
			}
		}
		return time.Since(start)
	}
	baseline := run(newProxyServer(t, core.Options{}))

	stuck := newStuckSink()
	p, base := sinkProxy(t, stuck, core.Limits{MetricQueue: 4, MetricBatch: 2})
	withSink := run(base)
	eventually(t, "the sink to be entered", func() bool {
		select {
		case <-stuck.entered:
			return true
		default:
			return false
		}
	})
	eventually(t, "every call's metric offered", func() bool { return p.DroppedMetrics()+int64(p.QueuedMetrics()) >= calls-2 })
	if withSink > 3*baseline+time.Second {
		t.Fatalf("%d calls took %v with a stuck sink vs %v without: the sink slowed calls", calls, withSink, baseline)
	}
	if d := p.DroppedMetrics(); d < calls-4-2 {
		t.Fatalf("dropped = %d, want >= %d (queue 4 + one batch of 2 in the sink)", d, calls-6)
	}
	short, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := p.Close(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close on a stuck sink = %v, want the deadline error", err)
	}
	close(stuck.release) // let the writer end so the test leaves no goroutine
}

func TestMetrics_ReadyzFollowsMetricFailAfter(t *testing.T) {
	up := newFakeUpstream(t, upstreamOptions{})
	sink := &collectSink{}
	sink.fail.Store(true)
	_, base := sinkProxy(t, sink, core.Limits{MetricBatch: 1, MetricFailAfter: 1})
	_ = readAll(t, post(t, base, chatPath, []byte(plainRequest), via(up)))
	eventually(t, "/readyz 503 after one failure", func() bool { return getStatus(t, base+"/readyz") == 503 })
}

// TestMetrics_LifecycleIsExplicit: with a sink, New starts nothing; /readyz
// says so until Start, and Start after Close is refused.
func TestMetrics_LifecycleIsExplicit(t *testing.T) {
	p, err := core.New(core.Options{Auth: allowLocal("acme"), Metrics: &collectSink{}})
	if err != nil {
		t.Fatal(err)
	}
	base := serve(t, p)
	if s := getStatus(t, base+"/readyz"); s != 503 {
		t.Fatalf("/readyz before Start = %d, want 503 (metric writer not started)", s)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := getStatus(t, base+"/readyz"); s != 200 {
		t.Fatalf("/readyz after Start = %d, want 200", s)
	}
	if err := p.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := p.Start(context.Background()); !errors.Is(err, core.ErrClosed) {
		t.Fatalf("Start after Close = %v, want ErrClosed", err)
	}
}

func TestMetrics_StartWithoutASinkDoesNothing(t *testing.T) {
	p, err := core.New(core.Options{Auth: allowLocal("acme")})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := getStatus(t, serve(t, p)+"/readyz"); s != 200 {
		t.Fatalf("/readyz = %d, want 200 with no sink", s)
	}
	closeProxy(t, p)
}

// TestMetrics_ResponseOverMaxResponseCopyIsCountedNotRead: a stream:false
// response is copied for its usage only up to Limits.MaxResponseCopy; a
// larger one reaches the client whole, has no usage and is counted, never
// silently (STR-8).
func TestMetrics_ResponseOverMaxResponseCopyIsCountedNotRead(t *testing.T) {
	up := newFakeUpstream(t, upstreamOptions{})
	sink := &collectSink{}
	p, base := sinkProxy(t, sink, core.Limits{MaxResponseCopy: 16})
	resp := post(t, base, chatPath, []byte(plainRequest), via(up))
	if body := readAll(t, resp); resp.StatusCode != 200 || len(body) <= 16 {
		t.Fatalf("client got %d, %d bytes; want the whole response", resp.StatusCode, len(body))
	}
	eventually(t, "the call's metric", func() bool { return len(sink.metrics()) == 1 })
	if u := sink.metrics()[0].Usage; u != (model.Usage{}) {
		t.Fatalf("usage = %+v, want none: the response was over the copy cap", u)
	}
	if n := getStatusz(t, adminServer(t, p)).Hooks.UsageNotRead; n != 1 {
		t.Fatalf("statusz hooks.usage_not_read = %d, want 1", n)
	}
}

// TestMetrics_UnparsableFrameIsCountedNotSilent: a frame the mapper cannot
// parse still reaches the client byte for byte, and statusz counts it
// (hooks.unparsed_frames) instead of losing its events silently.
func TestMetrics_UnparsableFrameIsCountedNotSilent(t *testing.T) {
	const stream = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: {not json\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, stream)
	}))
	t.Cleanup(up.Close)
	sink := &collectSink{}
	p, base := sinkProxy(t, sink, core.Limits{})
	resp := post(t, base, chatPath, []byte(`{"stream":true}`), map[string]string{"x-upstream-url": up.URL})
	if body := readAll(t, resp); string(body) != stream {
		t.Fatalf("client got %q, want the upstream bytes exactly", body)
	}
	eventually(t, "the call's metric", func() bool { return len(sink.metrics()) == 1 })
	if n := getStatusz(t, adminServer(t, p)).Hooks.UnparsedFrames; n != 1 {
		t.Fatalf("statusz hooks.unparsed_frames = %d, want 1", n)
	}
}

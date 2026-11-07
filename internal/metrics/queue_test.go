package metrics

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ethosagent/tollgate/internal/model"
)

// recSink records every batch it is given; fail makes Write return an error.
type recSink struct {
	mu      sync.Mutex
	batches [][]model.Metric
	fail    bool
}

func (s *recSink) Write(_ context.Context, b []model.Metric) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.batches = append(s.batches, append([]model.Metric(nil), b...))
	if s.fail {
		return errors.New("sink down")
	}
	return nil
}

func (s *recSink) setFail(v bool) {
	s.mu.Lock()
	s.fail = v
	s.mu.Unlock()
}

func (s *recSink) snapshot() (batches, metrics int, maxBatch int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.batches {
		metrics += len(b)
		maxBatch = max(maxBatch, len(b))
	}
	return len(s.batches), metrics, maxBatch
}

// blockSink blocks every Write until release is closed, ignoring its context.
type blockSink struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockSink() *blockSink {
	return &blockSink{entered: make(chan struct{}), release: make(chan struct{})}
}

func (s *blockSink) Write(context.Context, []model.Metric) error {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return nil
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func closeQ(t *testing.T, q *Queue) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := q.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestQueue_BatchesBySizeAndInterval(t *testing.T) {
	tests := []struct {
		name     string
		offers   int
		batch    int
		interval time.Duration
		wantMin  int // batches
		wantMax  int // largest batch
	}{
		{name: "size trigger flushes full batches before the interval", offers: 6, batch: 3, interval: time.Hour, wantMin: 2, wantMax: 3},
		{name: "interval trigger flushes a partial batch", offers: 2, batch: 100, interval: 10 * time.Millisecond, wantMin: 1, wantMax: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &recSink{}
			q := New(context.Background(), Config{Sink: s, Queue: 100, Batch: tt.batch, Interval: tt.interval})
			defer closeQ(t, q)
			for range tt.offers {
				q.Offer(model.Metric{CallID: "x"})
			}
			eventually(t, "all metrics written", func() bool { _, n, _ := s.snapshot(); return n == tt.offers })
			b, _, maxB := s.snapshot()
			if b < tt.wantMin || maxB > tt.wantMax {
				t.Fatalf("batches = %d (want >= %d), largest = %d (want <= %d)", b, tt.wantMin, maxB, tt.wantMax)
			}
		})
	}
}

func TestQueue_CloseFlushesWhatIsQueued(t *testing.T) {
	s := &recSink{}
	q := New(context.Background(), Config{Sink: s, Queue: 100, Batch: 100, Interval: time.Hour})
	for range 7 {
		q.Offer(model.Metric{})
	}
	closeQ(t, q)
	if _, n, _ := s.snapshot(); n != 7 {
		t.Fatalf("sink got %d metrics after Close, want 7", n)
	}
	q.Offer(model.Metric{}) // after Close: dropped and counted, never a panic
	if got := q.Stats().Dropped; got != 1 {
		t.Fatalf("Dropped = %d, want 1 for an offer after Close", got)
	}
	closeQ(t, q) // idempotent
}

func TestQueue_StuckSinkDropsAndCountsWithoutBlocking(t *testing.T) {
	s := newBlockSink()
	q := New(context.Background(), Config{Sink: s, Queue: 4, Batch: 4, Interval: time.Hour})
	defer func() {
		close(s.release)
		closeQ(t, q)
	}()
	for range 4 {
		q.Offer(model.Metric{})
	}
	<-s.entered // the first batch is now stuck in the sink
	start := time.Now()
	for range 1000 {
		q.Offer(model.Metric{})
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("1000 offers took %v against a stuck sink; Offer must not block", d)
	}
	st := q.Stats()
	if st.Dropped != 996 || st.Depth != 4 {
		t.Fatalf("stats = %+v, want 996 dropped and depth 4", st)
	}
}

func TestQueue_CloseGivesUpAtItsDeadlineOnAStuckSink(t *testing.T) {
	s := newBlockSink()
	q := New(context.Background(), Config{Sink: s, Queue: 4, Batch: 4, Interval: time.Hour})
	q.Offer(model.Metric{})
	q.Offer(model.Metric{})
	q.Offer(model.Metric{})
	q.Offer(model.Metric{})
	<-s.entered
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := q.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close = %v, want the context deadline error", err)
	}
	close(s.release) // let the writer finish so no goroutine leaks
	closeQ(t, q)
}

func TestQueue_SinkErrorsAreCountedAndDropTheBatch(t *testing.T) {
	s := &recSink{fail: true}
	q := New(context.Background(), Config{Sink: s, Queue: 10, Batch: 2, Interval: time.Hour})
	defer closeQ(t, q)
	for range 4 {
		q.Offer(model.Metric{})
	}
	eventually(t, "two failed writes", func() bool { return q.Stats().SinkErrors == 2 })
	if st := q.Stats(); st.Dropped != 4 || st.Written != 0 {
		t.Fatalf("stats = %+v, want 4 dropped and 0 written", st)
	}
}

func TestQueue_APanickingSinkIsAnErrorNotACrash(t *testing.T) {
	q := New(context.Background(), Config{Sink: panicSink{}, Queue: 10, Batch: 1, Interval: time.Hour})
	defer closeQ(t, q)
	q.Offer(model.Metric{})
	eventually(t, "the panic counted", func() bool { return q.Stats().SinkErrors == 1 })
}

type panicSink struct{}

func (panicSink) Write(context.Context, []model.Metric) error { panic("boom") }

func TestQueue_ReadyAfterNConsecutiveFailuresAndRecovers(t *testing.T) {
	s := &recSink{fail: true}
	q := New(context.Background(), Config{Sink: s, Queue: 10, Batch: 1, Interval: time.Hour, FailAfter: 3})
	defer closeQ(t, q)
	if !q.Stats().Ready {
		t.Fatal("not ready before any write")
	}
	for i := 1; i <= 3; i++ {
		q.Offer(model.Metric{})
		eventually(t, "failure counted", func() bool { return q.Stats().SinkErrors == int64(i) })
		if want := i < 3; q.Stats().Ready != want {
			t.Fatalf("Ready after %d failures = %v, want %v", i, !want, want)
		}
	}
	s.setFail(false)
	q.Offer(model.Metric{})
	eventually(t, "recovery", func() bool { return q.Stats().Ready })
}

func TestQueue_NotReadyWhileAWriteIsStuckPastItsTimeout(t *testing.T) {
	s := newBlockSink()
	q := New(context.Background(), Config{Sink: s, Queue: 2, Batch: 1, Interval: time.Hour, WriteTimeout: 20 * time.Millisecond})
	defer func() {
		close(s.release)
		closeQ(t, q)
	}()
	q.Offer(model.Metric{})
	<-s.entered
	eventually(t, "not ready", func() bool { return !q.Stats().Ready })
}

func TestQueue_WriteContextCarriesTheTimeout(t *testing.T) {
	got := make(chan time.Duration, 1)
	q := New(context.Background(), Config{Sink: sinkFunc(func(ctx context.Context, _ []model.Metric) error {
		dl, ok := ctx.Deadline()
		if !ok {
			got <- -1
		} else {
			got <- time.Until(dl)
		}
		return nil
	}), Queue: 2, Batch: 1, Interval: time.Hour, WriteTimeout: time.Minute})
	defer closeQ(t, q)
	q.Offer(model.Metric{})
	if d := <-got; d <= 0 || d > time.Minute {
		t.Fatalf("write deadline in %v, want within (0, 1m]", d)
	}
}

type sinkFunc func(context.Context, []model.Metric) error

func (f sinkFunc) Write(ctx context.Context, b []model.Metric) error { return f(ctx, b) }

func TestQueue_ConcurrentOffersAreAllWrittenOrCounted(t *testing.T) {
	s := &recSink{}
	q := New(context.Background(), Config{Sink: s, Queue: 64, Batch: 16, Interval: time.Millisecond})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 500 {
				q.Offer(model.Metric{})
			}
		}()
	}
	wg.Wait()
	closeQ(t, q)
	_, n, _ := s.snapshot()
	if st := q.Stats(); int64(n)+st.Dropped != 4000 || st.Written != int64(n) {
		t.Fatalf("written %d + dropped %d != 4000 (stats %+v)", n, st.Dropped, st)
	}
}

// An Offer can pass the closed check, be descheduled while Close drains and
// the writer exits, and then land in the buffer nobody reads any more. Such
// a straggler must still be counted as dropped ("offered => written or
// dropped"). The test sends into the buffer directly, as that Offer would.
func TestQueue_OfferThatRacesCloseIsCountedDropped(t *testing.T) {
	q := New(context.Background(), Config{Sink: &recSink{}, Queue: 4, Batch: 4, Interval: time.Hour})
	closeQ(t, q)
	q.ch <- model.Metric{CallID: "straggler"}
	if got := q.Stats().Dropped; got != 1 {
		t.Fatalf("Dropped = %d, want 1 for a metric that reached the buffer after the writer ended", got)
	}
}

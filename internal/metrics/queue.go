package metrics

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethosagent/tollgate/internal/model"
)

// Config configures a Queue. Every field is required and positive: the
// proxy fills them from Options and Limits, the one owner of the defaults.
type Config struct {
	Sink         model.MetricSink
	Queue        int           // metrics waiting for the writer
	Batch        int           // largest batch handed to the sink; capped at Queue
	Interval     time.Duration // longest a metric waits before a partial batch is written
	WriteTimeout time.Duration // deadline of one Sink.Write (Limits.MetricWriteTimeout)
	FailAfter    int           // consecutive failed writes that make the queue not ready (Limits.MetricFailAfter)
}

// Stats is a point-in-time view of a Queue.
type Stats struct {
	Depth      int   // metrics waiting right now
	Dropped    int64 // offered but never written: queue full, closed, or in a failed batch
	SinkErrors int64 // failed Sink.Write calls (error, panic, timeout)
	Written    int64 // metrics a Sink.Write accepted
	Started    bool  // the writer was started (Start, or Close)
	Ready      bool  // started, and neither FailAfter consecutive failures nor a write stuck past its timeout
}

// Queue is the bounded buffer between the request path and a MetricSink. Offer
// never blocks; one goroutine batches to the sink. It is safe for concurrent
// use. Its lifecycle is explicit: New allocates it, Start runs the writer,
// Close flushes and stops it. Offers made before Start wait in the bounded
// buffer (full drops and counts, as always).
type Queue struct {
	cfg    Config
	ch     chan model.Metric
	stop   chan struct{}
	done   chan struct{}
	cancel context.CancelFunc // ends every Write's context when Close gives up; set by Start

	startOnce sync.Once
	started   atomic.Bool
	closeOnce sync.Once
	closed    atomic.Bool

	dropped, sinkErrors, written atomic.Int64
	consecutive                  atomic.Int64 // failed writes in a row
	inflight                     atomic.Int64 // unix nanos the running write started, 0 when idle
}

// New returns a Queue that accepts offers but writes nothing until Start.
// It starts no goroutine.
func New(cfg Config) *Queue {
	cfg.Batch = max(1, min(cfg.Batch, cfg.Queue))
	return &Queue{cfg: cfg, ch: make(chan model.Metric, cfg.Queue), stop: make(chan struct{}), done: make(chan struct{}),
		cancel: func() {}}
}

// Start starts the writer goroutine, once; later calls do nothing. ctx's
// values reach every Sink.Write; its cancellation does not stop the writer
// (Close does, after flushing), so a host may pass its signal context.
func (q *Queue) Start(ctx context.Context) {
	q.startOnce.Do(func() {
		wctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		q.cancel = cancel
		q.started.Store(true)
		go q.run(wctx)
	})
}

// Offer enqueues m without blocking. A full or closed queue drops m and
// counts it.
func (q *Queue) Offer(m model.Metric) {
	if q.closed.Load() {
		q.dropped.Add(1)
		return
	}
	select {
	case q.ch <- m:
	default:
		q.dropped.Add(1)
	}
}

// Stats returns the current counters.
// Once the writer has ended, whatever is still buffered (an Offer that raced
// Close) will never be written, so it is counted as dropped here, at read
// time: a straggler that lands later is still counted.
func (q *Queue) Stats() Stats {
	st := Stats{Depth: len(q.ch), Dropped: q.dropped.Load(), SinkErrors: q.sinkErrors.Load(),
		Written: q.written.Load(), Started: q.started.Load(), Ready: q.ready()}
	select {
	case <-q.done:
		st.Dropped += int64(st.Depth)
	default:
	}
	return st
}

func (q *Queue) ready() bool {
	if !q.started.Load() || q.consecutive.Load() >= int64(q.cfg.FailAfter) {
		return false
	}
	since := q.inflight.Load()
	return since == 0 || time.Since(time.Unix(0, since)) <= q.cfg.WriteTimeout
}

// Close stops accepting metrics, writes what is queued and ends the writer.
// A Queue that was never started is started here, with ctx, so what was
// offered is still flushed. If ctx ends first it cancels the writer's
// context, returns ctx's error and leaves the writer to finish as soon as
// the sink returns. It is idempotent.
func (q *Queue) Close(ctx context.Context) error {
	q.Start(ctx)
	q.closeOnce.Do(func() {
		q.closed.Store(true)
		close(q.stop)
	})
	select {
	case <-q.done:
		return nil
	case <-ctx.Done():
		q.giveUp()
		return fmt.Errorf("metrics: flush on close: %w", ctx.Err())
	}
}

func (q *Queue) run(ctx context.Context) {
	defer func() {
		q.giveUp()
		close(q.done) // from here on Stats counts what is left in ch as dropped
	}()
	tick := time.NewTicker(q.cfg.Interval)
	defer tick.Stop()
	batch := make([]model.Metric, 0, q.cfg.Batch)
	for {
		select {
		case m := <-q.ch:
			if batch = append(batch, m); len(batch) >= q.cfg.Batch {
				batch = q.flush(ctx, batch)
			}
		case <-tick.C:
			batch = q.flush(ctx, batch)
		case <-q.stop:
			q.drain(ctx, batch)
			return
		}
	}
}

// drain writes everything still queued, in batches.
func (q *Queue) drain(ctx context.Context, batch []model.Metric) {
	for {
		select {
		case m := <-q.ch:
			if batch = append(batch, m); len(batch) >= q.cfg.Batch {
				batch = q.flush(ctx, batch)
			}
		default:
			q.flush(ctx, batch)
			return
		}
	}
}

// flush writes batch (if any) and returns a fresh empty one. A failed
// batch is dropped and counted; the sink is never retried (it may be slow
// because it is overloaded).
func (q *Queue) flush(ctx context.Context, batch []model.Metric) []model.Metric {
	if len(batch) == 0 {
		return batch
	}
	q.inflight.Store(time.Now().UnixNano())
	err := q.write(ctx, batch)
	q.inflight.Store(0)
	if err != nil {
		q.sinkErrors.Add(1)
		q.dropped.Add(int64(len(batch)))
		q.consecutive.Add(1)
	} else {
		q.written.Add(int64(len(batch)))
		q.consecutive.Store(0)
	}
	return make([]model.Metric, 0, q.cfg.Batch) // a sink may keep the slice it was given
}

// write calls the sink under the write timeout; a panic is an error.
func (q *Queue) write(ctx context.Context, batch []model.Metric) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("metrics: sink panicked: %v", r)
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, q.cfg.WriteTimeout)
	defer cancel()
	return q.cfg.Sink.Write(ctx, batch)
}

func (q *Queue) giveUp() { q.cancel() }

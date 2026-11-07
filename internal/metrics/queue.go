package metrics

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethosagent/tollgate/internal/model"
)

// Defaults for the zero fields of Config.
const (
	DefaultWriteTimeout = 10 * time.Second
	DefaultFailAfter    = 3
)

// Config configures a Queue. Sink, Queue, Batch and Interval are required
// (the proxy fills them from Options and Limits); the rest default.
type Config struct {
	Sink         model.MetricSink
	Queue        int           // metrics waiting for the writer
	Batch        int           // largest batch handed to the sink; capped at Queue
	Interval     time.Duration // longest a metric waits before a partial batch is written
	WriteTimeout time.Duration // deadline of one Sink.Write; default DefaultWriteTimeout
	FailAfter    int           // consecutive failed writes that make the queue not ready; default DefaultFailAfter
}

// Stats is a point-in-time view of a Queue.
type Stats struct {
	Depth      int   // metrics waiting right now
	Dropped    int64 // offered but never written: queue full, closed, or in a failed batch
	SinkErrors int64 // failed Sink.Write calls (error, panic, timeout)
	Written    int64 // metrics a Sink.Write accepted
	Ready      bool  // false after FailAfter consecutive failures or a write stuck past its timeout
}

// Queue is the bounded buffer between the request path and a MetricSink. Offer
// never blocks; one goroutine batches to the sink. It is safe for concurrent
// use. Create it with New and stop it with Close.
type Queue struct {
	cfg    Config
	ch     chan model.Metric
	stop   chan struct{}
	done   chan struct{}
	cancel context.CancelFunc // ends every Write's context when Close gives up

	closeOnce sync.Once
	closed    atomic.Bool

	dropped, sinkErrors, written atomic.Int64
	consecutive                  atomic.Int64 // failed writes in a row
	inflight                     atomic.Int64 // unix nanos the running write started, 0 when idle
}

// New starts the writer goroutine; the caller must Close the Queue. base is
// the parent of every Write's context (use a context that outlives requests).
func New(base context.Context, cfg Config) *Queue {
	cfg.WriteTimeout = durOr(cfg.WriteTimeout, DefaultWriteTimeout)
	if cfg.FailAfter <= 0 {
		cfg.FailAfter = DefaultFailAfter
	}
	cfg.Batch = max(1, min(cfg.Batch, cfg.Queue))
	q := &Queue{cfg: cfg, ch: make(chan model.Metric, cfg.Queue), stop: make(chan struct{}), done: make(chan struct{})}
	ctx, cancel := context.WithCancel(base)
	q.cancel = cancel
	go q.run(ctx)
	return q
}

func durOr(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
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
func (q *Queue) Stats() Stats {
	return Stats{Depth: len(q.ch), Dropped: q.dropped.Load(), SinkErrors: q.sinkErrors.Load(),
		Written: q.written.Load(), Ready: q.ready()}
}

func (q *Queue) ready() bool {
	if q.consecutive.Load() >= int64(q.cfg.FailAfter) {
		return false
	}
	since := q.inflight.Load()
	return since == 0 || time.Since(time.Unix(0, since)) <= q.cfg.WriteTimeout
}

// Close stops accepting metrics, writes what is queued and ends the writer.
// If ctx ends first it cancels the writer's context, returns ctx's error and
// leaves the writer to finish as soon as the sink returns. It is idempotent.
func (q *Queue) Close(ctx context.Context) error {
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
		q.dropped.Add(int64(len(q.ch))) // an offer that raced Close
		q.giveUp()
		close(q.done)
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

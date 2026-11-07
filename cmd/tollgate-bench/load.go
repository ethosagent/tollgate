package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// phase is what one target (the fake directly, or through the proxy) measured.
type phase struct {
	ttfb, total    []time.Duration // one entry per successful measured call
	errors         int             // failed measured calls
	warmup         int             // unmeasured calls that opened the connections
	peakStreams    int64           // most responses open at once
	bytesPerStream float64         // (live memory - baseline) / streams open when it was read; whole process; 0 if not measurable
}

// loader makes the calls of one phase.
type loader struct {
	cfg    config
	tg     target
	body   []byte
	client *http.Client
	open   atomic.Int64
	peak   atomic.Int64
}

// measure runs one warm-up call per session, then -calls calls per session,
// all sessions at once, against tg. Latency covers only the measured calls
// (connections are already open); memory covers both, from a fresh baseline,
// so the connections a stream needs are counted.
func measure(ctx context.Context, cfg config, tg target) (phase, error) {
	tr := &http.Transport{MaxIdleConnsPerHost: cfg.sessions, DisableCompression: true}
	defer tr.CloseIdleConnections()
	l := &loader{cfg: cfg, tg: tg, body: requestBody(cfg.fixture), client: &http.Client{Transport: tr}}
	runtime.GC()
	base := heapInUse()
	stopMem, deepest := sampleMemory(l.open.Load)
	warm := l.fire(ctx, 1)
	res := l.fire(ctx, cfg.calls)
	stopMem()
	if err := ctx.Err(); err != nil {
		return phase{}, err
	}
	p := phase{warmup: cfg.sessions, peakStreams: l.peak.Load(), errors: res.errors + warm.errors}
	p.ttfb, p.total = res.ttfb, res.total
	if mem, streams := deepest(); streams > 0 && mem > base {
		p.bytesPerStream = math.Round(float64(mem-base) / float64(streams))
	}
	return p, firstError(warm, res)
}

// batch is the outcome of one round of calls.
type batch struct {
	ttfb, total []time.Duration
	errors      int
	err         error // the first failure, for the message
}

func firstError(bs ...batch) error {
	for _, b := range bs {
		if b.errors > 0 {
			return fmt.Errorf("%d call(s) failed, first: %w", b.errors, b.err)
		}
	}
	return nil
}

// fire runs n sequential calls in each of cfg.sessions concurrent sessions.
func (l *loader) fire(ctx context.Context, n int) batch {
	parts := make([]batch, l.cfg.sessions)
	var wg sync.WaitGroup
	for s := range parts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range n {
				if ctx.Err() != nil {
					return
				}
				ttfb, total, err := l.call(ctx, "bench-"+strconv.Itoa(s))
				if err != nil {
					parts[s].errors++
					parts[s].err = err
					continue
				}
				parts[s].ttfb = append(parts[s].ttfb, ttfb)
				parts[s].total = append(parts[s].total, total)
			}
		}()
	}
	wg.Wait()
	var out batch
	for _, p := range parts {
		out.ttfb, out.total = append(out.ttfb, p.ttfb...), append(out.total, p.total...)
		if p.errors > 0 {
			out.errors += p.errors
			out.err = p.err
		}
	}
	return out
}

// call makes one streamed call and times the first body byte and the end.
func (l *loader) call(ctx context.Context, session string) (ttfb, total time.Duration, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.tg.url, bytes.NewReader(l.body))
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-session-id", session)
	for k, v := range l.tg.headers {
		req.Header.Set(k, v)
	}
	start := time.Now()
	resp, err := l.client.Do(req)
	if err != nil {
		return 0, 0, err
	}
	l.opened()
	defer func() { _ = resp.Body.Close(); l.open.Add(-1) }()
	var first [1]byte
	if _, err = io.ReadFull(resp.Body, first[:]); err != nil {
		return 0, 0, fmt.Errorf("status %d: %w", resp.StatusCode, err)
	}
	ttfb = time.Since(start)
	if _, err = io.Copy(io.Discard, resp.Body); err != nil {
		return 0, 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return 0, 0, fmt.Errorf("status %d", resp.StatusCode)
	}
	return ttfb, time.Since(start), nil
}

// opened counts one more open response and raises the peak.
func (l *loader) opened() {
	n := l.open.Add(1)
	for {
		old := l.peak.Load()
		if n <= old || l.peak.CompareAndSwap(old, n) {
			return
		}
	}
}

// requestBody is a streamed (or buffered) chat request in the fixture's wire format.
func requestBody(fixture string) []byte {
	stream := !strings.HasSuffix(fixture, "/chat") && !strings.HasSuffix(fixture, "/messages") && !strings.Contains(fixture, "/error-")
	if strings.HasPrefix(fixture, "anthropic/") {
		return fmt.Appendf(nil, `{"model":"claude-sonnet-4-5","max_tokens":256,"stream":%t,"messages":[{"role":"user","content":"Say hello."}]}`, stream)
	}
	return fmt.Appendf(nil, `{"model":"gpt-4.1-mini","stream":%t,"messages":[{"role":"system","content":"You are a coding agent."},{"role":"user","content":"Say hello."}]}`, stream)
}

// heapInUse is live heap plus goroutine stacks, the memory a stream can add.
func heapInUse() uint64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc + m.StackInuse
}

// sampleMemory watches the number of open streams (open) and, each time it
// reaches a new high, collects garbage and reads the live memory, so the
// figure is what the streams hold rather than what is waiting to be freed.
// stop ends the watch; deepest returns the memory read at the highest count
// seen and that count. Streams that open and close between two ticks (5ms)
// are missed, so the figure means most with -chunk-delay holding them open.
func sampleMemory(open func() int64) (stop func(), deepest func() (mem uint64, streams int64)) {
	done := make(chan struct{})
	var wg sync.WaitGroup
	var mem uint64
	var best int64
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(5 * time.Millisecond)
		defer t.Stop()
		for {
			if n := open(); n > best {
				best = n
				runtime.GC()
				mem = heapInUse()
			}
			select {
			case <-done:
				return
			case <-t.C:
			}
		}
	}()
	return func() { close(done); wg.Wait() }, func() (uint64, int64) { return mem, best }
}

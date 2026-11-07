package main

import (
	"context"
	"errors"
	"math"
	"runtime"
	"strconv"
	"sync"
	"time"
)

// minHold is the shortest chunk delay the memory pass uses, so that every
// session's stream is open at the moment memory is read.
const minHold = 100 * time.Millisecond

// memoryPass opens one stream per session at once, first straight at a
// fake upstream and then through tollgate, and reads live memory (after a
// GC) while all of them are open. It runs on its own rig, after the latency
// pass, so its forced collections never fall inside a timed call.
func memoryPass(ctx context.Context, cfg config) (memory, error) {
	hold := max(cfg.chunkDelay, minHold)
	r, err := newRig(ctx, cfg.sessions, hold)
	if err != nil {
		return memory{}, err
	}
	defer r.stop()
	m := memory{HoldChunkDelay: hold.String()}
	var direct, proxied float64
	if direct, m.DirectStreams, err = held(ctx, cfg, r.directTarget(cfg.fixture), "mem-d"); err != nil {
		return m, err
	}
	if proxied, m.ProxyStreams, err = held(ctx, cfg, r.proxyTarget(cfg.fixture), "mem-p"); err != nil {
		return m, err
	}
	m.DirectBytesPerStream, m.ProxyBytesPerStream = direct, proxied
	m.AddedBytesPerStream = proxied - direct
	return m, nil
}

// held runs one call per session against tg and returns live bytes per open
// stream, read when all sessions' streams are open (or as many as opened
// before the first ended), and that count.
func held(ctx context.Context, cfg config, tg target, tag string) (perStream float64, streams int64, err error) {
	l := newLoader(cfg.fixture, cfg.sessions)
	defer l.close()
	settle()
	base := heapInUse()
	var wg sync.WaitGroup
	errs := make([]error, cfg.sessions)
	for s := range cfg.sessions {
		wg.Go(func() { _, errs[s] = l.call(ctx, tg, tag+"-"+strconv.Itoa(s)) })
	}
	mem, streams := l.atPeak(ctx, int64(cfg.sessions))
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return 0, 0, err
	}
	if streams == 0 || mem <= base {
		return 0, streams, nil
	}
	return math.Round(float64(mem-base) / float64(streams)), streams, nil
}

// atPeak waits until want streams are open, or the count stops rising
// because streams have begun to end, then collects garbage and reads live
// memory with that many open.
func (l *loader) atPeak(ctx context.Context, want int64) (mem uint64, streams int64) {
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for ctx.Err() == nil {
		if n := l.open.Load(); n >= want || (n > 0 && n < l.peak.Load()) {
			break
		}
		<-tick.C
	}
	settle()
	return heapInUse(), l.open.Load()
}

// settle collects garbage until what is left is live: sync.Pool victims and
// objects with cleanups or finalizers (closed connections) take a second
// and third cycle.
func settle() {
	for range 3 {
		runtime.GC()
	}
}

// heapInUse is live heap plus goroutine stacks, the memory a stream can add.
func heapInUse() uint64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc + m.StackInuse
}

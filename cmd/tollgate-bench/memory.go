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

// memProxyTag tags the memory pass's proxied calls.
const memProxyTag = "mem-p"

// minHold is the shortest chunk delay the memory pass uses, so that every
// session's stream is open at the moment memory is read.
const minHold = 100 * time.Millisecond

// memoryPass opens one stream per session at once, first straight at a
// fake upstream and then through tollgate, and reads live memory (after a
// GC) while all of them are open. It runs on its own rig, after the latency
// pass, so its forced collections never fall inside a timed call.
func memoryPass(ctx context.Context, cfg config) (memory, error) {
	hold := max(cfg.chunkDelay, minHold)
	r, err := newRig(ctx, cfg, hold)
	if err != nil {
		return memory{}, err
	}
	defer r.stop()
	m := memory{HoldChunkDelay: hold.String()}
	direct, err := held(ctx, cfg, r.directTarget(cfg.fixture), "mem-d")
	if err != nil {
		return m, err
	}
	proxied, err := held(ctx, cfg, r.proxyTarget(cfg.fixture), memProxyTag)
	if err != nil {
		return m, err
	}
	m.DirectStreams, m.ProxyStreams = direct.streams, proxied.streams
	m.DirectBytesPerStream, m.ProxyBytesPerStream = direct.bytes, proxied.bytes
	m.AddedBytesPerStream = proxied.bytes - direct.bytes
	m.DirectGoroutinesPerStream, m.ProxyGoroutinesPerStream = direct.goroutines, proxied.goroutines
	m.AddedGoroutinesPerStream = proxied.goroutines - direct.goroutines
	return m, nil
}

// perStream is what one open stream holds, process-wide.
type perStream struct {
	bytes, goroutines float64
	streams           int64
}

// held runs one call per session against tg and returns live bytes and
// goroutines per open stream, read when all sessions' streams are open (or as
// many as opened before the first ended), and that count.
func held(ctx context.Context, cfg config, tg target, tag string) (perStream, error) {
	l := newLoader(cfg.fixture, cfg.sessions)
	defer l.close()
	settle()
	base, baseG := heapInUse(), runtime.NumGoroutine()
	var wg sync.WaitGroup
	errs := make([]error, cfg.sessions)
	for s := range cfg.sessions {
		wg.Go(func() { _, errs[s] = l.call(ctx, tg, tag+"-"+strconv.Itoa(s)) })
	}
	mem, gs, streams := l.atPeak(ctx, int64(cfg.sessions))
	var perr error
	if tag == memProxyTag {
		perr = writeProfile("heap", cfg.prof.heap)
	}
	wg.Wait()
	if err := errors.Join(append(errs, perr)...); err != nil {
		return perStream{}, err
	}
	out := perStream{streams: streams}
	if streams == 0 {
		return out, nil
	}
	// Each session's own goroutine (wg.Go) is the client's, on both paths.
	out.goroutines = math.Round(100*float64(gs-baseG)/float64(streams)) / 100
	if mem > base {
		out.bytes = math.Round(float64(mem-base) / float64(streams))
	}
	return out, nil
}

// atPeak waits until want streams are open, or the count stops rising
// because streams have begun to end, then collects garbage and reads live
// memory with that many open.
func (l *loader) atPeak(ctx context.Context, want int64) (mem uint64, goroutines int, streams int64) {
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for ctx.Err() == nil {
		if n := l.open.Load(); n >= want || (n > 0 && n < l.peak.Load()) {
			break
		}
		<-tick.C
	}
	settle()
	return heapInUse(), runtime.NumGoroutine(), l.open.Load()
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

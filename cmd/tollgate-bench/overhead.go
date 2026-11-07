package main

import (
	"fmt"
	"time"

	"github.com/ethosagent/tollgate/testkit"
)

// timings are the per-call figures of one latency pass. All stamps come from
// one process's monotonic clock, so client- and upstream-side stamps compare.
type timings struct {
	directTTFB, directTotal []time.Duration // end to end, client side
	proxyTTFB, proxyTotal   []time.Duration
	ttfbAdded               []time.Duration // per pair: proxied minus direct time to first byte, upstream delays excluded
	chunkAdded              []time.Duration // per pair and chunk: proxied minus direct upstream-write-to-client time
	forward                 []time.Duration // per proxied call: proxy handler entered -> upstream got the request
}

// leg is one call's time on the wire and in anything between client and
// fake, without the fake's own delays: request sent -> fake got it, and,
// per chunk, fake wrote it -> client had it.
type leg struct {
	in  time.Duration
	out []time.Duration
}

func legOf(s sample, up testkit.Request) (leg, error) {
	if up.Received.IsZero() || len(up.ChunksWritten) != len(s.chunks) {
		return leg{}, fmt.Errorf("%s: upstream recorded %d chunk writes, client got %d chunks", s.id, len(up.ChunksWritten), len(s.chunks))
	}
	l := leg{in: up.Received.Sub(s.start), out: make([]time.Duration, len(s.chunks))}
	for i, got := range s.chunks {
		l.out[i] = got.Sub(up.ChunksWritten[i])
	}
	return l, nil
}

// analyse pairs every call with the fake's record of it. The proxy's added
// time is the proxied leg minus the direct leg of the same pair: the fake's
// chunk delays and scheduling noise between chunks fall out of both.
func analyse(pairs []pair, up map[string]testkit.Request, arrival func(id string) (time.Time, bool)) (timings, error) {
	var t timings
	for _, p := range pairs {
		dl, err := legOf(p.direct, up[p.direct.id])
		if err != nil {
			return t, err
		}
		pl, err := legOf(p.proxy, up[p.proxy.id])
		if err != nil {
			return t, err
		}
		t.ttfbAdded = append(t.ttfbAdded, (pl.in+pl.out[0])-(dl.in+dl.out[0]))
		for i := range pl.out {
			t.chunkAdded = append(t.chunkAdded, pl.out[i]-dl.out[i])
		}
		if at, ok := arrival(p.proxy.id); ok {
			t.forward = append(t.forward, up[p.proxy.id].Received.Sub(at))
		}
		t.directTTFB, t.directTotal = appendE2E(t.directTTFB, t.directTotal, p.direct)
		t.proxyTTFB, t.proxyTotal = appendE2E(t.proxyTTFB, t.proxyTotal, p.proxy)
	}
	return t, nil
}

func appendE2E(ttfb, total []time.Duration, s sample) ([]time.Duration, []time.Duration) {
	return append(ttfb, s.chunks[0].Sub(s.start)), append(total, s.chunks[len(s.chunks)-1].Sub(s.start))
}

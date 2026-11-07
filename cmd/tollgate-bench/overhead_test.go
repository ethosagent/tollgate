package main

import (
	"strings"
	"testing"
	"time"

	"github.com/ethosagent/tollgate/testkit"
)

// at is a stamp n microseconds after a fixed origin.
func at(n int) time.Time { return time.Unix(0, 0).Add(time.Duration(n) * time.Microsecond) }

func stamps(ns ...int) []time.Time {
	out := make([]time.Time, len(ns))
	for i, n := range ns {
		out[i] = at(n)
	}
	return out
}

func TestAnalyse_SubtractsTheDirectLegAndIgnoresUpstreamDelays(t *testing.T) {
	// Direct: 100us to reach the fake, 50us per chunk back. Proxied: 400us to
	// reach the fake, 80us per chunk back. The fake waits 10ms between chunks
	// on one path and 20ms on the other: none of that may show.
	p := pair{
		direct: sample{id: "d", start: at(0), chunks: stamps(150, 10_150)},
		proxy:  sample{id: "p", start: at(0), chunks: stamps(480, 20_480)},
	}
	up := map[string]testkit.Request{
		"d": {Received: at(100), ChunksWritten: stamps(100, 10_100)},
		"p": {Received: at(400), ChunksWritten: stamps(400, 20_400)},
	}
	arrival := func(id string) (time.Time, bool) { return at(250), id == "p" }
	got, err := analyse([]pair{p}, up, arrival)
	if err != nil {
		t.Fatal(err)
	}
	us := time.Microsecond
	if want := (400*us + 80*us) - (100*us + 50*us); got.ttfbAdded[0] != want {
		t.Errorf("ttfb added = %v, want %v", got.ttfbAdded[0], want)
	}
	for i, c := range got.chunkAdded {
		if c != 30*us {
			t.Errorf("chunk %d added = %v, want 30us", i, c)
		}
	}
	if len(got.forward) != 1 || got.forward[0] != 150*us {
		t.Errorf("forward = %v, want [150us]", got.forward)
	}
	if got.proxyTotal[0] != 20_480*us || got.directTTFB[0] != 150*us {
		t.Errorf("end to end: proxy total %v, direct ttfb %v", got.proxyTotal[0], got.directTTFB[0])
	}
}

func TestAnalyse_FailsWhenTheUpstreamRecordDoesNotMatch(t *testing.T) {
	p := pair{
		direct: sample{id: "d", start: at(0), chunks: stamps(1, 2)},
		proxy:  sample{id: "p", start: at(0), chunks: stamps(1, 2)},
	}
	tests := []struct {
		name string
		up   map[string]testkit.Request
	}{
		{"no record of the direct call", map[string]testkit.Request{"p": {Received: at(0), ChunksWritten: stamps(0, 1)}}},
		{"the proxied call lost a chunk", map[string]testkit.Request{
			"d": {Received: at(0), ChunksWritten: stamps(0, 1)}, "p": {Received: at(0), ChunksWritten: stamps(0)}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := analyse([]pair{p}, tt.up, func(string) (time.Time, bool) { return time.Time{}, false })
			if err == nil || !strings.Contains(err.Error(), "upstream recorded") {
				t.Fatalf("err = %v, want a mismatch error", err)
			}
		})
	}
}

func TestOverheadOf_JudgesTheV1Target(t *testing.T) {
	tests := []struct {
		name         string
		ttfb, chunks []time.Duration
		want         bool
	}{
		{"both p99 under 1ms", []time.Duration{200 * time.Microsecond}, []time.Duration{50 * time.Microsecond}, true},
		{"chunk p99 at 1ms", []time.Duration{200 * time.Microsecond}, []time.Duration{time.Millisecond}, false},
		{"no samples", nil, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := overheadOf(timings{ttfbAdded: tt.ttfb, chunkAdded: tt.chunks}).MeetsV1; got != tt.want {
				t.Fatalf("MeetsV1 = %t, want %t", got, tt.want)
			}
		})
	}
}

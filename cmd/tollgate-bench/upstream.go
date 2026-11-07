package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ethosagent/tollgate/testkit"
)

// syncProbes is how many clock reads sync takes; the one with the shortest
// round trip bounds the mapping error.
const syncProbes = 16

// upstream is the fake the rig measures against.
type upstream interface {
	url() string
	// records returns, by call id, what the fake recorded for each call, on
	// this process's monotonic clock, and the bound on that mapping's error.
	records(ctx context.Context) (map[string]testkit.Request, time.Duration, error)
	stop()
}

// fakeClient reaches a testkit fake plus its control server (records,
// clock). The fake runs in this process by default or in another one
// (-serve-fake, then -upstream) so its goroutines, GC and scheduler work do
// not share a runtime with the client and the proxy; both are reached the
// same way.
type fakeClient struct {
	base   string // the fake
	port   int    // its control server's, on loopback
	client *http.Client
	close  func() // stops an in-process fake; nil for another process's
}

// connectUpstream dials the fake of another process (spec, the line it
// printed; its chunk delay is the one it was started with) or, with no spec,
// starts one in this process.
func connectUpstream(ctx context.Context, spec string, chunkDelay time.Duration) (upstream, error) {
	if spec != "" {
		return dialUpstream(spec)
	}
	return startLocalUpstream(ctx, chunkDelay)
}

// startLocalUpstream runs the fake in this process; it is reached exactly as
// another process's would be.
func startLocalUpstream(ctx context.Context, chunkDelay time.Duration) (*fakeClient, error) {
	f, err := startFake(ctx, chunkDelay)
	if err != nil {
		return nil, err
	}
	u := newUpstream(f.fake.URL, f.port)
	u.close = f.stop
	return u, nil
}

// dialUpstream parses an address line ("<fake url> <control port>",
// optionally prefixed with FAKE) as -serve-fake prints it. The control server
// is always on loopback, so only its port crosses over.
func dialUpstream(spec string) (*fakeClient, error) {
	f := strings.Fields(strings.TrimPrefix(strings.TrimSpace(spec), "FAKE"))
	if len(f) != 2 {
		return nil, fmt.Errorf("-upstream %q: want \"<fake url> <control port>\" as -serve-fake prints it", spec)
	}
	if u, err := url.Parse(f[0]); err != nil || u.Host == "" {
		return nil, fmt.Errorf("-upstream: bad URL %q", f[0])
	}
	port, err := strconv.ParseUint(f[1], 10, 16)
	if err != nil || port == 0 {
		return nil, fmt.Errorf("-upstream: bad control port %q", f[1])
	}
	return newUpstream(f[0], int(port)), nil
}

func newUpstream(base string, controlPort int) *fakeClient {
	return &fakeClient{base: base, port: controlPort, client: &http.Client{Timeout: 30 * time.Second}}
}

// control is the URL of one of the control server's endpoints.
func (u *fakeClient) control(endpoint string) string {
	return "http://127.0.0.1:" + strconv.Itoa(u.port) + "/" + endpoint
}

func (u *fakeClient) url() string { return u.base }

// stop ends an in-process fake; another process's keeps running, since
// whoever started it ends it.
func (u *fakeClient) stop() {
	u.client.CloseIdleConnections()
	if u.close != nil {
		u.close()
	}
}

// records returns, by call id, what the fake recorded for each call, with
// its stamps mapped onto this process's monotonic clock (see sync); bound
// is the mapping's worst-case error.
func (u *fakeClient) records(ctx context.Context) (out map[string]testkit.Request, bound time.Duration, err error) {
	origin, bound, err := u.sync(ctx)
	if err != nil {
		return nil, 0, err
	}
	recs, err := u.readRecords(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("fake records: %w", err)
	}
	out = make(map[string]testkit.Request, len(recs))
	for _, r := range recs {
		q := testkit.Request{Received: origin.Add(time.Duration(r.Received))}
		for _, c := range r.Chunks {
			q.ChunksWritten = append(q.ChunksWritten, origin.Add(time.Duration(c)))
		}
		out[r.ID] = q
	}
	return out, bound, nil
}

// sync finds the fake's clock origin on this process's monotonic clock. The
// fake stamps durations since its own origin (monotonic, so no NTP step can
// move them); each probe reads the fake's clock between two local reads and
// places it at their midpoint. The probe with the shortest round trip wins,
// and half of that round trip bounds the error. Both clocks tick at the same
// rate (one host), so one offset holds for the whole run.
func (u *fakeClient) sync(ctx context.Context) (origin time.Time, bound time.Duration, err error) {
	best := time.Duration(math.MaxInt64)
	for range syncProbes {
		t0 := time.Now()
		now, err := u.readClock(ctx)
		if err != nil {
			return time.Time{}, 0, fmt.Errorf("fake clock: %w", err)
		}
		if rtt := time.Since(t0); rtt < best {
			best, origin = rtt, t0.Add(rtt/2-time.Duration(now))
		}
	}
	return origin, best / 2, nil
}

// readClock returns the fake's clock: nanoseconds since its origin.
func (u *fakeClient) readClock(ctx context.Context) (now int64, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.control("clock"), nil)
	if err != nil {
		return 0, err
	}
	return now, u.decode(req, &now)
}

// readRecords returns what the fake recorded, on its own clock.
func (u *fakeClient) readRecords(ctx context.Context) (recs []record, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.control("records"), nil)
	if err != nil {
		return nil, err
	}
	return recs, u.decode(req, &recs)
}

func (u *fakeClient) decode(req *http.Request, v any) error {
	resp, err := u.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}

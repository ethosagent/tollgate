package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ethosagent/tollgate/testkit"
)

// upstream is the fake the rig measures against: in this process, or in
// another one (-serve-fake, then -upstream) so its goroutines, GC and
// scheduler work do not share a runtime with the client and the proxy.
type upstream interface {
	url() string
	// records returns, by call id, what the fake recorded for each call.
	records(ctx context.Context) (map[string]testkit.Request, error)
	stop()
}

// localUpstream is a testkit fake in this process.
type localUpstream struct {
	fake  *testkit.FakeUpstream
	close func()
}

func newLocalUpstream(chunkDelay time.Duration) *localUpstream {
	fake, stop := testkit.NewServer(testkit.Options{ChunkDelay: chunkDelay})
	return &localUpstream{fake: fake, close: stop}
}

func (u *localUpstream) url() string { return u.fake.URL }
func (u *localUpstream) stop()       { u.close() }

func (u *localUpstream) records(context.Context) (map[string]testkit.Request, error) {
	return byCallID(u.fake.Requests()), nil
}

func byCallID(reqs []testkit.Request) map[string]testkit.Request {
	out := make(map[string]testkit.Request, len(reqs))
	for _, q := range reqs {
		out[q.Header.Get(callHeader)] = q
	}
	return out
}

// record is one fake-side call on the wire between the processes. Stamps
// are wall-clock Unix nanoseconds: both processes read the same host clock.
type record struct {
	ID       string  `json:"id"`
	Received int64   `json:"received"`
	Chunks   []int64 `json:"chunks"`
}

// remoteUpstream is a fake in another process: one started with
// -serve-fake, whose address line is passed as -upstream.
type remoteUpstream struct {
	base, recs string
	client     *http.Client
}

// dialRemoteUpstream parses an address line ("<fake url> <records url>",
// optionally prefixed with FAKE) as -serve-fake prints it.
func dialRemoteUpstream(spec string) (*remoteUpstream, error) {
	f := strings.Fields(strings.TrimPrefix(strings.TrimSpace(spec), "FAKE"))
	if len(f) != 2 {
		return nil, fmt.Errorf("-upstream %q: want \"<fake url> <records url>\" as -serve-fake prints it", spec)
	}
	for _, raw := range f {
		if u, err := url.Parse(raw); err != nil || u.Host == "" {
			return nil, fmt.Errorf("-upstream: bad URL %q", raw)
		}
	}
	return &remoteUpstream{base: f[0], recs: f[1], client: &http.Client{Timeout: 30 * time.Second}}, nil
}

func (u *remoteUpstream) url() string { return u.base }

// stop leaves the other process running: whoever started it ends it.
func (u *remoteUpstream) stop() { u.client.CloseIdleConnections() }

func (u *remoteUpstream) records(ctx context.Context) (map[string]testkit.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.recs, nil)
	if err != nil {
		return nil, err
	}
	resp, err := u.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var recs []record
	if err := json.NewDecoder(resp.Body).Decode(&recs); err != nil {
		return nil, fmt.Errorf("fake records: %w", err)
	}
	out := make(map[string]testkit.Request, len(recs))
	for _, r := range recs {
		q := testkit.Request{Received: time.Unix(0, r.Received)}
		for _, c := range r.Chunks {
			q.ChunksWritten = append(q.ChunksWritten, time.Unix(0, c))
		}
		out[r.ID] = q
	}
	return out, nil
}

// serveFake is the -serve-fake process: a testkit fake plus a records
// endpoint on loopback. It prints "FAKE <fake url> <records url>" (the value
// for -upstream) and serves until ctx ends (Ctrl-C).
func serveFake(ctx context.Context, chunkDelay time.Duration, out io.Writer) error {
	fake, stop := testkit.NewServer(testkit.Options{ChunkDelay: chunkDelay})
	defer stop()
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(recordsOf(fake.Requests()))
	}), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }() // ends with srv.Close below
	defer srv.Close()
	if _, err := fmt.Fprintf(out, "FAKE %s http://%s/records\n", fake.URL, ln.Addr()); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

func recordsOf(reqs []testkit.Request) []record {
	out := make([]record, 0, len(reqs))
	for _, q := range reqs {
		r := record{ID: q.Header.Get(callHeader), Received: q.Received.UnixNano()}
		for _, c := range q.ChunksWritten {
			r.Chunks = append(r.Chunks, c.UnixNano())
		}
		out = append(out, r)
	}
	return out
}

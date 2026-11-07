package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/ethosagent/tollgate/testkit"
)

// record is one fake-side call on the wire between fake and rig. Stamps are
// nanoseconds since the fake's origin, read on its monotonic clock.
type record struct {
	ID       string  `json:"id"`
	Received int64   `json:"received"`
	Chunks   []int64 `json:"chunks"`
}

// fakeServer is a testkit fake plus its control server on loopback.
type fakeServer struct {
	fake     *testkit.FakeUpstream
	stopFake func()
	srv      *http.Server
	port     int // of the control server, on 127.0.0.1
	origin   time.Time
}

func startFake(ctx context.Context, chunkDelay time.Duration) (*fakeServer, error) {
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	f := &fakeServer{port: ln.Addr().(*net.TCPAddr).Port, origin: time.Now()}
	f.fake, f.stopFake = testkit.NewServer(testkit.Options{ChunkDelay: chunkDelay})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /clock", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(int64(time.Since(f.origin)))
	})
	mux.HandleFunc("GET /records", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(f.records())
	})
	f.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = f.srv.Serve(ln) }() // ends with srv.Close in stop
	return f, nil
}

// line is the address line -serve-fake prints and -upstream takes.
func (f *fakeServer) line() string { return fmt.Sprintf("FAKE %s %d", f.fake.URL, f.port) }

func (f *fakeServer) stop() {
	_ = f.srv.Close()
	f.stopFake()
}

func (f *fakeServer) records() []record {
	reqs := f.fake.Requests()
	out := make([]record, 0, len(reqs))
	for _, q := range reqs {
		r := record{ID: q.Header.Get(callHeader), Received: int64(q.Received.Sub(f.origin))}
		for _, c := range q.ChunksWritten {
			r.Chunks = append(r.Chunks, int64(c.Sub(f.origin)))
		}
		out = append(out, r)
	}
	return out
}

// serveFake is the -serve-fake process: it prints the address line (the
// value for -upstream) and serves until ctx ends (Ctrl-C).
func serveFake(ctx context.Context, chunkDelay time.Duration, out io.Writer) error {
	f, err := startFake(ctx, chunkDelay)
	if err != nil {
		return err
	}
	defer f.stop()
	if _, err := fmt.Fprintln(out, f.line()); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

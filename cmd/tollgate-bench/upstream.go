package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/ethosagent/tollgate/testkit"
)

// upstream is the fake the rig measures against: in this process, or in a
// child process (-upstream-proc) so its goroutines, GC and scheduler work do
// not share a runtime with the client and the proxy.
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

// remoteUpstream is a fake in a child process started by this binary.
type remoteUpstream struct {
	cmd        *exec.Cmd
	stdin      io.Closer
	base, recs string
	client     *http.Client
}

// startRemoteUpstream re-executes this binary in fake mode with GOMAXPROCS
// procs (0 leaves the runtime default) and waits for its address line.
func startRemoteUpstream(ctx context.Context, chunkDelay time.Duration, procs int) (*remoteUpstream, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, exe, "-serve-fake", "-chunk-delay", chunkDelay.String())
	cmd.Env = os.Environ()
	if procs > 0 {
		cmd.Env = append(cmd.Env, "GOMAXPROCS="+strconv.Itoa(procs))
	}
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	u := &remoteUpstream{cmd: cmd, stdin: stdin, client: &http.Client{Timeout: 30 * time.Second}}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if f := strings.Fields(line); err == nil && len(f) == 3 && f[0] == "FAKE" {
		u.base, u.recs = f[1], f[2]
		return u, nil
	}
	u.stop()
	return nil, fmt.Errorf("fake upstream process: bad address line %q: %w", line, err)
}

func (u *remoteUpstream) url() string { return u.base }

// stop closes the child's stdin, which ends it, and reaps it.
func (u *remoteUpstream) stop() {
	_ = u.stdin.Close()
	_ = u.cmd.Wait()
}

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

// serveFake is the child process: a testkit fake plus a records endpoint on
// loopback. It prints "FAKE <fake url> <records url>" and serves until its
// stdin closes.
func serveFake(ctx context.Context, chunkDelay time.Duration, stdin io.Reader, out io.Writer) error {
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
	_, err = io.Copy(io.Discard, stdin) // the parent closes stdin (or dies) to end the child
	if errors.Is(err, os.ErrClosed) {
		err = nil
	}
	return err
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

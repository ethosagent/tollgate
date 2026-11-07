package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethosagent/tollgate/testkit"
)

// sample is one call as the client saw it.
type sample struct {
	id     string      // callHeader value, unique per call
	start  time.Time   // just before the request was sent
	chunks []time.Time // when each fixture chunk had fully arrived
}

// pair is one direct and one proxied call made back to back by one session.
type pair struct{ direct, proxy sample }

// loader makes calls for one fixture and counts the streams open at once.
type loader struct {
	fixture string
	body    []byte
	want    [][]byte // the fixture's chunks: what every call must return
	client  *http.Client
	open    atomic.Int64 // calls that have their first chunk and are not done
	peak    atomic.Int64
}

func newLoader(fixture string, sessions int) *loader {
	_, body := testkit.RequestFor(fixture)
	tr := &http.Transport{MaxIdleConnsPerHost: 2 * sessions, DisableCompression: true}
	return &loader{fixture: fixture, body: body, want: testkit.Chunks(fixture), client: &http.Client{Transport: tr}}
}

func (l *loader) close() { l.client.CloseIdleConnections() }

// pairs runs, in each of sessions concurrent sessions, n rounds of one direct
// and one proxied call on that session's warm connections. Odd rounds go
// proxy first, so neither path always runs on the other's heels.
func (l *loader) pairs(ctx context.Context, r *rig, sessions, n int, tag string) ([]pair, error) {
	var (
		mu   sync.Mutex
		out  []pair
		errs []error
		wg   sync.WaitGroup
	)
	direct, proxied := r.directTarget(l.fixture), r.proxyTarget(l.fixture)
	for s := range sessions {
		wg.Go(func() {
			for k := 0; k < n && ctx.Err() == nil; k++ {
				id := fmt.Sprintf("%s-s%d-r%d", tag, s, k)
				p, err := l.pair(ctx, direct, proxied, id, k%2 == 1)
				mu.Lock()
				if err != nil {
					errs = append(errs, err)
				} else {
					out = append(out, p)
				}
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(errs) > 0 {
		return out, fmt.Errorf("%d call(s) failed, first: %w", len(errs), errs[0])
	}
	return out, nil
}

func (l *loader) pair(ctx context.Context, direct, proxied target, id string, proxyFirst bool) (pair, error) {
	var p pair
	first, second := &p.direct, &p.proxy
	ft, st := direct, proxied
	if proxyFirst {
		first, second, ft, st = second, first, st, ft
	}
	var err1, err2 error
	*first, err1 = l.call(ctx, ft, id+"-"+tagOf(ft, proxied))
	*second, err2 = l.call(ctx, st, id+"-"+tagOf(st, proxied))
	return p, errors.Join(err1, err2)
}

func tagOf(t, proxied target) string {
	if t.url == proxied.url {
		return "p"
	}
	return "d"
}

// call makes one call, stamping the arrival of each fixture chunk, and
// checks the body is the fixture byte for byte.
func (l *loader) call(ctx context.Context, tg target, id string) (sample, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tg.url, bytes.NewReader(l.body))
	if err != nil {
		return sample{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-session-id", id)
	req.Header.Set(callHeader, id)
	for k, v := range tg.headers {
		req.Header.Set(k, v)
	}
	s := sample{id: id, start: time.Now(), chunks: make([]time.Time, 0, len(l.want))}
	resp, err := l.client.Do(req)
	if err != nil {
		return s, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return s, fmt.Errorf("%s: status %d", id, resp.StatusCode)
	}
	return s, l.read(resp.Body, &s)
}

// read consumes the body chunk by chunk, as the fixture was written. The
// call counts as an open stream from its first chunk to its end.
func (l *loader) read(body io.Reader, s *sample) error {
	buf := make([]byte, 0, 4096)
	if err := l.readChunk(body, s, &buf, 0); err != nil {
		return err
	}
	l.opened()
	defer l.open.Add(-1)
	for i := 1; i < len(l.want); i++ {
		if err := l.readChunk(body, s, &buf, i); err != nil {
			return err
		}
	}
	if n, _ := io.Copy(io.Discard, body); n > 0 {
		return fmt.Errorf("%s: %d bytes after the fixture's end", s.id, n)
	}
	return nil
}

func (l *loader) readChunk(body io.Reader, s *sample, buf *[]byte, i int) error {
	want := l.want[i]
	if cap(*buf) < len(want) {
		*buf = make([]byte, len(want))
	}
	b := (*buf)[:len(want)]
	if _, err := io.ReadFull(body, b); err != nil {
		return fmt.Errorf("%s: chunk %d: %w", s.id, i, err)
	}
	s.chunks = append(s.chunks, time.Now())
	if !bytes.Equal(b, want) {
		return fmt.Errorf("%s: chunk %d differs from the fixture", s.id, i)
	}
	return nil
}

// opened counts one more open stream and raises the peak.
func (l *loader) opened() {
	n := l.open.Add(1)
	for {
		old := l.peak.Load()
		if n <= old || l.peak.CompareAndSwap(old, n) {
			return
		}
	}
}

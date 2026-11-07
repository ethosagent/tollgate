package forward

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ethosagent/tollgate/internal/model"
)

// reads splits b into reads of n bytes.
func reads(b []byte, n int) *chunkReader {
	r := &chunkReader{err: io.EOF}
	for len(b) > 0 {
		k := min(n, len(b))
		r.chunks = append(r.chunks, string(b[:k]))
		b = b[k:]
	}
	return r
}

// drain ends q as a clean call would and returns its frames.
func drain(q *FrameQueue) []string {
	q.finish(model.FinishOK)
	var out []string
	for f, _, ok := q.Next(); ok; f, _, ok = q.Next() {
		out = append(out, string(f))
	}
	return out
}

func TestTee_ClientBytesIdenticalWithAndWithoutConsumer(t *testing.T) {
	files, _ := filepath.Glob("../../testkit/testdata/*/*.sse")
	if len(files) == 0 {
		t.Fatal("no fixtures")
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(filepath.Base(f), func(t *testing.T) {
			plain, tapped := &recWriter{}, &recWriter{}
			q := NewFrameQueue(len(raw))
			if _, err := Tee(plain, reads(raw, 37), nil, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := Tee(tapped, reads(raw, 37), q, nil); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(plain.buf.Bytes(), raw) || !bytes.Equal(tapped.buf.Bytes(), raw) {
				t.Fatal("client bytes differ from the upstream bytes")
			}
			if got, want := drain(q), scanAll(string(raw), len(raw)+1); !slices.Equal(got, want) {
				t.Fatalf("frames = %q, want %q", got, want)
			}
		})
	}
}

func TestTee_WritesClientBeforeScanning(t *testing.T) {
	w := &recWriter{failAt: 1}
	q := NewFrameQueue(8)
	_, err := Tee(w, reads([]byte("data: a\n\ndata: b\n\n"), 100), q, nil)
	if err == nil {
		t.Fatal("want the client write error")
	}
	if got := drain(q); len(got) != 0 {
		t.Fatalf("scanned %q although the client write failed first", got)
	}
}

func TestTee_BeginRunsBeforeFirstWrite(t *testing.T) {
	var order []string
	w := &orderWriter{log: &order}
	begin := func() { order = append(order, "begin") }
	if _, err := Tee(w, reads([]byte("data: a\n\n"), 100), nil, begin); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(order, []string{"begin", "write"}) {
		t.Fatalf("order = %v", order)
	}
}

type orderWriter struct {
	recWriter
	log *[]string
}

func (w *orderWriter) Write(p []byte) (int, error) {
	*w.log = append(*w.log, "write")
	return w.recWriter.Write(p)
}

// slowSource writes n numbered frames, one per gap of fake time, then EOF.
func slowSource(n int, gap time.Duration) (io.Reader, []byte) {
	pr, pw := io.Pipe()
	var all bytes.Buffer
	for i := range n {
		fmt.Fprintf(&all, "data: %d\n\n", i)
	}
	frames := strings.SplitAfter(all.String(), "\n\n")
	go func() {
		for _, f := range frames[:n] {
			time.Sleep(gap)
			_, _ = pw.Write([]byte(f))
		}
		pw.Close()
	}()
	return pr, all.Bytes()
}

func TestTee_StuckConsumerNeverDelaysClient(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const n, gap, buffer = 50, 10 * time.Millisecond, 2
		src, want := slowSource(n, gap)
		q := NewFrameQueue(buffer)
		release := make(chan struct{})
		go func() { // takes one frame, then never reads again
			q.Next()
			<-release
		}()
		w := &recWriter{}
		start := time.Now()
		if _, err := Tee(w, src, q, nil); err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(start); elapsed != n*gap {
			t.Fatalf("stream took %v of fake time, want exactly the source time %v", elapsed, n*gap)
		}
		if !bytes.Equal(w.buf.Bytes(), want) {
			t.Fatal("client bytes changed")
		}
		if got := q.Dropped(); got < n-buffer-1 {
			t.Fatalf("dropped = %d, want at least %d", got, n-buffer-1)
		}
		close(release)
	})
}

func TestTee_CountsOversizedFramesAsDropped(t *testing.T) {
	q := NewFrameQueue(4)
	big := "data: " + strings.Repeat("x", MaxFrameBytes) + "\n\n"
	if _, err := Tee(&recWriter{}, reads([]byte(big+"data: ok\n\n"), 1<<14), q, nil); err != nil {
		t.Fatal(err)
	}
	if got := drain(q); !slices.Equal(got, []string{"data: ok"}) || q.Dropped() != 1 {
		t.Fatalf("frames=%q dropped=%d", got, q.Dropped())
	}
}

func TestFrameQueue_FinishIsNilSafe(_ *testing.T) {
	var nilQ *FrameQueue
	nilQ.finish(model.FinishOK)
}

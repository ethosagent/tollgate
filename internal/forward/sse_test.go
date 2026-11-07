package forward

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// scanAll feeds the input in pieces of the given sizes (cycled) and returns
// the emitted frames, copied, including the end-of-stream flush.
func scanAll(input string, sizes ...int) []string {
	var s Scanner
	var got []string
	emit := func(f []byte) { got = append(got, string(f)) }
	b := []byte(input)
	for i := 0; len(b) > 0; i++ {
		n := min(sizes[i%len(sizes)], len(b))
		s.Scan(b[:n], emit)
		b = b[n:]
	}
	s.Finish(emit)
	return got
}

func TestScanner_Frames(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"two frames", "data: a\n\ndata: b\n\n", []string{"data: a", "data: b"}},
		{"event name and data", "event: message_start\ndata: {}\n\n", []string{"event: message_start\ndata: {}"}},
		{"crlf line endings", "data: a\r\n\r\ndata: b\r\n\r\n", []string{"data: a", "data: b"}},
		{"cr only line endings", "data: a\r\rdata: b\r\r", []string{"data: a", "data: b"}},
		{"mixed endings", "data: a\r\n\ndata: b\n\r\n", []string{"data: a", "data: b"}},
		{"multi-line data", "data: one\ndata: two\n\n", []string{"data: one\ndata: two"}},
		{"comment-only frame is dropped", ": keep-alive\n\ndata: a\n\n", []string{"data: a"}},
		{"comment inside frame is kept", ": note\ndata: a\n\n", []string{": note\ndata: a"}},
		{"final frame without blank line", "data: a\n\ndata: tail", []string{"data: a", "data: tail"}},
		{"final frame with one newline", "data: tail\n", []string{"data: tail"}},
		{"extra blank lines", "\n\ndata: a\n\n\n\n\ndata: b\n\n", []string{"data: a", "data: b"}},
		{"empty input", "", nil},
		{"data with no space", "data:a\n\n", []string{"data:a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, sizes := range [][]int{{1 << 20}, {1}, {2}, {3}, {1, 5}} {
				if got := scanAll(tt.input, sizes...); !slices.Equal(got, tt.want) {
					t.Errorf("read sizes %v: got %q, want %q", sizes, got, tt.want)
				}
			}
		})
	}
}

func TestScanner_DropsOversizedFrame(t *testing.T) {
	var s Scanner
	var got []string
	emit := func(f []byte) { got = append(got, string(f)) }
	big := "data: " + strings.Repeat("x", MaxFrameBytes) + "\n\n"
	s.Scan([]byte("data: a\n\n"+big+"data: b\n\n"), emit)
	s.Finish(emit)
	if !slices.Equal(got, []string{"data: a", "data: b"}) || s.Oversized != 1 {
		t.Fatalf("frames=%q oversized=%d", got, s.Oversized)
	}
}

func TestScanner_ResetReusesBuffer(t *testing.T) {
	var s Scanner
	var n int
	s.Scan([]byte("data: partial"), func([]byte) { n++ })
	s.Reset()
	s.Scan([]byte("data: a\n\n"), func(f []byte) {
		n++
		if string(f) != "data: a" {
			t.Errorf("frame after Reset = %q", f)
		}
	})
	if n != 1 {
		t.Fatalf("emitted %d frames, want 1", n)
	}
}

func fixtureSeeds(tb testing.TB) [][]byte {
	tb.Helper()
	files, err := filepath.Glob("../fixtures/testdata/*/*.sse")
	if err != nil || len(files) == 0 {
		tb.Fatalf("no SSE fixtures found: %v", err)
	}
	var seeds [][]byte
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			tb.Fatal(err)
		}
		seeds = append(seeds, b)
	}
	return seeds
}

func TestScanner_FixturesSplitAtEveryOffset(t *testing.T) {
	for _, seed := range fixtureSeeds(t) {
		whole := scanAll(string(seed), len(seed)+1)
		if len(whole) == 0 {
			t.Fatal("fixture produced no frames")
		}
		for cut := 1; cut < len(seed); cut += 7 {
			if got := scanAll(string(seed), cut, len(seed)); !slices.Equal(got, whole) {
				t.Fatalf("cut at %d changed the frames", cut)
			}
		}
	}
}

// FuzzSSEScanner checks that however the bytes are split across reads, the
// frames are the same, and that no frame holds a blank line or a bare CR.
func FuzzSSEScanner(f *testing.F) {
	for _, seed := range fixtureSeeds(f) {
		f.Add(seed, 1)
		f.Add(seed, 64)
	}
	f.Add([]byte("data: a\r\n\r\ndata: b\r\r: c\n\ndata: tail"), 3)
	f.Fuzz(func(t *testing.T, input []byte, size int) {
		size = max(size, 1)
		whole := scanAll(string(input), len(input)+1)
		if got := scanAll(string(input), size); !slices.Equal(got, whole) {
			t.Fatalf("split %d: got %q, want %q", size, got, whole)
		}
		for _, fr := range whole {
			if strings.Contains(fr, "\r") || strings.Contains(fr, "\n\n") || fr == "" || bytes.HasSuffix([]byte(fr), []byte("\n")) {
				t.Fatalf("malformed frame %q", fr)
			}
		}
	})
}

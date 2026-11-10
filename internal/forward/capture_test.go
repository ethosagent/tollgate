package forward

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ethosagent/tollgate/internal/model"
)

type reasons []string

func (r *reasons) note(s string) { *r = append(*r, s) }

func TestCapture_HoldsAResponseUntilReleasedOrReplaced(t *testing.T) {
	rec := httptest.NewRecorder()
	var skips reasons
	c := NewCapture(rec, 64, skips.note)
	c.Header().Set("Content-Type", "application/json")
	c.WriteHeader(201)
	_, _ = c.Write([]byte(`{"a":`))
	_, _ = c.Write([]byte(`1}`))
	if err := http.NewResponseController(c).Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if rec.Body.Len() != 0 || rec.Code != 200 || rec.Header().Get("Content-Type") != "" {
		t.Fatal("a held response reached the client")
	}
	r := c.Response()
	if !c.Started() || c.Passed() || r.Status != 201 || string(r.Body) != `{"a":1}` || r.Header.Get("Content-Type") != "application/json" || len(skips) != 0 {
		t.Fatalf("response=%+v passed=%v skips=%v", r, c.Passed(), skips)
	}
	if err := c.Release(); err != nil {
		t.Fatal(err)
	}
	if len(skips) != 0 || rec.Code != 201 || rec.Body.String() != `{"a":1}` || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("released: code=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestCapture_PassesThroughWhenItCannotHoldTheResponse(t *testing.T) {
	tests := []struct {
		name, reason string
		header       http.Header
		chunks       []string
	}{
		{"body grows past the cap", "over_cap", nil, []string{strings.Repeat("a", 40), strings.Repeat("b", 40), "tail"}},
		{"declared length past the cap", "over_cap", http.Header{"Content-Length": {"500"}}, []string{"x"}},
		{"content encoding", "encoded", http.Header{"Content-Encoding": {"gzip"}}, []string{"x"}},
		{"event stream", "streaming", http.Header{"Content-Type": {"text/event-stream"}}, []string{"data: x\n\n"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			var skips reasons
			c := NewCapture(rec, 64, skips.note)
			for k, v := range tt.header {
				c.Header()[k] = v
			}
			c.WriteHeader(200)
			for _, ch := range tt.chunks {
				if _, err := c.Write([]byte(ch)); err != nil {
					t.Fatal(err)
				}
			}
			if rec.Body.String() != strings.Join(tt.chunks, "") || !c.Passed() || len(skips) != 1 || skips[0] != tt.reason {
				t.Fatalf("body=%q passed=%v skips=%v, want the bytes unchanged and one %q skip", rec.Body.String(), c.Passed(), skips, tt.reason)
			}
			if err := c.Release(); err != nil || rec.Body.Len() != len(strings.Join(tt.chunks, "")) {
				t.Fatalf("Release after pass-through must do nothing: %v", err)
			}
		})
	}
}

func TestWriteResponse_SetsLengthDefaultsStatusAndDropsHopByHop(t *testing.T) {
	tests := []struct {
		name       string
		in         model.Response
		wantStatus int
	}{
		{"zero status is 200", model.Response{Body: []byte("hi")}, 200},
		{"invalid status is 500", model.Response{Status: 42, Body: []byte("hi")}, 500},
		{"stale length is replaced", model.Response{Status: 202, Header: http.Header{"Content-Length": {"999"}, "Connection": {"close"}, "X-A": {"1"}}, Body: []byte("hi")}, 202},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			WriteResponse(rec, &tt.in)
			if rec.Code != tt.wantStatus || rec.Body.String() != "hi" || rec.Header().Get("Content-Length") != strconv.Itoa(2) || rec.Header().Get("Connection") != "" {
				t.Fatalf("code=%d headers=%v body=%q", rec.Code, rec.Header(), rec.Body.String())
			}
		})
	}
}

func TestTimedWriter_RecordsFirstByteAndStillFlushes(t *testing.T) {
	rec := httptest.NewRecorder()
	tw := NewTimedWriter(rec)
	if !tw.FirstWrite().IsZero() {
		t.Fatal("first write set before any write")
	}
	before := time.Now()
	_, _ = tw.Write([]byte("a"))
	first := tw.FirstWrite()
	_, _ = tw.Write([]byte("b"))
	if first.Before(before) || !tw.FirstWrite().Equal(first) {
		t.Fatalf("first=%v after=%v", first, tw.FirstWrite())
	}
	if err := http.NewResponseController(tw).Flush(); err != nil || !rec.Flushed {
		t.Fatalf("Flush through the wrapper: err=%v flushed=%v", err, rec.Flushed)
	}
}

func TestTimedWriter_FrameBreak(t *testing.T) {
	tests := []struct {
		name   string
		writes []string
		want   string
	}{
		{"nothing written", nil, ""},
		{"whole frame", []string{"data: x\n\n"}, ""},
		{"crlf frame", []string{"data: x\r\n\r\n"}, ""},
		{"line, no blank line", []string{"data: x\n"}, "\n"},
		{"crlf line, no blank line", []string{"data: x\r\n"}, "\n"},
		{"mid line", []string{"data: x\n\ndata: {\"a"}, "\n\n"},
		{"frame split over byte writes", []string{"d", "a", "\n", "\n"}, ""},
		{"line split over byte writes", []string{"d", "a", "\n"}, "\n"},
		{"mid line after a frame, byte writes", []string{"\n", "\n", "x"}, "\n\n"},
		{"long then short writes", []string{"data: xxxxxxxx\n\n", "e", "\n"}, "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tw := NewTimedWriter(httptest.NewRecorder())
			for _, w := range tt.writes {
				if _, err := tw.Write([]byte(w)); err != nil {
					t.Fatal(err)
				}
			}
			if got := tw.FrameBreak(); got != tt.want {
				t.Fatalf("FrameBreak = %q, want %q", got, tt.want)
			}
		})
	}
}

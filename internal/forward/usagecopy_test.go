package forward

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBodyCopy_WritesThroughAndKeepsACopy(t *testing.T) {
	tests := []struct {
		name     string
		limit    int64
		header   map[string]string
		writes   []string
		wantBody string
		wantOK   bool
		reason   string // why the copy was skipped (the same reasons as Capture)
	}{
		{name: "plain json", limit: 100, header: map[string]string{"Content-Type": "application/json"}, writes: []string{`{"a":`, `1}`}, wantBody: `{"a":1}`, wantOK: true},
		{name: "over the cap is not kept", limit: 4, writes: []string{"abc", "def"}, wantOK: false, reason: SkipOverCap},
		{name: "encoded bodies are not readable", limit: 100, header: map[string]string{"Content-Encoding": "gzip"}, writes: []string{"x"}, wantOK: false, reason: SkipEncoded},
		{name: "identity encoding is readable", limit: 100, header: map[string]string{"Content-Encoding": "identity"}, writes: []string{"x"}, wantBody: "x", wantOK: true},
		{name: "event streams are not kept", limit: 100, header: map[string]string{"Content-Type": "text/event-stream; charset=utf-8"}, writes: []string{"data: x\n\n"}, wantOK: false, reason: SkipStreaming},
		{name: "declared length over the cap", limit: 4, header: map[string]string{"Content-Length": "50"}, writes: []string{"ab"}, wantOK: false, reason: SkipOverCap},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c := NewBodyCopy(rec, tt.limit)
			for k, v := range tt.header {
				c.Header().Set(k, v)
			}
			c.WriteHeader(http.StatusTeapot)
			var sent strings.Builder
			for _, w := range tt.writes {
				if _, err := c.Write([]byte(w)); err != nil {
					t.Fatal(err)
				}
				sent.WriteString(w)
			}
			if rec.Code != http.StatusTeapot || rec.Body.String() != sent.String() {
				t.Fatalf("client got %d %q, want 418 %q: every byte must pass through", rec.Code, rec.Body.String(), sent.String())
			}
			body, status, ok := c.Held()
			if ok != tt.wantOK || (ok && (string(body) != tt.wantBody || status != http.StatusTeapot)) {
				t.Fatalf("Held = %q %d %v, want %q %v", body, status, ok, tt.wantBody, tt.wantOK)
			}
			if got := c.Skipped(); got != tt.reason {
				t.Fatalf("Skipped = %q, want %q", got, tt.reason)
			}
		})
	}
}

func TestBodyCopy_ImplicitHeaderIs200AndNoWriteIsNotHeld(t *testing.T) {
	c := NewBodyCopy(httptest.NewRecorder(), 10)
	if _, _, ok := c.Held(); ok {
		t.Fatal("Held ok before any write")
	}
	_, _ = c.Write([]byte("x"))
	if _, status, ok := c.Held(); !ok || status != 200 {
		t.Fatalf("status = %d ok = %v, want 200 true", status, ok)
	}
}

func TestBodyCopy_FlushReachesTheClientWriter(t *testing.T) {
	rec := httptest.NewRecorder()
	c := NewBodyCopy(rec, 10)
	if err := http.NewResponseController(c).Flush(); err != nil || !rec.Flushed {
		t.Fatalf("flush = %v flushed = %v, want the flush to reach the wrapped writer", err, rec.Flushed)
	}
}

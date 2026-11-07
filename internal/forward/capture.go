package forward

import (
	"bytes"
	"net/http"
	"strconv"
	"time"

	"github.com/ethosagent/tollgate/internal/model"
)

// Reasons a Capture stops holding a response and passes it through.
const (
	SkipOverCap   = "over_cap"  // the body is, or grew, larger than the cap
	SkipEncoded   = "encoded"   // Content-Encoding: hooks cannot read the bytes
	SkipStreaming = "streaming" // an event stream must never be held
)

// Capture is an http.ResponseWriter that holds one response in memory so a
// hook can inspect or replace it (stream:false, Hook.OnResponse). Memory is
// bounded by limit. A response it cannot hold (over limit, encoded, or an
// event stream) is passed to dst unchanged, in order, from that point on, and
// skipped is told why once. Use it from one goroutine, as Forward does.
type Capture struct {
	dst     http.ResponseWriter
	limit   int64
	skipped func(reason string)
	hdr     http.Header
	status  int
	started bool
	passed  bool
	buf     bytes.Buffer
}

// NewCapture holds up to limit body bytes written for dst. skipped may be nil.
func NewCapture(dst http.ResponseWriter, limit int64, skipped func(reason string)) *Capture {
	return &Capture{dst: dst, limit: limit, skipped: skipped, hdr: http.Header{}}
}

// Header implements http.ResponseWriter.
func (c *Capture) Header() http.Header {
	if c.passed {
		return c.dst.Header()
	}
	return c.hdr
}

// WriteHeader implements http.ResponseWriter.
func (c *Capture) WriteHeader(code int) {
	if c.started {
		return
	}
	c.started, c.status = true, code
	if reason := unholdable(c.hdr, c.limit); reason != "" {
		_ = c.pass(reason)
	}
}

// unholdable is the one rule for whether a response announced with h can be
// kept in memory for reading (Capture, BodyCopy): it returns why not, or "".
// limit caps the body; a larger declared length, an encoded body or an
// event stream cannot be kept.
func unholdable(h http.Header, limit int64) string {
	if n, err := strconv.ParseInt(h.Get("Content-Length"), 10, 64); err == nil && n > limit {
		return SkipOverCap
	}
	if enc := h.Get("Content-Encoding"); enc != "" && enc != "identity" {
		return SkipEncoded
	}
	if isEventStream(h) {
		return SkipStreaming
	}
	return ""
}

// Write implements http.ResponseWriter.
func (c *Capture) Write(p []byte) (int, error) {
	if !c.started {
		c.WriteHeader(http.StatusOK)
	}
	if c.passed {
		return c.dst.Write(p)
	}
	if int64(c.buf.Len()+len(p)) > c.limit {
		if err := c.pass(SkipOverCap); err != nil {
			return 0, err
		}
		return c.dst.Write(p)
	}
	return c.buf.Write(p)
}

// FlushError makes http.ResponseController flushes harmless: nothing is sent
// while a response is held, and flushes go through once it is passed.
func (c *Capture) FlushError() error {
	if c.passed {
		return http.NewResponseController(c.dst).Flush()
	}
	return nil
}

// pass sends the held head and bytes to dst and switches to direct writes;
// a non-empty reason is reported as a skip.
func (c *Capture) pass(reason string) error {
	if c.skipped != nil && reason != "" {
		c.skipped(reason)
	}
	c.passed = true
	h := c.dst.Header()
	for k, v := range c.hdr {
		h[k] = v
	}
	c.dst.WriteHeader(c.status)
	if c.buf.Len() == 0 {
		return nil
	}
	_, err := c.dst.Write(c.buf.Bytes())
	c.buf.Reset()
	return err
}

// Started reports whether the response head was written.
func (c *Capture) Started() bool { return c.started }

// Passed reports whether the response went to dst unheld.
func (c *Capture) Passed() bool { return c.passed }

// Response returns the held response. Header and Body alias the Capture; copy
// them before handing them to code that may keep them.
func (c *Capture) Response() *model.Response {
	return &model.Response{Status: c.status, Header: c.hdr, Body: c.buf.Bytes()}
}

// Release sends what is held to dst unchanged, for a body that broke before
// the end: the client must see the cut, not silence. It does nothing when the
// response was passed or never started.
func (c *Capture) Release() error {
	if !c.started || c.passed {
		return nil
	}
	return c.pass("")
}

// ResponseStatus is the status WriteResponse sends for code: 200 for zero,
// 500 for anything outside 200..599.
func ResponseStatus(code int) int {
	switch {
	case code == 0:
		return http.StatusOK
	case code < 200 || code > 599:
		return http.StatusInternalServerError
	}
	return code
}

// WriteResponse writes r to w with a correct Content-Length. A zero Status is
// 200 and a status outside 200..599 is 500. Hop-by-hop headers in r.Header
// are dropped, the same filter as for upstream responses.
func WriteResponse(w http.ResponseWriter, r *model.Response) {
	responseHeaders.CopyTo(w.Header(), r.Header)
	w.Header().Set("Content-Length", strconv.Itoa(len(r.Body)))
	w.WriteHeader(ResponseStatus(r.Status))
	_, _ = w.Write(r.Body) // a failed write means the client left; there is nobody to tell
}

// TimedWriter records when the first body byte was written, for TTFB. It adds
// no buffering; flushes reach the wrapped writer through Unwrap.
type TimedWriter struct {
	http.ResponseWriter
	first time.Time
}

// NewTimedWriter wraps w.
func NewTimedWriter(w http.ResponseWriter) *TimedWriter { return &TimedWriter{ResponseWriter: w} }

// Write implements http.ResponseWriter.
func (t *TimedWriter) Write(p []byte) (int, error) {
	if t.first.IsZero() && len(p) > 0 {
		t.first = time.Now()
	}
	return t.ResponseWriter.Write(p)
}

// Unwrap lets http.ResponseController reach the wrapped writer.
func (t *TimedWriter) Unwrap() http.ResponseWriter { return t.ResponseWriter }

// FirstWrite is the time of the first non-empty write, or zero.
func (t *TimedWriter) FirstWrite() time.Time { return t.first }

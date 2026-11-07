package forward

import (
	"bytes"
	"net/http"
)

// BodyCopy is an http.ResponseWriter that passes every byte straight to the
// client and keeps a bounded copy, so a stream:false response can be read for
// usage without being held back (unlike Capture, which holds it for
// OnResponse). The client never waits on it. Use it from one goroutine, as
// Forward does.
type BodyCopy struct {
	http.ResponseWriter
	limit   int64
	status  int
	skip    string // why the copy was given up (a Skip* reason), or ""
	started bool
	buf     bytes.Buffer
}

// NewBodyCopy copies up to limit body bytes written for dst.
func NewBodyCopy(dst http.ResponseWriter, limit int64) *BodyCopy {
	return &BodyCopy{ResponseWriter: dst, limit: limit}
}

// WriteHeader implements http.ResponseWriter.
func (c *BodyCopy) WriteHeader(code int) {
	if !c.started {
		c.started, c.status = true, code
		c.skip = unholdable(c.Header(), c.limit)
	}
	c.ResponseWriter.WriteHeader(code)
}

// Write implements http.ResponseWriter: the client first, then the copy.
func (c *BodyCopy) Write(p []byte) (int, error) {
	if !c.started {
		c.WriteHeader(http.StatusOK)
	}
	n, err := c.ResponseWriter.Write(p)
	if c.skip == "" {
		if int64(c.buf.Len()+n) > c.limit {
			c.skip = SkipOverCap
			c.buf = bytes.Buffer{}
		} else {
			c.buf.Write(p[:n])
		}
	}
	return n, err
}

// Unwrap lets http.ResponseController reach the wrapped writer.
func (c *BodyCopy) Unwrap() http.ResponseWriter { return c.ResponseWriter }

// Held returns the copied body and the response status. ok is false when
// nothing was written or the copy is unreadable or incomplete by design
// (over the limit, encoded, an event stream).
func (c *BodyCopy) Held() (body []byte, status int, ok bool) {
	if !c.started || c.skip != "" {
		return nil, 0, false
	}
	return c.buf.Bytes(), c.status, true
}

// Skipped is why the copy was given up (SkipOverCap, SkipEncoded,
// SkipStreaming), or "" when it was kept or nothing was written.
func (c *BodyCopy) Skipped() string { return c.skip }

package forward

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"

	"io"

	"github.com/ethosagent/tollgate/internal/model"
	"github.com/ethosagent/tollgate/internal/normalise"
)

// Pipeline errors. Each carries its HTTP status and a message safe to show
// the client; none wraps a transport error, so no URL leaks through them.
var (
	// ErrBodyTooLarge (413): the request body exceeds the limit.
	ErrBodyTooLarge error = &statusError{"request body too large", 413}
	// ErrBadBody (400): the request body could not be read.
	ErrBadBody error = &statusError{"request body unreadable", 400}
	// ErrUpstreamTimeout (504): the upstream did not answer in time.
	ErrUpstreamTimeout error = &statusError{"upstream timed out", 504}
	// ErrUpstreamUnreachable (502): the upstream round trip failed.
	ErrUpstreamUnreachable error = &statusError{"upstream unreachable", 502}
)

// ReadBody reads r's body under limit bytes. Errors are ErrBodyTooLarge or
// ErrBadBody.
func ReadBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	if r.ContentLength > limit {
		return nil, ErrBodyTooLarge
	}
	buf := bytes.NewBuffer(make([]byte, 0, max(r.ContentLength, 0)+bytes.MinRead))
	_, err := buf.ReadFrom(http.MaxBytesReader(w, r.Body, limit))
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		return nil, ErrBodyTooLarge
	case err != nil:
		return nil, ErrBadBody
	}
	return buf.Bytes(), nil
}

// Request is one upstream call. Target must come from Target.
type Request struct {
	Method string
	Target *url.URL
	Header http.Header
	Body   []byte
	// Limits bound the call; the zero value sets none.
	Limits StreamLimits
	// Shape picks the wire format of the error event appended to an SSE
	// stream that breaks after it started.
	Shape normalise.ErrorShape
	// Frames, if not nil, receives a copy of every SSE frame after the
	// client has been sent the bytes. Forward closes it when the call ends.
	Frames *FrameQueue
}

// Result is how a forwarded call ended.
type Result struct {
	Status int          // upstream status relayed to the client; 0 if none
	Finish model.Finish // how the call ended
	// Cut means headers were sent and the body ended abnormally: the caller
	// must abort the connection so the client sees a cut stream, not a
	// clean end. An SSE stream is never cut: it ends with an error event.
	Cut bool
}

// Forward sends req upstream under ctx and relays the response to w
// unchanged, flushing every read. Response headers are held until the first
// body byte, so a failure before any byte reaches the client returns
// ErrUpstreamTimeout or ErrUpstreamUnreachable for the caller to answer. A
// client that has gone away gets no error, only FinishClientCancel. A limit
// that fires mid-stream, or an upstream that drops, ends an SSE response with
// a provider-shaped error event and Finish timeout or upstream_error.
func Forward(ctx context.Context, rt http.RoundTripper, w http.ResponseWriter, req Request) (Result, error) {
	defer req.Frames.close()
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	wd := startWatchdog(req.Limits, cancel)
	defer wd.stop()
	// req.Target passed Target, the one check every upstream URL goes
	// through, so the caller cannot steer this request (ADR 0007).
	out, err := http.NewRequestWithContext(ctx, req.Method, req.Target.String(), bytes.NewReader(req.Body))
	if err != nil {
		return Result{Finish: model.FinishUpstreamError}, ErrUpstreamUnreachable
	}
	out.Header = req.Header
	if out.Header == nil {
		out.Header = http.Header{} // a RoundTripper rejects a nil header
	}
	resp, err := rt.RoundTrip(out)
	if err != nil {
		return failedBeforeBody(ctx, err)
	}
	defer resp.Body.Close()
	wd.attach(resp.Body)
	return relayResponse(ctx, w, resp, req, wd)
}

// failedBeforeBody classifies an error that happened before any byte went to
// the client.
func failedBeforeBody(ctx context.Context, err error) (Result, error) {
	switch f := finishFor(ctx, err); f {
	case model.FinishClientCancel:
		return Result{Finish: f}, nil
	case model.FinishTimeout:
		return Result{Finish: f}, ErrUpstreamTimeout
	default:
		return Result{Finish: f}, ErrUpstreamUnreachable
	}
}

var responseHeaders = NewHeaderFilter()

func relayResponse(ctx context.Context, w http.ResponseWriter, resp *http.Response, req Request, wd *watchdog) (Result, error) {
	res := Result{Status: resp.StatusCode, Finish: model.FinishOK}
	if resp.StatusCode >= http.StatusBadRequest {
		res.Finish = model.FinishUpstreamError
	}
	sent := false
	begin := func() {
		sent = true
		responseHeaders.CopyTo(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
	}
	var src io.Reader = resp.Body
	if wd != nil {
		src = touchReader{resp.Body, wd}
	}
	_, err := Tee(w, src, req.Frames, begin)
	if err == nil {
		if !sent { // an empty body still carries a status and headers
			begin()
		}
		return res, nil
	}
	f := finishFor(ctx, err)
	if !sent {
		return failedBeforeBody(ctx, err)
	}
	res.Finish = f
	if f != model.FinishClientCancel {
		res.Cut = !endSSEWithError(w, resp.Header, req.Shape, f)
	}
	return res, nil
}

// endSSEWithError appends a provider-shaped error event to a broken SSE
// stream so the client sees why it ended. It reports false, doing nothing,
// for a body that is not SSE.
func endSSEWithError(w http.ResponseWriter, h http.Header, shape normalise.ErrorShape, f model.Finish) bool {
	if !isSSE(h) {
		return false
	}
	status, msg := http.StatusBadGateway, "upstream connection lost"
	if f == model.FinishTimeout {
		status, msg = http.StatusGatewayTimeout, "stream timed out"
	}
	ev := normalise.AppendErrorEvent(nil, shape, status, msg)
	_, _ = writeFlush(w, http.NewResponseController(w), ev) // the client may be gone; the call is ending anyway
	return true
}

func isSSE(h http.Header) bool {
	ct := h.Get("Content-Type")
	return len(ct) >= len("text/event-stream") && strings.EqualFold(ct[:len("text/event-stream")], "text/event-stream")
}

// finishFor classifies a failed round trip or relay. ctx is the call's
// context: cancelled means the client left; past its deadline means timeout.
func finishFor(ctx context.Context, err error) model.Finish {
	var ne net.Error
	switch {
	case errors.Is(context.Cause(ctx), errLimit):
		return model.FinishTimeout
	case errors.Is(err, ErrClientWrite), errors.Is(ctx.Err(), context.Canceled):
		return model.FinishClientCancel
	case errors.Is(ctx.Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded),
		errors.As(err, &ne) && ne.Timeout():
		return model.FinishTimeout
	}
	return model.FinishUpstreamError
}

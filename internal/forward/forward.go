package forward

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"

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
	// Frames, if not nil, receives a copy of every SSE frame the client was
	// sent, after it was sent; with Final, that includes a replacement's
	// frames and never a terminal region a Finalizer discarded. Forward
	// closes it when the call ends and records the ending in Frames.End.
	Frames *FrameQueue
	// Final, if not nil, holds the terminal event of an SSE response until
	// its Decide has run (final.go). Nil means the terminal event is relayed
	// like any other frame, with no added work.
	Final *Finalize
}

// Result is how a forwarded call ended.
type Result struct {
	Status int          // upstream status relayed to the client; 0 if none
	Finish model.Finish // how the call ended
	// Cut means headers were sent and the body ended abnormally: the caller
	// must abort the connection so the client sees a cut stream, not a
	// clean end. An SSE stream is never cut: it ends with an error event.
	Cut bool
	// Final is what the Finalizer barrier did, when the response went
	// through it; nil otherwise.
	Final *Finalized
}

// Forward sends req upstream under ctx and relays the response to w
// unchanged, flushing every read. Response headers are held until the first
// body byte, so a failure before any byte reaches the client returns
// ErrUpstreamTimeout or ErrUpstreamUnreachable for the caller to answer. A
// client that has gone away gets no error, only FinishClientCancel. A limit
// that fires mid-stream, or an upstream that drops, ends an SSE response with
// a provider-shaped error event and Finish timeout or upstream_error.
func Forward(ctx context.Context, rt http.RoundTripper, w http.ResponseWriter, req Request) (res Result, err error) {
	defer func() { req.Frames.finish(res.Finish) }()
	ctx, cancel := withLimits(ctx, req.Limits)
	defer cancel(nil)
	clock := startClock(req.Limits, cancel)
	defer clock.stop()
	// req.Target passed Target, the one check every upstream URL goes
	// through, so the caller cannot steer this request (ADR 0007).
	out, err := newUpstreamRequest(ctx, req, req.Body)
	if err != nil {
		return Result{Finish: model.FinishUpstreamError}, ErrUpstreamUnreachable
	}
	resp, err := rt.RoundTrip(out)
	if err != nil {
		return failedBeforeBody(ctx, err)
	}
	defer resp.Body.Close()
	// Whatever ends the call (a limit, the client leaving) closes the body,
	// which unblocks a read stuck on a silent upstream.
	defer context.AfterFunc(ctx, func() { _ = resp.Body.Close() })()
	clock.headers()
	var src io.Reader = resp.Body
	if clock != nil {
		src = &touchReader{resp.Body, clock}
	}
	return relayResponse(ctx, w, resp, src, req, relayEnv{rt: rt, clock: clock})
}

// newUpstreamRequest builds the upstream request for body from req's target
// and headers.
func newUpstreamRequest(ctx context.Context, req Request, body []byte) (*http.Request, error) {
	out, err := http.NewRequestWithContext(ctx, req.Method, req.Target.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	out.Header = req.Header
	if out.Header == nil {
		out.Header = http.Header{} // a RoundTripper rejects a nil header
	}
	return out, nil
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

// relayEnv is what a relay needs beyond the response: the transport and the
// idle clock, for a Finalizer's recovery call.
type relayEnv struct {
	rt    http.RoundTripper
	clock *idleClock
}

func relayResponse(ctx context.Context, w http.ResponseWriter, resp *http.Response, src io.Reader, req Request, env relayEnv) (Result, error) {
	res := Result{Status: resp.StatusCode, Finish: model.FinishOK}
	if resp.StatusCode >= http.StatusBadRequest {
		res.Finish = model.FinishUpstreamError
	}
	var sent bool
	var err error
	if req.Final != nil && finalizable(resp) && !lengthDeclared(resp.Header) {
		sent, res.Final, err = relayFinal(ctx, w, resp, src, req, env)
	} else {
		// begin stays on the stack: nothing here may let it escape (STR-6).
		begin := func() {
			sent = true
			startResponse(w, resp)
		}
		_, err = Tee(w, src, req.Frames, begin)
	}
	switch {
	case err == nil && !sent: // an empty body still carries a status and headers
		startResponse(w, resp)
	case err != nil && !sent:
		return failedBeforeBody(ctx, err)
	case err != nil:
		res.Finish = finishFor(ctx, err)
		if res.Finish != model.FinishClientCancel {
			res.Cut = !terminate(w, resp.Header, req.Shape, res.Finish)
		}
	}
	return res, nil
}

// startResponse sends the status line and the filtered upstream headers.
func startResponse(w http.ResponseWriter, resp *http.Response) {
	responseHeaders.CopyTo(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
}

// terminate ends a response that broke after its first byte. An event
// stream gets a provider-shaped error event (status from
// normalise.FinishStatus, fixed message) and a clean end; terminate then
// reports true. Any other body, or one whose head declared its length,
// cannot carry an in-band error, so it reports false and the caller aborts
// the connection. The choice is made once per
// response from its media type: the wire alone cannot tell, because a
// streaming wire still answers errors and stream:false calls with JSON.
func terminate(w http.ResponseWriter, h http.Header, shape normalise.ErrorShape, f model.Finish) bool {
	if !isEventStream(h) || lengthDeclared(h) {
		return false
	}
	status := normalise.FinishStatus(f)
	ev := normalise.AppendErrorEvent(nil, shape, status, normalise.StatusMessage(status))
	_, _ = writeFlush(w, http.NewResponseController(w), ev) // the client may be gone; the call is ending anyway
	return true
}

// lengthDeclared reports whether h declares the body's length. The relayed
// head then promises the client exactly that many bytes, so nothing may
// change the body's length: no barrier Replace or recovery splice, and no
// in-band error event (net/http would cut the excess and the client would
// read a truncated body).
func lengthDeclared(h http.Header) bool { return h.Get("Content-Length") != "" }

// isEventStream reports whether h describes an SSE body.
func isEventStream(h http.Header) bool {
	mt, _, err := mime.ParseMediaType(h.Get("Content-Type"))
	return err == nil && mt == "text/event-stream"
}

// finishFor classifies a failed round trip or relay. ctx is the call's
// context: cancelled means the client left; past its deadline, or cancelled
// by a stream limit, means timeout. The context is read once: a limit firing
// between two reads must not turn a timeout into a client cancel.
func finishFor(ctx context.Context, err error) model.Finish {
	cause := context.Cause(ctx)
	var ctxErr error
	if cause != nil { // done: Err is now fixed
		ctxErr = ctx.Err()
	}
	var ne net.Error
	switch {
	case errors.Is(cause, errLimit):
		return model.FinishTimeout
	case errors.Is(err, ErrClientWrite), errors.Is(ctxErr, context.Canceled):
		return model.FinishClientCancel
	case errors.Is(ctxErr, context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded),
		errors.As(err, &ne) && ne.Timeout():
		return model.FinishTimeout
	}
	return model.FinishUpstreamError
}

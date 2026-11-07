package forward

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"

	"github.com/ethosagent/tollgate/internal/model"
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
}

// Result is how a forwarded call ended.
type Result struct {
	Status int          // upstream status relayed to the client; 0 if none
	Finish model.Finish // how the call ended
	// Cut means headers were sent and the body ended abnormally: the caller
	// must abort the connection so the client sees a cut stream, not a
	// clean end.
	Cut bool
}

// Forward sends req upstream under ctx and relays the response to w
// unchanged, flushing every read. A failure before any byte reaches the
// client returns ErrUpstreamTimeout or ErrUpstreamUnreachable for the caller
// to answer; a client that has gone away gets no error, only
// FinishClientCancel.
func Forward(ctx context.Context, rt http.RoundTripper, w http.ResponseWriter, req Request) (Result, error) {
	// req.Target passed Target, the one check every upstream URL goes
	// through, so the caller cannot steer this request (ADR 0007).
	out, err := http.NewRequestWithContext(ctx, req.Method, req.Target.String(), bytes.NewReader(req.Body))
	if err != nil {
		return Result{Finish: model.FinishUpstreamError}, ErrUpstreamUnreachable
	}
	out.Header = req.Header
	resp, err := rt.RoundTrip(out)
	if err != nil {
		f := finishFor(ctx, err)
		switch f {
		case model.FinishClientCancel:
			return Result{Finish: f}, nil
		case model.FinishTimeout:
			return Result{Finish: f}, ErrUpstreamTimeout
		}
		return Result{Finish: f}, ErrUpstreamUnreachable
	}
	defer resp.Body.Close()
	return relayResponse(ctx, w, resp), nil
}

var responseHeaders = NewHeaderFilter()

func relayResponse(ctx context.Context, w http.ResponseWriter, resp *http.Response) Result {
	res := Result{Status: resp.StatusCode, Finish: model.FinishOK}
	if resp.StatusCode >= http.StatusBadRequest {
		res.Finish = model.FinishUpstreamError
	}
	responseHeaders.CopyTo(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	if _, err := Relay(w, resp.Body); err != nil {
		res.Finish = finishFor(ctx, err)
		res.Cut = res.Finish != model.FinishClientCancel
	}
	return res
}

// finishFor classifies a failed round trip or relay. ctx is the call's
// context: cancelled means the client left; past its deadline means timeout.
func finishFor(ctx context.Context, err error) model.Finish {
	var ne net.Error
	switch {
	case errors.Is(err, ErrClientWrite), errors.Is(ctx.Err(), context.Canceled):
		return model.FinishClientCancel
	case errors.Is(ctx.Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded),
		errors.As(err, &ne) && ne.Timeout():
		return model.FinishTimeout
	}
	return model.FinishUpstreamError
}

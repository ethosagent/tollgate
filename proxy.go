package tollgate

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ethosagent/tollgate/internal/forward"
	"github.com/ethosagent/tollgate/internal/normalise"
	"github.com/ethosagent/tollgate/internal/resolve"
)

// outcome is what the access log line says about one call. It holds
// metadata only: never headers, bodies or credentials (PRIV-1, PRIV-2).
type outcome struct {
	start    time.Time
	callID   string
	tenant   string
	provider string
	status   int
	finish   Finish
}

// serveProxy runs the transparent pass-through pipeline: Auth, Admission,
// ProviderResolver, body cap, forward, relay. Normalising, sessions, hooks
// and metrics are added by later milestones (ARCHITECTURE.md section 4).
func (p *Proxy) serveProxy(w http.ResponseWriter, r *http.Request) {
	o := &outcome{start: time.Now(), callID: rand.Text(), finish: FinishOK}
	defer p.logCall(r.Context(), o)
	shape := normalise.ShapeForPath(r.URL.Path)

	tenant, err := p.opts.Auth.Authenticate(r.Context(), r)
	if err != nil {
		p.reject(w, o, shape, authStatus(err), "authentication failed")
		return
	}
	o.tenant = tenant.ID
	call := &Call{ID: o.callID, Tenant: tenant, Path: r.URL.Path, Header: forward.FilterHeaders(r.Header, p.ownHeaders()...)}
	release, err := p.opts.Admission.Acquire(r.Context(), tenant, call)
	if err != nil {
		w.Header().Set("Retry-After", retryAfter(err))
		p.reject(w, o, shape, http.StatusTooManyRequests, "too many requests")
		return
	}
	if release != nil {
		defer release()
	}
	prov, rest, err := p.opts.Providers.Resolve(r.Context(), tenant, r)
	if err != nil || prov.BaseURL == nil {
		p.reject(w, o, shape, resolveStatus(err), "unknown provider or upstream")
		return
	}
	o.provider = prov.Name
	body, status := p.readBody(w, r)
	if status != 0 {
		p.reject(w, o, shape, status, "request body rejected")
		return
	}
	p.forwardAndRelay(w, r, o, shape, upstreamURL(prov.BaseURL, rest, r.URL.RawQuery), call.Header, body)
}

// forwardAndRelay sends the request upstream and streams the response back.
func (p *Proxy) forwardAndRelay(w http.ResponseWriter, r *http.Request, o *outcome, shape normalise.ErrorShape, target *url.URL, hdr http.Header, body []byte) {
	ctx, cancel := context.WithTimeout(r.Context(), p.opts.Limits.MaxCallDuration)
	defer cancel()
	// The target comes from the ProviderResolver, which authorises any
	// caller-supplied override against Policy.AllowedUpstreams (ADR 0006).
	req := (&http.Request{
		Method:        r.Method,
		URL:           target,
		Header:        hdr,
		Body:          http.NoBody,
		ContentLength: int64(len(body)),
	}).WithContext(ctx)
	if len(body) > 0 {
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	resp, err := p.opts.Transport.RoundTrip(req)
	if err != nil {
		p.upstreamFailed(ctx, w, o, shape, err)
		return
	}
	defer resp.Body.Close()
	o.status = resp.StatusCode
	if resp.StatusCode >= http.StatusBadRequest {
		o.finish = FinishUpstreamError
	}
	for k, vs := range forward.FilterHeaders(resp.Header) {
		w.Header()[k] = vs
	}
	w.WriteHeader(resp.StatusCode)
	p.relay(ctx, w, o, resp.Body)
}

// relay streams the upstream body to the client, flushing every read.
func (p *Proxy) relay(ctx context.Context, w http.ResponseWriter, o *outcome, src io.Reader) {
	rc := http.NewResponseController(w)
	flush := func() error {
		if err := rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return err
		}
		return nil
	}
	_, err := forward.Relay(w, flush, src)
	switch {
	case err == nil:
	case errors.Is(err, forward.ErrClientWrite), errors.Is(ctx.Err(), context.Canceled):
		o.finish = FinishClientCancel // the client is gone: nothing to tell it
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		o.finish = FinishTimeout
		panic(http.ErrAbortHandler) // headers are sent: abort so the client sees a cut stream, not a clean end
	default:
		o.finish = FinishUpstreamError
		panic(http.ErrAbortHandler)
	}
}

// upstreamFailed answers a failed upstream round trip. A client that has gone
// away gets nothing.
func (p *Proxy) upstreamFailed(ctx context.Context, w http.ResponseWriter, o *outcome, shape normalise.ErrorShape, err error) {
	var ne net.Error
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		o.finish = FinishClientCancel
	case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne) && ne.Timeout():
		p.reject(w, o, shape, http.StatusGatewayTimeout, "upstream timed out")
		o.finish = FinishTimeout
	default:
		p.reject(w, o, shape, http.StatusBadGateway, "upstream unreachable")
		o.finish = FinishUpstreamError
	}
}

// readBody reads the request body under Limits.MaxRequestBody. A non-zero
// status means the request must be rejected with it.
func (p *Proxy) readBody(w http.ResponseWriter, r *http.Request) ([]byte, int) {
	limit := p.opts.Limits.MaxRequestBody
	if r.ContentLength > limit {
		return nil, http.StatusRequestEntityTooLarge
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		return nil, http.StatusRequestEntityTooLarge
	case err != nil:
		return nil, http.StatusBadRequest
	}
	return b, 0
}

func (p *Proxy) reject(w http.ResponseWriter, o *outcome, shape normalise.ErrorShape, status int, msg string) {
	o.status, o.finish = status, FinishRejected
	normalise.WriteError(w, shape, status, msg)
}

func (p *Proxy) logCall(ctx context.Context, o *outcome) {
	p.opts.Logger.LogAttrs(ctx, slog.LevelInfo, "proxy call",
		slog.String("call_id", o.callID), slog.String("tenant", o.tenant),
		slog.String("provider", o.provider), slog.Int("status", o.status),
		slog.String("finish", string(o.finish)),
		slog.Int64("duration_ms", time.Since(o.start).Milliseconds()))
}

func (p *Proxy) ownHeaders() []string {
	h := p.opts.Headers
	return []string{h.TenantKey, h.Session, h.UpstreamURL}
}

func upstreamURL(base *url.URL, rest, rawQuery string) *url.URL {
	u := *base
	u.Path = strings.TrimSuffix(base.Path, "/") + rest
	u.RawPath = ""
	u.RawQuery = rawQuery
	return &u
}

// authStatus is 403 when the error says so (HTTPStatus() int), else 401.
func authStatus(err error) int {
	var se interface{ HTTPStatus() int }
	if errors.As(err, &se) && se.HTTPStatus() == http.StatusForbidden {
		return http.StatusForbidden
	}
	return http.StatusUnauthorized
}

// retryAfter is the Retry-After value in whole seconds (at least 1).
func retryAfter(err error) string {
	var re interface{ RetryAfter() time.Duration }
	secs := int64(1)
	if errors.As(err, &re) {
		secs = max(secs, int64((re.RetryAfter()+time.Second-1)/time.Second))
	}
	return strconv.FormatInt(secs, 10)
}

func resolveStatus(err error) int {
	switch {
	case errors.Is(err, resolve.ErrUpstreamForbidden):
		return http.StatusForbidden
	case errors.Is(err, resolve.ErrBadUpstream):
		return http.StatusBadRequest
	}
	return http.StatusNotFound
}

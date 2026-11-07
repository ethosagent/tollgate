package tollgate

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/ethosagent/tollgate/internal/chain"
	"github.com/ethosagent/tollgate/internal/forward"
	"github.com/ethosagent/tollgate/internal/model"
	"github.com/ethosagent/tollgate/internal/normalise"
)

// serveProxy runs the pipeline: Auth, Admission, ProviderResolver, target
// check, body cap, then the call (hooks.go). Sessions are added by a later
// milestone (ARCHITECTURE.md section 4). Every extension call runs under
// chain.Within (recover plus a Limits deadline); extension errors map to a
// status through model.StatusOf (ADR 0008). Every call, whatever its path,
// ends in finishCall, which emits its one Metric.
func (p *Proxy) serveProxy(w http.ResponseWriter, r *http.Request) {
	o := p.newOutcome(w, r)
	defer p.finishCall(r.Context(), o)
	req, release, ok := p.prepare(r, o)
	if release != nil {
		defer release() // runs before finishCall: the slot is free before After
	}
	if ok {
		p.serveCall(r, o, req)
	}
}

// prepare runs the steps before the upstream call. ok is false when the
// request was answered; release, if not nil, must be called when the call ends.
func (p *Proxy) prepare(r *http.Request, o *outcome) (req forward.Request, release func(), ok bool) {
	l, call := p.opts.Limits, o.call
	tenant, err := chain.Within(r.Context(), l.ExtensionDeadline, func(ctx context.Context) (Tenant, error) {
		return p.opts.Auth.Authenticate(ctx, r)
	})
	if err != nil {
		p.fail(r, o, "auth", err, http.StatusUnauthorized)
		return req, nil, false
	}
	o.tenant = tenant.ID
	call.Tenant, call.Header, call.Meta = tenant, p.reqHeaders.Filter(r.Header), map[string]any{}
	release, err = chain.WithinLate(r.Context(), l.AdmissionDeadline, func(ctx context.Context) (func(), error) {
		return p.opts.Admission.Acquire(ctx, tenant, call)
	}, releaseLate)
	if err != nil {
		if !chain.IsPanic(err) {
			o.w.Header().Set("Retry-After", strconv.Itoa(model.RetryAfterSeconds(err)))
		}
		p.fail(r, o, "admission", err, http.StatusTooManyRequests)
		return req, nil, false
	}
	res, err := chain.Within(r.Context(), l.ExtensionDeadline, func(ctx context.Context) (resolved, error) {
		return p.resolve(ctx, r, tenant)
	})
	if err != nil {
		p.fail(r, o, "resolver", err, http.StatusNotFound)
		return req, release, false
	}
	o.provider, o.shape = res.prov.Name, normalise.ShapeForWire(res.prov.Wire)
	call.Provider = res.prov
	if res.target, err = p.target(r, tenant, res.prov, res.rest); err != nil {
		p.reject(o, model.StatusOf(err, http.StatusNotFound))
		return req, release, false
	}
	if call.Body, err = forward.ReadBody(o.w, r, l.MaxRequestBody); err != nil {
		p.reject(o, model.StatusOf(err, http.StatusBadRequest))
		return req, release, false
	}
	return forward.Request{Method: r.Method, Target: res.target, Header: call.Header, Body: call.Body}, release, true
}

// releaseLate frees a slot an Admission granted after its deadline: the call
// was already answered 503 and must not hold it.
func releaseLate(release func(), _ error) {
	if release != nil {
		_, _ = chain.Guard(context.Background(), func(context.Context) (struct{}, error) { release(); return struct{}{}, nil })
	}
}

// resolved is the ProviderResolver's answer plus the checked upstream URL.
type resolved struct {
	prov   Provider
	rest   string
	target *url.URL
}

func (p *Proxy) resolve(ctx context.Context, r *http.Request, t Tenant) (resolved, error) {
	prov, rest, err := p.opts.Providers.Resolve(ctx, t, r)
	return resolved{prov: prov, rest: rest}, err
}

// target returns the checked upstream URL. The override header is applied
// here for every resolver (ADR 0007).
func (p *Proxy) target(r *http.Request, t Tenant, prov Provider, rest string) (*url.URL, error) {
	var allow []string
	if t.Policy != nil {
		allow = t.Policy.AllowedUpstreams
	}
	override := strings.TrimSpace(r.Header.Get(p.opts.Headers.UpstreamURL))
	return forward.Target(prov.BaseURL, override, allow, r.URL, rest)
}

// fail answers an extension error. A panic (500) or a missed deadline (503)
// is logged with metadata only. An error that arrives after the client left
// (a queue wait cut short) writes nothing and is recorded as client_cancel,
// not as a rejection.
func (p *Proxy) fail(r *http.Request, o *outcome, seam string, err error, fallback int) {
	msg := ""
	switch {
	case chain.IsPanic(err):
		msg = "extension panicked"
	case r.Context().Err() != nil:
		o.finish = FinishClientCancel
		return
	case errors.Is(err, chain.ErrDeadline):
		msg = "extension timed out"
	}
	if msg != "" {
		p.opts.Logger.LogAttrs(context.WithoutCancel(r.Context()), slog.LevelError, msg,
			slog.String("seam", seam), slog.String("call_id", o.callID), slog.String("tenant", o.tenant))
	}
	p.reject(o, model.StatusOf(err, fallback))
}

// abortIfCut ends the handler with http.ErrAbortHandler for a response whose
// headers are sent but whose body ended abnormally: the client must see a cut
// stream, not a clean end.
func abortIfCut(res forward.Result) {
	if res.Cut {
		panic(http.ErrAbortHandler)
	}
}

// relay runs the upstream call under the stream limits (first byte, idle,
// max call duration) and writes the response to body: o.w, or a Capture
// that holds it for OnResponse. An error before any byte is answered on o.w.
// It is the one place a broken non-SSE body is detected; a broken SSE stream
// ends with an error event instead.
func (p *Proxy) relay(body http.ResponseWriter, r *http.Request, o *outcome, req forward.Request) forward.Result {
	l := p.opts.Limits
	req.Limits = forward.StreamLimits{FirstByte: l.FirstByteTimeout, Idle: l.IdleStreamTimeout, MaxCall: l.MaxCallDuration}
	req.Shape = o.shape
	res, err := forward.Forward(r.Context(), p.opts.Transport, body, req)
	if err != nil {
		p.reject(o, model.StatusOf(err, http.StatusBadGateway))
	} else if res.Status != 0 {
		o.status = res.Status
	}
	o.finish = res.Finish
	return res
}

// reject answers with a provider-shaped error whose message is fixed by the
// status alone (ADR 0008): no error text from an extension or the transport
// ever reaches the client.
func (p *Proxy) reject(o *outcome, status int) {
	o.status, o.finish = status, FinishRejected
	normalise.WriteError(o.w, o.shape, status, normalise.StatusMessage(status))
}

// nextCallID is a per-process random prefix plus a counter: unique without
// a crypto read per call.
func (p *Proxy) nextCallID() string {
	return p.idPrefix + strconv.FormatUint(p.callSeq.Add(1), 36)
}

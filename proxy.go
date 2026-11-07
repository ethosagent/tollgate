package tollgate

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ethosagent/tollgate/internal/chain"
	"github.com/ethosagent/tollgate/internal/forward"
	"github.com/ethosagent/tollgate/internal/model"
	"github.com/ethosagent/tollgate/internal/normalise"
	"github.com/ethosagent/tollgate/internal/resolve"
)

// outcome is what the access log line says about one call. It holds
// metadata only: never headers, bodies or credentials (PRIV-1, PRIV-2).
type outcome struct {
	start    time.Time
	callID   string
	shape    normalise.ErrorShape // error body shape; refined once the provider is known
	tenant   string
	provider string
	status   int
	finish   Finish
}

// serveProxy runs the transparent pass-through pipeline: Auth, Admission,
// ProviderResolver, target check, body cap, forward and relay. Normalising,
// sessions, hooks and metrics are added by later milestones
// (ARCHITECTURE.md section 4). Every extension call runs under chain.Guard;
// extension errors map to a status through model.StatusOf (ADR 0008).
func (p *Proxy) serveProxy(w http.ResponseWriter, r *http.Request) {
	o := &outcome{start: time.Now(), callID: p.nextCallID(), finish: FinishOK,
		shape: normalise.ShapeForWire(resolve.WireForPath(r.URL.Path))}
	defer p.logCall(r.Context(), o)

	tenant, err := chain.Guard(func() (Tenant, error) { return p.opts.Auth.Authenticate(r.Context(), r) })
	if err != nil {
		p.fail(w, r, o, "auth", err, http.StatusUnauthorized)
		return
	}
	o.tenant = tenant.ID
	call := &Call{ID: o.callID, Tenant: tenant, Path: r.URL.Path, Header: p.reqHeaders.Filter(r.Header)}
	release, err := chain.Guard(func() (func(), error) { return p.opts.Admission.Acquire(r.Context(), tenant, call) })
	if err != nil {
		if !chain.IsPanic(err) {
			w.Header().Set("Retry-After", strconv.Itoa(model.RetryAfterSeconds(err)))
		}
		p.fail(w, r, o, "admission", err, http.StatusTooManyRequests)
		return
	}
	if release != nil {
		defer release()
	}
	res, err := chain.Guard(func() (resolved, error) { return p.resolve(r, tenant) })
	if err != nil {
		p.fail(w, r, o, "resolver", err, http.StatusNotFound)
		return
	}
	o.provider, o.shape = res.prov.Name, normalise.ShapeForWire(res.prov.Wire)
	if res.target, err = p.target(r, tenant, res.prov, res.rest); err != nil {
		p.reject(w, o, model.StatusOf(err, http.StatusNotFound))
		return
	}
	body, err := forward.ReadBody(w, r, p.opts.Limits.MaxRequestBody)
	if err != nil {
		p.reject(w, o, model.StatusOf(err, http.StatusBadRequest))
		return
	}
	p.forward(w, r, o, forward.Request{Method: r.Method, Target: res.target, Header: call.Header, Body: body})
}

// resolved is the ProviderResolver's answer plus the checked upstream URL.
type resolved struct {
	prov   Provider
	rest   string
	target *url.URL
}

func (p *Proxy) resolve(r *http.Request, t Tenant) (resolved, error) {
	prov, rest, err := p.opts.Providers.Resolve(r.Context(), t, r)
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

// fail answers an extension error. A panic is logged with metadata only and
// answered 500. An error that arrives after the client left (a queue wait
// cut short) writes nothing and is recorded as client_cancel, not as a
// rejection.
func (p *Proxy) fail(w http.ResponseWriter, r *http.Request, o *outcome, seam string, err error, fallback int) {
	if chain.IsPanic(err) {
		p.opts.Logger.LogAttrs(context.WithoutCancel(r.Context()), slog.LevelError, "extension panicked",
			slog.String("seam", seam), slog.String("call_id", o.callID), slog.String("tenant", o.tenant))
	} else if r.Context().Err() != nil {
		o.finish = FinishClientCancel
		return
	}
	p.reject(w, o, model.StatusOf(err, fallback))
}

// forward runs the upstream call under the stream limits (first byte, idle,
// max call duration). It is the one place a broken non-SSE body is aborted;
// a broken SSE stream ends with an error event instead.
func (p *Proxy) forward(w http.ResponseWriter, r *http.Request, o *outcome, req forward.Request) {
	l := p.opts.Limits
	req.Limits = forward.StreamLimits{FirstByte: l.FirstByteTimeout, Idle: l.IdleStreamTimeout, MaxCall: l.MaxCallDuration}
	req.Shape = o.shape
	res, err := forward.Forward(r.Context(), p.opts.Transport, w, req)
	if err != nil {
		p.reject(w, o, model.StatusOf(err, http.StatusBadGateway))
	} else if res.Status != 0 {
		o.status = res.Status
	}
	o.finish = res.Finish
	if res.Cut {
		panic(http.ErrAbortHandler) // headers are sent: the client must see a cut stream, not a clean end
	}
}

// reject answers with a provider-shaped error whose message is fixed by the
// status alone (ADR 0008): no error text from an extension or the transport
// ever reaches the client.
func (p *Proxy) reject(w http.ResponseWriter, o *outcome, status int) {
	o.status, o.finish = status, FinishRejected
	normalise.WriteError(w, o.shape, status, normalise.StatusMessage(status))
}

// logCall writes the access line. The request context may already be
// cancelled (client gone), so it is detached: handlers that consult ctx must
// still record exactly those calls.
func (p *Proxy) logCall(ctx context.Context, o *outcome) {
	p.opts.Logger.LogAttrs(context.WithoutCancel(ctx), slog.LevelInfo, "proxy call",
		slog.String("call_id", o.callID), slog.String("tenant", o.tenant),
		slog.String("provider", o.provider), slog.Int("status", o.status),
		slog.String("finish", string(o.finish)),
		slog.Int64("duration_ms", time.Since(o.start).Milliseconds()))
}

// nextCallID is a per-process random prefix plus a counter: unique without
// a crypto read per call.
func (p *Proxy) nextCallID() string {
	return p.idPrefix + strconv.FormatUint(p.callSeq.Add(1), 36)
}

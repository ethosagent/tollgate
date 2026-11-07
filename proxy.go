package tollgate

import (
	"context"
	"errors"
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

// serveProxy runs the pipeline: Auth, Admission, ProviderResolver, target
// check, body cap, then forward and relay, with the hook chain around the
// forward when hooks are registered. Sessions and metrics are added by later
// milestones (ARCHITECTURE.md section 4). Every extension call runs under
// chain.Within (recover plus a Limits deadline); extension errors map to a status through model.StatusOf (ADR 0008).
func (p *Proxy) serveProxy(w http.ResponseWriter, r *http.Request) {
	chn := p.newChain() // the hooks of this call: later Use calls do not join it
	o := &outcome{start: time.Now(), callID: p.nextCallID(), finish: FinishOK,
		shape: normalise.ShapeForWire(resolve.WireForPath(r.URL.Path))}
	defer p.logCall(r.Context(), o)

	call, req, release, ok := p.prepare(w, r, o)
	if release != nil {
		defer release()
	}
	switch {
	case !ok:
	case chn.Empty():
		p.forward(w, r, o, req)
	default:
		p.serveHooked(w, r, o, call, req, chn)
	}
}

// prepare runs the steps before the upstream call. ok is false when the
// request was answered; release, if not nil, must be called when the call ends.
func (p *Proxy) prepare(w http.ResponseWriter, r *http.Request, o *outcome) (call *Call, req forward.Request, release func(), ok bool) {
	l := p.opts.Limits
	tenant, err := chain.Within(r.Context(), l.ExtensionDeadline, func(ctx context.Context) (Tenant, error) {
		return p.opts.Auth.Authenticate(ctx, r)
	})
	if err != nil {
		p.fail(w, r, o, "auth", err, http.StatusUnauthorized)
		return nil, req, nil, false
	}
	o.tenant = tenant.ID
	call = &Call{ID: o.callID, Tenant: tenant, Path: r.URL.Path, Header: p.reqHeaders.Filter(r.Header), Meta: map[string]any{}}
	admitted := call // a late Acquire must not read the named result, which returns rewrite
	release, err = chain.WithinLate(r.Context(), l.AdmissionDeadline, func(ctx context.Context) (func(), error) {
		return p.opts.Admission.Acquire(ctx, tenant, admitted)
	}, releaseLate)
	if err != nil {
		if !chain.IsPanic(err) {
			w.Header().Set("Retry-After", strconv.Itoa(model.RetryAfterSeconds(err)))
		}
		p.fail(w, r, o, "admission", err, http.StatusTooManyRequests)
		return nil, req, nil, false
	}
	res, err := chain.Within(r.Context(), l.ExtensionDeadline, func(ctx context.Context) (resolved, error) {
		return p.resolve(ctx, r, tenant)
	})
	if err != nil {
		p.fail(w, r, o, "resolver", err, http.StatusNotFound)
		return nil, req, release, false
	}
	o.provider, o.shape = res.prov.Name, normalise.ShapeForWire(res.prov.Wire)
	call.Provider = res.prov
	if res.target, err = p.target(r, tenant, res.prov, res.rest); err != nil {
		p.reject(w, o, model.StatusOf(err, http.StatusNotFound))
		return nil, req, release, false
	}
	if call.Body, err = forward.ReadBody(w, r, p.opts.Limits.MaxRequestBody); err != nil {
		p.reject(w, o, model.StatusOf(err, http.StatusBadRequest))
		return nil, req, release, false
	}
	return call, forward.Request{Method: r.Method, Target: res.target, Header: call.Header, Body: call.Body}, release, true
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
func (p *Proxy) fail(w http.ResponseWriter, r *http.Request, o *outcome, seam string, err error, fallback int) {
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
	p.reject(w, o, model.StatusOf(err, fallback))
}

// forward relays one call and, if its body broke after the headers went out,
// aborts the connection so the client sees a cut stream.
func (p *Proxy) forward(w http.ResponseWriter, r *http.Request, o *outcome, req forward.Request) {
	abortIfCut(p.relay(w, w, r, o, req))
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
// max call duration) and writes the response to body. An error before any byte
// is answered on w; the two differ only when a hook holds the response. It is
// the one place a broken non-SSE body is detected; a broken SSE stream ends
// with an error event instead.
func (p *Proxy) relay(w, body http.ResponseWriter, r *http.Request, o *outcome, req forward.Request) forward.Result {
	l := p.opts.Limits
	req.Limits = forward.StreamLimits{FirstByte: l.FirstByteTimeout, Idle: l.IdleStreamTimeout, MaxCall: l.MaxCallDuration}
	req.Shape = o.shape
	res, err := forward.Forward(r.Context(), p.opts.Transport, body, req)
	if err != nil {
		p.reject(w, o, model.StatusOf(err, http.StatusBadGateway))
	} else if res.Status != 0 {
		o.status = res.Status
	}
	o.finish = res.Finish
	return res
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

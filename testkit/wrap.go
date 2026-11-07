package testkit

import (
	"context"
	"net/http"
	"slices"

	"github.com/ethosagent/tollgate"
)

// The wrappers record each call, then delegate to the plugin under test and
// return exactly what it returned.

type authRec struct {
	next tollgate.Authenticator
	rec  *recorder
}

func (a authRec) Authenticate(ctx context.Context, r *http.Request) (tollgate.Tenant, error) {
	in := Invocation{Stage: StageAuthenticate,
		Request: &RequestInfo{Method: r.Method, Path: r.URL.Path, Header: a.rec.scrub(r.Header)}}
	defer func() { a.rec.add(in) }() // also when the plugin panics
	t, err := a.next.Authenticate(ctx, r)
	tc := cloneTenant(t)
	in.Tenant, in.Err = &tc, errText(err)
	return t, err
}

// ConsumedHeaders forwards the plugin's HeaderConsumer so its credential
// header is still stripped before the upstream sees it.
func (a authRec) ConsumedHeaders() []string {
	if hc, ok := a.next.(tollgate.HeaderConsumer); ok {
		return hc.ConsumedHeaders()
	}
	return nil
}

// hookBase is what every stage wrapper of one Hook shares. Each wrapper
// overrides exactly one stage, so the core offers it that stage and no other
// (see overrides); hookRecs registers one per stage the plugin implements.
type hookBase struct {
	tollgate.NoopHook
	next tollgate.Hook
	rec  *recorder
}

func (h hookBase) Name() string { return h.next.Name() }

type beforeRec struct{ hookBase }

func (h beforeRec) Before(ctx context.Context, c *tollgate.Call) (*tollgate.Response, error) {
	h.rec.add(Invocation{Stage: StageBefore, Plugin: h.Name(), Call: h.rec.cloneCall(c)}) // input, before the hook can change it
	return h.next.Before(ctx, c)
}

type eventRec struct{ hookBase }

func (h eventRec) OnEvent(ctx context.Context, c *tollgate.Call, e tollgate.Event) {
	h.rec.add(Invocation{Stage: StageOnEvent, Plugin: h.Name(), Call: h.rec.cloneCall(c), Event: cloneEvent(e)})
	h.next.OnEvent(ctx, c, e)
}

type responseRec struct{ hookBase }

func (h responseRec) OnResponse(ctx context.Context, c *tollgate.Call, r *tollgate.Response) (*tollgate.Response, error) {
	h.rec.add(Invocation{Stage: StageOnResponse, Plugin: h.Name(), Call: h.rec.cloneCall(c), Response: cloneResponse(r)})
	return h.next.OnResponse(ctx, c, r)
}

type afterRec struct{ hookBase }

func (h afterRec) After(ctx context.Context, c *tollgate.Call, m tollgate.Metric) {
	h.rec.add(Invocation{Stage: StageAfter, Plugin: h.Name(), Call: h.rec.cloneCall(c), Metric: &m})
	h.next.After(ctx, c, m)
}

type closedRec struct{ hookBase }

func (h closedRec) OnSessionClosed(ctx context.Context, s tollgate.Session) {
	h.rec.add(Invocation{Stage: StageSessionClosed, Plugin: h.Name(), Session: &s})
	h.next.OnSessionClosed(ctx, s)
}

// hookRecs returns one recording wrapper per stage h implements itself, in
// stage order. A method h only inherits from NoopHook gets none, as in the
// core, so it is neither offered nor recorded.
func hookRecs(h tollgate.Hook, rec *recorder) []tollgate.Hook {
	b := hookBase{next: h, rec: rec}
	var out []tollgate.Hook
	for _, s := range []struct {
		method string
		w      tollgate.Hook
	}{
		{"Before", beforeRec{b}}, {"OnEvent", eventRec{b}}, {"OnResponse", responseRec{b}},
		{"After", afterRec{b}}, {"OnSessionClosed", closedRec{b}},
	} {
		if overrides(h, s.method) {
			out = append(out, s.w)
		}
	}
	return out
}

type finRec struct {
	next tollgate.Finalizer
	rec  *recorder
}

func (f finRec) Name() string { return f.next.Name() }

func (f finRec) Decide(ctx context.Context, c *tollgate.Call, s tollgate.StreamSummary) tollgate.Decision {
	in := Invocation{Stage: StageDecide, Plugin: f.Name(), Call: f.rec.cloneCall(c), Summary: cloneSummary(s)}
	defer func() { f.rec.add(in) }() // also when the plugin panics
	d := f.next.Decide(ctx, c, s)
	in.Decision = cloneDecision(d)
	return d
}

type sinkRec struct {
	next tollgate.MetricSink
	rec  *recorder
}

func (s sinkRec) Write(ctx context.Context, batch []tollgate.Metric) error {
	in := Invocation{Stage: StageWrite, Batch: slices.Clone(batch)}
	defer func() { s.rec.add(in) }() // also when the plugin panics
	err := s.next.Write(ctx, batch)
	in.Err = errText(err)
	return err
}

package core_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/ethosagent/tollgate/internal/core"
	"github.com/ethosagent/tollgate/internal/fixtures"
	"github.com/ethosagent/tollgate/internal/model"
	"github.com/ethosagent/tollgate/internal/normalise"
)

// tHook is a Hook whose methods are test-supplied functions; a nil function
// is a no-op.
type tHook struct {
	model.NoopHook
	name    string
	before  func(context.Context, *model.Call) (*model.Response, error)
	onEvent func(context.Context, *model.Call, model.Event)
	onResp  func(context.Context, *model.Call, *model.Response) (*model.Response, error)
	after   func(context.Context, *model.Call, model.Metric)
}

func (h *tHook) Name() string { return h.name }

func (h *tHook) Before(ctx context.Context, c *model.Call) (*model.Response, error) {
	if h.before == nil {
		return nil, nil
	}
	return h.before(ctx, c)
}

func (h *tHook) OnEvent(ctx context.Context, c *model.Call, e model.Event) {
	if h.onEvent != nil {
		h.onEvent(ctx, c, e)
	}
}

func (h *tHook) OnResponse(ctx context.Context, c *model.Call, r *model.Response) (*model.Response, error) {
	if h.onResp == nil {
		return r, nil
	}
	return h.onResp(ctx, c, r)
}

func (h *tHook) After(ctx context.Context, c *model.Call, m model.Metric) {
	if h.after != nil {
		h.after(ctx, c, m)
	}
}

// newHookedProxy is newProxyServer with hooks registered before serving; it
// also returns the Proxy so a test can call Use or read counters.
func newHookedProxy(t testing.TB, opts core.Options, hooks ...model.Hook) (string, *core.Proxy) {
	t.Helper()
	if opts.Auth == nil {
		opts.Auth = allowLocal("acme")
	}
	p, err := core.New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p.Use(hooks...)
	return serve(t, p), p
}

// expectedEvents maps a stream fixture with the wire's mapper, frame by
// frame, as the proxy's consumer must.
func expectedEvents(t testing.TB, fixture string) []model.Event {
	t.Helper()
	wire := model.WireOpenAIChat
	if fixture[:9] == "anthropic" {
		wire = model.WireAnthropicMessages
	}
	w, err := normalise.ForWire(wire)
	if err != nil {
		t.Fatal(err)
	}
	data, err := fixtures.Bytes(fixture)
	if err != nil {
		t.Fatal(err)
	}
	m := w.NewEventMapper()
	var out []model.Event
	for _, frame := range bytes.Split(data, []byte("\n\n")) {
		if len(bytes.TrimSpace(frame)) == 0 {
			continue
		}
		name, d := normalise.SplitFrame(frame)
		evs, _ := m.Frame(name, d)
		out = append(out, evs...)
	}
	return append(out, m.Finish()...)
}

// slogTo returns a text logger writing to w.
func slogTo(w io.Writer) *slog.Logger { return slog.New(slog.NewTextHandler(w, nil)) }

// rawResolver sends every call to the upstream with a pass-through wire.
type rawResolver struct{ up *fakeUpstream }

func (r rawResolver) Resolve(_ context.Context, _ model.Tenant, req *http.Request) (model.Provider, string, error) {
	u, _ := url.Parse(r.up.URL)
	return model.Provider{Name: "raw", Wire: model.WirePassthrough, BaseURL: u}, strings.TrimPrefix(req.URL.Path, "/raw"), nil
}

// readAllRaw reads a body without decoding it (a gzip body stays gzip).
func readAllRaw(t testing.TB, resp *http.Response) []byte {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return b
}

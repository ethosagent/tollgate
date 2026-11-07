package chain

import (
	"bytes"
	"context"
	"log/slog"
	"sync"

	"github.com/ethosagent/tollgate/internal/model"
)

// hook is a model.Hook whose methods are test-supplied functions.
type hook struct {
	model.NoopHook
	name    string
	before  func(context.Context, *model.Call) (*model.Response, error)
	onEvent func(context.Context, *model.Call, model.Event)
	onResp  func(context.Context, *model.Call, *model.Response) (*model.Response, error)
	after   func(context.Context, *model.Call, model.Metric)
	closed  func(context.Context, model.Session)
}

func (h *hook) Name() string { return h.name }

func (h *hook) Before(ctx context.Context, c *model.Call) (*model.Response, error) {
	if h.before == nil {
		return nil, nil
	}
	return h.before(ctx, c)
}

func (h *hook) OnEvent(ctx context.Context, c *model.Call, e model.Event) {
	if h.onEvent != nil {
		h.onEvent(ctx, c, e)
	}
}

func (h *hook) OnResponse(ctx context.Context, c *model.Call, r *model.Response) (*model.Response, error) {
	if h.onResp == nil {
		return r, nil
	}
	return h.onResp(ctx, c, r)
}

func (h *hook) After(ctx context.Context, c *model.Call, m model.Metric) {
	if h.after != nil {
		h.after(ctx, c, m)
	}
}

func (h *hook) OnSessionClosed(ctx context.Context, s model.Session) {
	if h.closed != nil {
		h.closed(ctx, s)
	}
}

// logBuf is a goroutine-safe log sink.
type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func newLogger() (*slog.Logger, *logBuf) {
	b := &logBuf{}
	return slog.New(slog.NewTextHandler(b, nil)), b
}

func newCall(body string) *model.Call {
	return &model.Call{ID: "call-1", Tenant: model.Tenant{ID: "acme"}, Body: []byte(body)}
}

package tollgate

import (
	"context"
	"log/slog"
	"sync"

	"github.com/ethosagent/tollgate/internal/chain"
	"github.com/ethosagent/tollgate/internal/session"
)

// sessionState is the Proxy's session machinery. def is the default strategy
// (nil when the host supplied Options.Sessions). The goroutine that turns
// Closed() into OnSessionClosed starts when the first hook that implements
// OnSessionClosed is added, so a proxy without one starts nothing.
type sessionState struct {
	def *session.Strategy

	mu      sync.Mutex // guards the fields below
	started bool
	closed  bool
	cancel  context.CancelFunc
	done    chan struct{}
}

// initSessions installs the default strategy unless the host supplied one.
func (p *Proxy) initSessions() {
	if p.opts.Sessions != nil {
		return
	}
	l := p.opts.Limits
	p.sessions.def = session.New(session.Config{TTL: l.SessionTTL, Max: l.MaxSessions,
		MeasurePrediction: p.opts.MeasureSessionPrediction})
	p.opts.Sessions = p.sessions.def
}

// attributeSession sets Call.Session from the SessionStrategy. It runs once
// per call after the request is parsed, so a prediction sees the messages;
// a call with neither a session header nor messages has nothing to go on. A
// strategy that panics or runs past its deadline leaves the call without a
// session: it is still served.
func (p *Proxy) attributeSession(ctx context.Context, o *outcome) {
	c := o.call
	if c.SessionHeader == "" && len(c.Messages) == 0 {
		return
	}
	resolve := func(ctx context.Context) (Session, error) {
		return p.opts.Sessions.Resolve(ctx, c.Tenant, c), nil
	}
	var s Session
	var err error
	if p.sessions.def != nil { // in-process and non-blocking: no goroutine or timer per call
		s, err = chain.Guard(ctx, resolve)
	} else {
		s, err = chain.Within(ctx, p.opts.Limits.ExtensionDeadline, resolve)
	}
	if err != nil {
		p.opts.Logger.LogAttrs(ctx, slog.LevelError, "session strategy failed; call has no session",
			slog.String("call_id", o.callID), slog.String("tenant", o.tenant))
		return
	}
	c.Session = s
}

// startSessionConsumer starts the goroutine that runs OnSessionClosed for each
// session the strategy closes, once. It does nothing after Close.
func (p *Proxy) startSessionConsumer() {
	st := &p.sessions
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.started || st.closed {
		return
	}
	ch, err := chain.Guard(context.Background(), func(context.Context) (<-chan Session, error) {
		return p.opts.Sessions.Closed(), nil
	})
	if err != nil {
		p.opts.Logger.LogAttrs(context.Background(), slog.LevelError, "session strategy Closed failed; OnSessionClosed will not run")
		return
	}
	var ctx context.Context
	ctx, st.cancel = context.WithCancel(context.Background())
	st.started, st.done = true, make(chan struct{})
	go p.consumeClosed(ctx, ch, st.done)
}

func (p *Proxy) consumeClosed(ctx context.Context, ch <-chan Session, done chan<- struct{}) {
	defer close(done)
	for {
		select {
		case <-ctx.Done():
			return
		case s, ok := <-ch:
			if !ok {
				return
			}
			p.chain.Load().SessionClosed(ctx, s) // guarded and bounded per hook
		}
	}
}

// closeSessions stops the default strategy's sweeper and the consumer
// goroutine, waiting for it until ctx ends.
func (p *Proxy) closeSessions(ctx context.Context) error {
	st := &p.sessions
	st.mu.Lock()
	st.closed = true
	cancel, done := st.cancel, st.done
	st.mu.Unlock()
	if st.def != nil {
		st.def.Close()
	}
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// sessionStatus returns the default store's counters (the sessions section
// of /statusz), or nil for a host strategy (its internals are its own).
func (p *Proxy) sessionStatus() *session.Stats {
	if p.sessions.def == nil {
		return nil
	}
	s := p.sessions.def.Stats()
	return &s
}

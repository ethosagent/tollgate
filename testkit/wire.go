package testkit

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethosagent/tollgate"
)

// wired is the Options and hooks built from the plugins under test.
type wired struct {
	auth       tollgate.Authenticator
	sink       tollgate.MetricSink
	finalizers []tollgate.Finalizer
	hooks      []tollgate.Hook
	closes     bool // some hook implements OnSessionClosed itself
}

// wirePlugins wraps each plugin in a recorder for every interface it
// implements. It fails t for a value that implements none, and for a second
// Authenticator or MetricSink: tollgate takes one of each, so the harness
// would otherwise run one of them silently untested. Hooks and Finalizers
// may be several, as in Options and Proxy.Use.
func wirePlugins(t testing.TB, plugins []any, rec *recorder) wired {
	t.Helper()
	for _, n := range baseCredentials {
		rec.creds[n] = true
	}
	rec.creds[strings.ToLower(tollgate.DefaultHeaderNames().TenantKey)] = true
	var w wired
	for _, pl := range plugins {
		used, err := w.add(pl, rec)
		if err != nil {
			t.Fatalf("testkit.Run: %v", err)
		}
		if !used {
			t.Fatalf("testkit.Run: %T is not an Authenticator, Hook, Finalizer or MetricSink", pl)
		}
	}
	return w
}

// add wires one plugin into every seam it implements.
func (w *wired) add(pl any, rec *recorder) (used bool, err error) {
	if a, ok := pl.(tollgate.Authenticator); ok {
		if w.auth != nil {
			return false, fmt.Errorf("%T is a second Authenticator; tollgate takes one (Options.Auth): test them in separate runs", pl)
		}
		w.auth, used = authRec{a, rec}, true
		if hc, ok := pl.(tollgate.HeaderConsumer); ok {
			for _, h := range hc.ConsumedHeaders() {
				rec.creds[strings.ToLower(h)] = true
			}
		}
	}
	if s, ok := pl.(tollgate.MetricSink); ok {
		if w.sink != nil {
			return false, fmt.Errorf("%T is a second MetricSink; tollgate takes one (Options.Metrics): test them in separate runs", pl)
		}
		w.sink, used = sinkRec{s, rec}, true
	}
	if h, ok := pl.(tollgate.Hook); ok {
		w.hooks, used = append(w.hooks, hookRecs(h, rec)...), true
		w.closes = w.closes || overrides(h, "OnSessionClosed")
	}
	if f, ok := pl.(tollgate.Finalizer); ok {
		w.finalizers, used = append(w.finalizers, finRec{f, rec}), true
	}
	return used, nil
}

// fixedResolver sends every call to one upstream, picking the wire format from
// the path prefix like the default resolver (/openai, /anthropic). Going
// through the resolver, not the upstream-override header, means an
// Authenticator under test needs no Policy.AllowedUpstreams.
type fixedResolver struct{ base *url.URL }

func (r fixedResolver) Resolve(_ context.Context, _ tollgate.Tenant, req *http.Request) (tollgate.Provider, string, error) {
	name, rest, _ := strings.Cut(strings.TrimPrefix(req.URL.Path, "/"), "/")
	wire := tollgate.WireOpenAIChat
	if name == "anthropic" {
		wire = tollgate.WireAnthropicMessages
	}
	return tollgate.Provider{Name: name, Wire: wire, BaseURL: r.base}, "/" + rest, nil
}

// sessions is a SessionStrategy that attributes calls by the session header
// and, unlike the default, can be told to close every session at once, so
// OnSessionClosed is delivered without waiting out a TTL.
type sessions struct {
	mu   sync.Mutex
	live map[string]*tollgate.Session
	out  chan tollgate.Session
	sent int
}

func newSessions() *sessions {
	return &sessions{live: map[string]*tollgate.Session{}, out: make(chan tollgate.Session, 16)}
}

func (s *sessions) Resolve(_ context.Context, t tollgate.Tenant, c *tollgate.Call) tollgate.Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := t.ID + "\x00" + c.SessionHeader
	now := time.Now()
	sess := s.live[key]
	if sess == nil {
		sess = &tollgate.Session{ID: c.SessionHeader, TenantID: t.ID, Source: tollgate.SessionFromHeader, StartedAt: now}
		s.live[key] = sess
	}
	sess.Calls++
	sess.LastSeen = now
	return *sess
}

func (s *sessions) Closed() <-chan tollgate.Session { return s.out }

// closeAll reports every live session closed, oldest key first.
func (s *sessions) closeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]string, 0, len(s.live))
	for k := range s.live {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		s.out <- *s.live[k]
		s.sent++
		delete(s.live, k)
	}
}

// emitted is how many sessions closeAll has reported.
func (s *sessions) emitted() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sent
}

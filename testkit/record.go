package testkit

import (
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/ethosagent/tollgate"
)

// Stage names one plugin entry point the harness can record.
type Stage string

// Stages a Recording can hold.
const (
	StageAuthenticate  Stage = "Authenticate"    // Authenticator.Authenticate
	StageBefore        Stage = "Before"          // Hook.Before
	StageOnEvent       Stage = "OnEvent"         // Hook.OnEvent
	StageOnResponse    Stage = "OnResponse"      // Hook.OnResponse
	StageDecide        Stage = "Decide"          // Finalizer.Decide
	StageAfter         Stage = "After"           // Hook.After
	StageWrite         Stage = "Write"           // MetricSink.Write
	StageSessionClosed Stage = "OnSessionClosed" // Hook.OnSessionClosed
)

// RequestInfo is the inbound request an Authenticator saw.
type RequestInfo struct {
	Method string
	Path   string
	// Header is a copy of the request headers without credentials:
	// Authorization, Proxy-Authorization, X-Api-Key, Cookie, the tenant key
	// header and any header the plugin declares through HeaderConsumer.
	Header http.Header
}

// Invocation is one call a plugin received, with copies of its arguments and,
// where the plugin returns something, of what it returned. Only the fields of
// its Stage are set. A Hook is offered, and recorded at, only the stages it
// implements itself, as in the core: a method it inherits from an embedded
// tollgate.NoopHook is never called.
type Invocation struct {
	Seq    int    // 1-based arrival order across the whole Recording
	Stage  Stage  // which entry point was called
	Plugin string // Name() of the Hook or Finalizer; "" for the others

	Request  *RequestInfo            // Authenticate
	Call     *tollgate.Call          // Before, OnEvent, OnResponse, After, Decide
	Event    *tollgate.Event         // OnEvent
	Response *tollgate.Response      // OnResponse: the buffered response it was given
	Metric   *tollgate.Metric        // After
	Batch    []tollgate.Metric       // Write
	Session  *tollgate.Session       // OnSessionClosed
	Summary  *tollgate.StreamSummary // Decide

	Tenant   *tollgate.Tenant   // Authenticate: the tenant returned
	Decision *tollgate.Decision // Decide: the verdict returned
	Err      string             // Authenticate, Write: the error returned, "" if none
}

// Reply is what the client received for one fixture.
type Reply struct {
	Fixture string
	Status  int
	Body    []byte
}

// Recording is everything a Run observed. Calls are in arrival order and hold
// copies, so a plugin that mutates its arguments does not change the record.
type Recording struct {
	Calls   []Invocation
	Replies []Reply // one per fixture, in order
}

// Stages returns the stage of every call, in order.
func (r *Recording) Stages() []Stage {
	out := make([]Stage, len(r.Calls))
	for i, c := range r.Calls {
		out[i] = c.Stage
	}
	return out
}

// Of returns the calls of one stage, in order.
func (r *Recording) Of(s Stage) []Invocation {
	var out []Invocation
	for _, c := range r.Calls {
		if c.Stage == s {
			out = append(out, c)
		}
	}
	return out
}

// recorder collects Invocations from the goroutines the proxy calls plugins on.
type recorder struct {
	mu    sync.Mutex
	calls []Invocation
	creds map[string]bool // lower-case header names left out of copies
}

func (r *recorder) add(inv Invocation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	inv.Seq = len(r.calls) + 1
	r.calls = append(r.calls, inv)
}

func (r *recorder) count(s Stage) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.calls {
		if c.Stage == s {
			n++
		}
	}
	return n
}

func (r *recorder) snapshot() []Invocation {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

// scrub copies h without credential headers.
func (r *recorder) scrub(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for k, v := range h {
		if !r.creds[strings.ToLower(k)] {
			out[k] = slices.Clone(v)
		}
	}
	return out
}

// baseCredentials are never recorded, whatever the plugin declares.
var baseCredentials = []string{"authorization", "proxy-authorization", "x-api-key", "cookie", "set-cookie"}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

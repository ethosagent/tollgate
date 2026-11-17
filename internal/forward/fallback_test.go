package forward

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/ethosagent/tollgate/internal/model"
)

const (
	origBody    = `{"model":"big"}`
	changedBody = `{"model":"small"}`
	refusedBody = "REFUSED: the rewritten request is not supported"
	okBody      = "data: {\"ok\":true}\n\n"
)

// refusingUpstream answers refusals for the bodies in refuse and 200 for any
// other, recording every body it got. ct is the 200's media type.
type refusingUpstream struct {
	mu     sync.Mutex
	bodies []string
}

func (u *refusingUpstream) server(t *testing.T, ct string, refuse ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.bodies = append(u.bodies, string(b))
		u.mu.Unlock()
		for _, s := range refuse {
			if string(b) == s {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, refusedBody)
				return
			}
		}
		w.Header().Set("Content-Type", ct)
		_, _ = io.WriteString(w, okBody)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (u *refusingUpstream) got() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.bodies...)
}

// TestForward_FallbackToTheOriginalBody: a refusal in the configured set of
// a body a hook changed is answered by resending the original once; any
// other case is one request and the client sees the first response.
func TestForward_FallbackToTheOriginalBody(t *testing.T) {
	tests := []struct {
		name       string
		fallback   []byte // the original body; nil = unchanged
		on         []int
		refuse     []string
		wantBodies []string
		wantStatus int
		wantClient string
		wantFrom   int
	}{
		{"status in set, body changed", []byte(origBody), []int{400}, []string{changedBody},
			[]string{changedBody, origBody}, 200, okBody, 400},
		{"status not in set", []byte(origBody), []int{422}, []string{changedBody},
			[]string{changedBody}, 400, refusedBody, 0},
		{"body unchanged", nil, []int{400}, []string{changedBody},
			[]string{changedBody}, 400, refusedBody, 0},
		{"setting off", []byte(origBody), nil, []string{changedBody},
			[]string{changedBody}, 400, refusedBody, 0},
		{"fallback refused too is relayed as-is", []byte(origBody), []int{400}, []string{changedBody, origBody},
			[]string{changedBody, origBody}, 400, refusedBody, 400},
		{"success is never resent", []byte(origBody), []int{400}, nil,
			[]string{changedBody}, 200, okBody, 0},
	}
	for _, ct := range []string{"text/event-stream", "application/json"} {
		for _, tt := range tests {
			t.Run(ct+"/"+tt.name, func(t *testing.T) {
				up := &refusingUpstream{}
				srv := up.server(t, ct, tt.refuse...)
				target, _ := url.Parse(srv.URL + "/v1/chat/completions")
				rec := httptest.NewRecorder()
				res, err := Forward(context.Background(), srv.Client().Transport, rec, Request{
					Method: http.MethodPost, Target: target, Header: http.Header{"Authorization": {"Bearer k"}},
					Body: []byte(changedBody), FallbackBody: tt.fallback, FallbackOn: tt.on,
				})
				if err != nil || res.Status != tt.wantStatus || res.FallbackFrom != tt.wantFrom {
					t.Fatalf("res=%+v err=%v", res, err)
				}
				if got := up.got(); strings.Join(got, "|") != strings.Join(tt.wantBodies, "|") {
					t.Fatalf("upstream got %q, want %q", got, tt.wantBodies)
				}
				if rec.Code != tt.wantStatus || rec.Body.String() != tt.wantClient {
					t.Fatalf("client got %d %q, want %d %q", rec.Code, rec.Body.String(), tt.wantStatus, tt.wantClient)
				}
			})
		}
	}
}

// closeBody records whether it was closed.
type closeBody struct {
	io.Reader
	closed bool
}

func (b *closeBody) Close() error { b.closed = true; return nil }

// TestForward_FallbackClosesTheRefusedBody: the refused response is closed
// and none of its bytes reach the client; the second request keeps the
// headers (credentials verbatim) and carries the original body.
func TestForward_FallbackClosesTheRefusedBody(t *testing.T) {
	refused := &closeBody{Reader: strings.NewReader(refusedBody)}
	var calls []*http.Request
	var bodies []string
	rt := rtFunc(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		calls, bodies = append(calls, r), append(bodies, string(b))
		if len(calls) == 1 {
			return &http.Response{StatusCode: 400, Header: http.Header{"X-First": {"1"}}, Body: refused}, nil
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(okBody))}, nil
	})
	target, _ := url.Parse("http://upstream/v1")
	rec := httptest.NewRecorder()
	res, err := Forward(context.Background(), rt, rec, Request{Method: http.MethodPost, Target: target,
		Header: http.Header{"Authorization": {"Bearer k"}}, Body: []byte(changedBody),
		FallbackBody: []byte(origBody), FallbackOn: []int{400}})
	if err != nil || res.Status != 200 || res.Finish != model.FinishOK || res.FallbackFrom != 400 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if !refused.closed {
		t.Fatal("refused body was not closed")
	}
	if rec.Body.String() != okBody || rec.Header().Get("X-First") != "" {
		t.Fatalf("client got %q %v", rec.Body.String(), rec.Header())
	}
	if len(calls) != 2 || bodies[1] != origBody || calls[1].Header.Get("Authorization") != "Bearer k" {
		t.Fatalf("calls=%d bodies=%q", len(calls), bodies)
	}
}

// TestForward_FallbackTransportFailure: the resend failing before any byte
// is the same 502 as a first attempt failing, and still reports the refusal.
func TestForward_FallbackTransportFailure(t *testing.T) {
	n := 0
	rt := rtFunc(func(*http.Request) (*http.Response, error) {
		if n++; n == 1 {
			return &http.Response{StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(refusedBody))}, nil
		}
		return nil, errors.New("refused")
	})
	target, _ := url.Parse("http://upstream/v1")
	rec := httptest.NewRecorder()
	res, err := Forward(context.Background(), rt, rec, Request{Method: http.MethodPost, Target: target,
		Body: []byte(changedBody), FallbackBody: []byte(origBody), FallbackOn: []int{400}})
	if !errors.Is(err, ErrUpstreamUnreachable) || res.FallbackFrom != 400 || rec.Body.Len() != 0 {
		t.Fatalf("res=%+v err=%v client=%q", res, err, rec.Body.String())
	}
}

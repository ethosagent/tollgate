package core_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ethosagent/tollgate/internal/core"
	"github.com/ethosagent/tollgate/internal/model"
)

const fbOriginal = `{"model":"big","messages":[{"role":"user","content":"hi"}]}`

// modelRefuser answers 400 to any body naming the "small" model and 200
// otherwise, counting the bodies it got.
func modelRefuser(t *testing.T) (url string, bodies func() []string) {
	t.Helper()
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if bytes.Contains(b, []byte(`"small"`)) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"refused"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"ok","choices":[]}`)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), got...) }
}

func rewriteModel(to string) *tHook {
	return &tHook{name: "router", before: func(_ context.Context, c *model.Call) (*model.Response, error) {
		c.Body = bytes.ReplaceAll(c.Body, []byte(`"big"`), []byte(`"`+to+`"`))
		return nil, nil
	}}
}

// TestProxy_FallbackToTheOriginalRequest: with FallbackStatuses set, a
// refused rewritten body is resent as the client sent it and the Metric says
// so; off, or with the body unchanged, the refusal is the answer.
func TestProxy_FallbackToTheOriginalRequest(t *testing.T) {
	tests := []struct {
		name       string
		on         []int
		to         string // the model the hook rewrites to
		wantSent   int
		wantStatus int
		wantFrom   int
	}{
		{"on, body changed, refused", []int{400}, "small", 2, 200, 400},
		{"off", nil, "small", 1, 400, 0},
		{"on, body unchanged", []int{400}, "big", 1, 200, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			up, bodies := modelRefuser(t)
			base, rec, _ := metricProxy(t, core.Options{FallbackStatuses: tt.on}, rewriteModel(tt.to))
			resp := post(t, base, "/openai/v1/chat/completions", []byte(fbOriginal), map[string]string{"x-upstream-url": up})
			readAll(t, resp)
			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("client status %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			if got := bodies(); len(got) != tt.wantSent || (tt.wantSent == 2 && got[1] != fbOriginal) {
				t.Fatalf("upstream got %q", got)
			}
			m := waitMetric(t, rec)
			if m.Status != tt.wantStatus || m.Fallback != (tt.wantFrom != 0) || m.FallbackFromStatus != tt.wantFrom {
				t.Fatalf("metric = %+v", m)
			}
		})
	}
}

func waitMetric(t *testing.T, rec *metrics) model.Metric {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if ms := rec.all(); len(ms) > 0 {
			return ms[0]
		}
	}
	t.Fatal("no metric")
	return model.Metric{}
}

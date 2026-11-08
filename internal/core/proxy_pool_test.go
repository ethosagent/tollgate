package core_test

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ethosagent/tollgate/internal/core"
)

// TestProxy_DefaultPoolReusesConnectionsUpToTheCap: with default Limits, two
// rounds of 40 concurrent streams (over the old idle cap of 20, under
// MaxConnsPerHost) dial the upstream 40 times, not 60: the second round
// reuses every connection the first one opened.
func TestProxy_DefaultPoolReusesConnectionsUpToTheCap(t *testing.T) {
	const streams = 40
	var dials atomic.Int64
	var arrived sync.WaitGroup
	up := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		arrived.Done()
		arrived.Wait() // every stream of the round is open at once
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	up.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			dials.Add(1)
		}
	}
	up.Start()
	t.Cleanup(up.Close)
	base := newProxyServer(t, core.Options{})
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: streams}}
	t.Cleanup(client.CloseIdleConnections)
	for round := range 2 {
		arrived.Add(streams)
		var wg sync.WaitGroup
		for range streams {
			wg.Go(func() {
				req, _ := http.NewRequest(http.MethodPost, base+"/openai/v1/chat/completions", strings.NewReader(`{"stream":true}`))
				req.Header.Set("x-upstream-url", up.URL)
				resp, err := client.Do(req)
				if err != nil {
					t.Error(err)
					return
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			})
		}
		wg.Wait()
		if got := dials.Load(); got != streams {
			t.Fatalf("after round %d: %d upstream connections dialled, want %d (reused, not churned)", round+1, got, streams)
		}
	}
}

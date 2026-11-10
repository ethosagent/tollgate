package core_test

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/ethosagent/tollgate/internal/core"
)

// TestProxy_AcceptEncodingIsTheClients: the proxy never adds Accept-Encoding
// upstream (transport compression is off) and forwards the client's own value
// verbatim. A Go client adds "gzip" by itself, so the "none" case must use a
// client with compression disabled; otherwise a probe sees the client's
// header and blames the proxy.
func TestProxy_AcceptEncodingIsTheClients(t *testing.T) {
	tests := []struct {
		name, send, want string
	}{
		{"client sent none: upstream sees none", "", ""},
		{"client sent br: forwarded", "br", "br"},
		{"client sent gzip: forwarded", "gzip", "gzip"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			up := newFakeUpstream(t, upstreamOptions{})
			base := newProxyServer(t, core.Options{})
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, base+chatPath, bytes.NewReader([]byte(plainRequest)))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("x-upstream-url", up.URL)
			if tt.send != "" {
				req.Header.Set("Accept-Encoding", tt.send)
			}
			cl := &http.Client{Transport: &http.Transport{DisableCompression: true}}
			t.Cleanup(cl.CloseIdleConnections)
			resp, err := cl.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = readAll(t, resp)
			rs := up.Requests()
			if len(rs) != 1 {
				t.Fatalf("upstream saw %d requests, want 1", len(rs))
			}
			if got := rs[0].Header.Get("Accept-Encoding"); got != tt.want {
				t.Fatalf("upstream Accept-Encoding = %q, want %q", got, tt.want)
			}
		})
	}
}

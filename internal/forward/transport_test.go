package forward

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewTransport_AppliesConfig(t *testing.T) {
	cfg := TransportConfig{
		DialTimeout:         3 * time.Second,
		FirstByteTimeout:    7 * time.Second,
		MaxConnsPerHost:     11,
		MaxIdleConnsPerHost: 5,
		IdleConnTimeout:     13 * time.Second,
	}
	tr := NewTransport(cfg)
	if tr.MaxConnsPerHost != 11 || tr.MaxIdleConnsPerHost != 5 || tr.MaxIdleConns != 40 {
		t.Fatalf("pool sizes not applied: %+v", tr)
	}
	if tr.IdleConnTimeout != 13*time.Second || tr.ResponseHeaderTimeout != 7*time.Second {
		t.Fatalf("timeouts not applied: idle=%v ttfb=%v", tr.IdleConnTimeout, tr.ResponseHeaderTimeout)
	}
	if !tr.ForceAttemptHTTP2 {
		t.Fatal("HTTP/2 must be attempted")
	}
}

func TestNewTransport_RoundTrips(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	defer up.Close()

	tr := NewTransport(TransportConfig{DialTimeout: time.Second, FirstByteTimeout: time.Second, MaxConnsPerHost: 1, MaxIdleConnsPerHost: 1, IdleConnTimeout: time.Second})
	defer tr.CloseIdleConnections()
	req, err := http.NewRequest(http.MethodGet, up.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTeapot {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

// TestNewTransport_NeverAddsCompression proves transparency: Go's transport
// must not add Accept-Encoding: gzip and silently decode the upstream body.
func TestNewTransport_NeverAddsCompression(t *testing.T) {
	got := make(chan string, 1)
	up := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("Accept-Encoding")
	}))
	defer up.Close()
	tr := NewTransport(TransportConfig{DialTimeout: time.Second, FirstByteTimeout: time.Second, MaxConnsPerHost: 1, MaxIdleConnsPerHost: 1, IdleConnTimeout: time.Second})
	defer tr.CloseIdleConnections()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, up.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if ae := <-got; ae != "" {
		t.Fatalf("transport added Accept-Encoding %q", ae)
	}
}

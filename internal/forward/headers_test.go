package forward

import (
	"net/http"
	"testing"
)

func TestFilterHeaders(t *testing.T) {
	src := http.Header{}
	for k, v := range map[string]string{
		"Authorization":       "Bearer sk-1",
		"X-Api-Key":           "ak-1",
		"Content-Type":        "application/json",
		"X-Proxy-Key":         "tenant",
		"Connection":          "keep-alive, X-Hop",
		"X-Hop":               "1",
		"Keep-Alive":          "timeout=5",
		"Te":                  "trailers",
		"Upgrade":             "websocket",
		"Proxy-Authorization": "Basic x",
		"Transfer-Encoding":   "chunked",
	} {
		src.Set(k, v)
	}
	got := FilterHeaders(src, "x-proxy-key")

	for k, want := range map[string]string{"Authorization": "Bearer sk-1", "X-Api-Key": "ak-1", "Content-Type": "application/json"} {
		if got.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, got.Get(k), want)
		}
	}
	for _, k := range []string{"X-Proxy-Key", "Connection", "X-Hop", "Keep-Alive", "Te", "Upgrade", "Proxy-Authorization", "Transfer-Encoding"} {
		if got.Get(k) != "" {
			t.Errorf("%s survived: %q", k, got.Get(k))
		}
	}
	if src.Get("X-Proxy-Key") == "" || src.Get("Connection") == "" {
		t.Error("FilterHeaders modified its input")
	}
}

func TestFilterHeaders_NilInput(t *testing.T) {
	if got := FilterHeaders(nil); got == nil || len(got) != 0 {
		t.Fatalf("got %v, want empty non-nil header", got)
	}
}

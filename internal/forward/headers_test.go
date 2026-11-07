package forward

import (
	"net/http"
	"testing"
)

func TestHeaderFilter(t *testing.T) {
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
	got := NewHeaderFilter("x-proxy-key").Filter(src)

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
		t.Error("Filter modified its input")
	}
	got["Authorization"][0] = "changed"
	if src.Get("Authorization") != "Bearer sk-1" {
		t.Error("Filter aliased its input's value slices")
	}
}

func TestHeaderFilter_CopyToAppends(t *testing.T) {
	dst := http.Header{"Vary": {"Origin"}}
	NewHeaderFilter().CopyTo(dst, http.Header{"Vary": {"Accept"}, "Keep-Alive": {"x"}})
	if len(dst["Vary"]) != 2 || dst.Get("Keep-Alive") != "" {
		t.Fatalf("dst = %v", dst)
	}
}

func TestHeaderFilter_NilInput(t *testing.T) {
	if got := NewHeaderFilter().Filter(nil); got == nil || len(got) != 0 {
		t.Fatalf("got %v, want empty non-nil header", got)
	}
}

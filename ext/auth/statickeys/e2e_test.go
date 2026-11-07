package statickeys_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethosagent/tollgate"
	"github.com/ethosagent/tollgate/ext/auth/statickeys"
	"github.com/ethosagent/tollgate/testkit"
)

// proxyWith serves a tollgate proxy whose Auth is a statickeys table with one
// key reading header; opts.Headers stay at their defaults.
func proxyWith(t *testing.T, header string) string {
	t.Helper()
	auth, err := statickeys.New(statickeys.Config{Header: header, Keys: map[string]statickeys.Tenant{
		"tenant-secret": {ID: "a", AllowedUpstreams: []string{"127.0.0.0/8"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := tollgate.New(tollgate.Options{Auth: auth})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(p.Handler())
	t.Cleanup(srv.Close)
	return srv.URL
}

func call(t *testing.T, base, upstream string, hdr map[string]string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, base+"/openai/v1/chat/completions", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("x-upstream-url", upstream)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

func TestAuth_WorksWithTollgate(t *testing.T) {
	up := testkit.NewFakeUpstream(t, testkit.Options{})
	base := proxyWith(t, "")
	tests := []struct {
		name string
		hdr  map[string]string
		want int
	}{
		{"missing key is 401", nil, http.StatusUnauthorized},
		{"unknown key is 403", map[string]string{"x-proxy-key": "nope"}, http.StatusForbidden},
		{"known key reaches the upstream", map[string]string{"x-proxy-key": "tenant-secret"}, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := call(t, base, up.URL, tt.hdr); got != tt.want {
				t.Fatalf("status = %d, want %d", got, tt.want)
			}
		})
	}
	if n := len(up.Requests()); n != 1 {
		t.Fatalf("upstream saw %d calls, want only the authenticated one", n)
	}
}

// TestAuth_TenantKeyNeverReachesUpstream covers PRIV-4: whatever header the
// authenticator reads, the core strips it, even when the host did not mirror
// it into Options.Headers.TenantKey.
func TestAuth_TenantKeyNeverReachesUpstream(t *testing.T) {
	for _, header := range []string{"", "x-team-key", "X-Custom-Credential"} {
		t.Run("header "+header, func(t *testing.T) {
			up := testkit.NewFakeUpstream(t, testkit.Options{})
			name := header
			if name == "" {
				name = tollgate.DefaultHeaderNames().TenantKey
			}
			base := proxyWith(t, header)
			if got := call(t, base, up.URL, map[string]string{name: "tenant-secret", "Authorization": "Bearer up"}); got != http.StatusOK {
				t.Fatalf("status = %d", got)
			}
			reqs := up.Requests()
			if len(reqs) != 1 {
				t.Fatalf("upstream saw %d calls", len(reqs))
			}
			for k, vs := range reqs[0].Header {
				if strings.Contains(strings.Join(vs, ","), "tenant-secret") {
					t.Fatalf("tenant key reached the upstream in header %q", k)
				}
			}
			if reqs[0].Header.Get("Authorization") != "Bearer up" {
				t.Fatal("upstream credential was not forwarded verbatim")
			}
		})
	}
}

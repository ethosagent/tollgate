package forward

import (
	"errors"
	"net/url"
	"testing"
)

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestTarget_UpstreamOverride(t *testing.T) {
	tests := []struct {
		name     string
		allow    []string
		header   string
		wantBase string
		wantErr  error
	}{
		{"no policy ignores header", nil, "http://evil.example", "https://api.openai.com", nil},
		{"no header uses default", []string{"vllm.internal"}, "", "https://api.openai.com", nil},
		{"exact host", []string{"vllm.internal"}, "http://vllm.internal/base", "http://vllm.internal/base", nil},
		{"exact host case-insensitive", []string{"VLLM.internal"}, "https://vllm.INTERNAL", "https://vllm.INTERNAL", nil},
		{"exact host:port", []string{"vllm.internal:8000"}, "http://vllm.internal:8000", "http://vllm.internal:8000", nil},
		{"port mismatch with host:port entry", []string{"vllm.internal:8000"}, "http://vllm.internal:9", "", ErrUpstreamForbidden},
		{"bare host allows any port", []string{"vllm.internal"}, "http://vllm.internal:9", "http://vllm.internal:9", nil},
		{"cidr match", []string{"10.0.0.0/8"}, "http://10.1.2.3:8000", "http://10.1.2.3:8000", nil},
		{"cidr matches ipv4-mapped ipv6", []string{"10.0.0.0/8"}, "http://[::ffff:10.1.2.3]", "http://[::ffff:10.1.2.3]", nil},
		{"ip entry matches ipv4-mapped ipv6", []string{"10.1.2.3"}, "http://[::ffff:10.1.2.3]:80", "http://[::ffff:10.1.2.3]:80", nil},
		{"cidr miss", []string{"10.0.0.0/8"}, "http://11.1.2.3", "", ErrUpstreamForbidden},
		{"cidr does not match hostnames", []string{"10.0.0.0/8"}, "http://vllm.internal", "", ErrUpstreamForbidden},
		{"host mismatch", []string{"vllm.internal"}, "http://evil.example", "", ErrUpstreamForbidden},
		{"suffix is not a match", []string{"internal"}, "http://vllm.internal", "", ErrUpstreamForbidden},
		{"bad scheme", []string{"vllm.internal"}, "file://vllm.internal", "", ErrBadUpstream},
		{"credentials in URL", []string{"vllm.internal"}, "http://u:p@vllm.internal", "", ErrBadUpstream},
		{"garbage", []string{"vllm.internal"}, "::not a url", "", ErrBadUpstream},
	}
	base := mustURL(t, "https://api.openai.com")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Target(base, tt.header, tt.allow, mustURL(t, "/openai"), "")
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if err == nil && got.String() != tt.wantBase {
				t.Fatalf("target = %s, want %s", got, tt.wantBase)
			}
		})
	}
}

func TestTarget_RejectsBadResolverBase(t *testing.T) {
	for _, raw := range []string{"", "file:///etc/passwd", "http://u:p@h", "http:opaque", "https://"} {
		var base *url.URL
		if raw != "" {
			base = mustURL(t, raw)
		}
		if _, err := Target(base, "", nil, mustURL(t, "/x"), "/"); !errors.Is(err, ErrBadTarget) {
			t.Errorf("%q: err = %v, want ErrBadTarget", raw, err)
		}
	}
}

func TestTarget_JoinsPathKeepingEscapes(t *testing.T) {
	tests := []struct{ base, in, rest, want string }{
		{"https://h", "/openai/v1/chat/completions?a=1", "/v1/chat/completions", "https://h/v1/chat/completions?a=1"},
		{"https://h/api/", "/openrouter/v1/a%2Fb%20c?q=a%2Fb", "/v1/a/b c", "https://h/api/v1/a%2Fb%20c?q=a%2Fb"},
		{"https://h/a%2Fb", "/p/x%2Fy", "/x/y", "https://h/a%2Fb/x%2Fy"},
		{"https://h", "/openai", "/", "https://h/"},
		{"https://h#frag", "/p/v1", "/rewritten", "https://h/rewritten"},
		{"https://h/openai?api-version=1", "/p/v1/x", "/v1/x", "https://h/openai/v1/x?api-version=1"},
		{"https://h?api-version=1", "/p/v1/x?a=2", "/v1/x", "https://h/v1/x?api-version=1&a=2"},
	}
	for _, tt := range tests {
		got, err := Target(mustURL(t, tt.base), "", nil, mustURL(t, tt.in), tt.rest)
		if err != nil || got.String() != tt.want {
			t.Errorf("Target(%s, %s, %s) = %v %v, want %s", tt.base, tt.in, tt.rest, got, err, tt.want)
		}
	}
}

func TestTargetErrors_CarryStatus(t *testing.T) {
	for err, want := range map[error]int{ErrUpstreamForbidden: 403, ErrBadUpstream: 400, ErrBadTarget: 502} {
		var se interface{ HTTPStatus() int }
		if !errors.As(err, &se) || se.HTTPStatus() != want {
			t.Errorf("%v: status %v, want %d", err, se, want)
		}
	}
}

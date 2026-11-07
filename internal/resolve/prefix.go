package resolve

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/ethosagent/tollgate/internal/model"
)

// Errors the default resolver returns. The proxy maps them to 404, 403 and
// 400; any other resolver error also becomes a 404.
var (
	// ErrUnknownProvider means no provider matches the path prefix.
	ErrUnknownProvider = errors.New("resolve: unknown provider")
	// ErrUpstreamForbidden means the upstream override is not in the
	// tenant's Policy.AllowedUpstreams.
	ErrUpstreamForbidden = errors.New("resolve: upstream not allowed by policy")
	// ErrBadUpstream means the upstream override is not a usable http(s) URL.
	ErrBadUpstream = errors.New("resolve: invalid upstream URL")
)

// DefaultProviders returns the built-in provider table, keyed by path prefix.
func DefaultProviders() map[string]model.Provider {
	return map[string]model.Provider{
		"openai": {Name: "openai", Wire: model.WireOpenAIChat,
			BaseURL: &url.URL{Scheme: "https", Host: "api.openai.com"}},
		"anthropic": {Name: "anthropic", Wire: model.WireAnthropicMessages,
			BaseURL: &url.URL{Scheme: "https", Host: "api.anthropic.com"}},
		"openrouter": {Name: "openrouter", Wire: model.WireOpenAIChat,
			BaseURL: &url.URL{Scheme: "https", Host: "openrouter.ai", Path: "/api"}},
	}
}

// Resolver is the default model.ProviderResolver: the first path segment
// picks the provider; an optional header overrides its base URL when the
// tenant's policy allows the target (docs/decisions/0006).
type Resolver struct {
	providers map[string]model.Provider
	header    string
}

// New returns a Resolver over providers (nil means DefaultProviders) that
// reads the upstream override from header (empty disables the override).
func New(providers map[string]model.Provider, header string) *Resolver {
	if providers == nil {
		providers = DefaultProviders()
	}
	return &Resolver{providers: providers, header: header}
}

// Resolve implements model.ProviderResolver. restPath is the request path
// after the provider prefix, "/" when there is none.
func (r *Resolver) Resolve(_ context.Context, t model.Tenant, req *http.Request) (model.Provider, string, error) {
	seg, rest, _ := strings.Cut(strings.TrimPrefix(req.URL.Path, "/"), "/")
	p, ok := r.providers[seg]
	if !ok {
		return model.Provider{}, "", ErrUnknownProvider
	}
	rest = "/" + rest
	override := ""
	if r.header != "" {
		override = strings.TrimSpace(req.Header.Get(r.header))
	}
	if override == "" || t.Policy == nil || len(t.Policy.AllowedUpstreams) == 0 {
		return p, rest, nil // no override, or no policy to authorise it: ignored
	}
	u, err := parseUpstream(override)
	if err != nil {
		return model.Provider{}, "", err
	}
	if !allowed(u, t.Policy.AllowedUpstreams) {
		return model.Provider{}, "", ErrUpstreamForbidden
	}
	p.BaseURL = u
	return p, rest, nil
}

func parseUpstream(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return nil, ErrBadUpstream
	}
	return u, nil
}

// allowed reports whether u's host is listed exactly (host or host:port) or,
// for an IP literal, falls inside a listed CIDR. No DNS lookups are made.
func allowed(u *url.URL, list []string) bool {
	host := u.Hostname()
	ip := net.ParseIP(host)
	for _, entry := range list {
		if _, cidr, err := net.ParseCIDR(entry); err == nil {
			if ip != nil && cidr.Contains(ip) {
				return true
			}
			continue
		}
		if strings.EqualFold(entry, host) || strings.EqualFold(entry, u.Host) {
			return true
		}
	}
	return false
}

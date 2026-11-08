package main

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/ethosagent/tollgate"
)

var (
	_ tollgate.ProviderResolver = pathResolver{}
	_ tollgate.InlineSafe       = pathResolver{}
)

// pathResolver is a ProviderResolver over the configured providers table: the
// first path segment names the provider. The core still applies the
// upstream-override header and checks the final target (ADR 0007).
type pathResolver struct{ providers map[string]tollgate.Provider }

// errUnknownProvider carries no status, so the proxy answers 404.
var errUnknownProvider = errors.New("tollgated: unknown provider")

// InlineSafe marks the resolver as a pure map lookup (ADR 0015).
func (pathResolver) InlineSafe() {}

// Resolve implements tollgate.ProviderResolver.
func (r pathResolver) Resolve(_ context.Context, _ tollgate.Tenant, req *http.Request) (tollgate.Provider, string, error) {
	seg, rest, _ := strings.Cut(strings.TrimPrefix(req.URL.Path, "/"), "/")
	p, ok := r.providers[seg]
	if !ok {
		return tollgate.Provider{}, "", errUnknownProvider
	}
	return p, "/" + rest, nil
}

// defaultWire picks a wire format from the provider name when the config
// does not say.
func defaultWire(name string) tollgate.WireFormat {
	switch name {
	case "openai", "openrouter":
		return tollgate.WireOpenAIChat
	case "anthropic":
		return tollgate.WireAnthropicMessages
	}
	return tollgate.WirePassthrough
}

// buildProviders turns the providers section into a resolver. With no
// section it returns nil, and the core keeps its built-in resolver.
func buildProviders(c *collector, cfg map[string]providerConfig, lookup lookupFunc) tollgate.ProviderResolver {
	if len(cfg) == 0 {
		return nil
	}
	table := make(map[string]tollgate.Provider, len(cfg))
	for name, pc := range cfg {
		field := "providers." + name
		if name == "" || strings.Contains(name, "/") {
			c.addf("%s: provider name must be a single non-empty path segment", field)
			continue
		}
		base := parseUpstream(c, field+".upstream", c.expand(field+".upstream", pc.Upstream, lookup))
		wire := tollgate.WireFormat(pc.Wire)
		switch wire {
		case "":
			wire = defaultWire(name)
		case tollgate.WireOpenAIChat, tollgate.WireAnthropicMessages, tollgate.WirePassthrough:
		default:
			c.addf("%s.wire: %q is not one of openai.chat, anthropic.messages, passthrough", field, pc.Wire)
		}
		table[name] = tollgate.Provider{Name: name, Wire: wire, BaseURL: base}
	}
	return pathResolver{providers: table}
}

func parseUpstream(c *collector, field, raw string) *url.URL {
	u, err := url.Parse(raw)
	if raw == "" || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		c.addf("%s: must be an http(s) URL with a host and no credentials", field)
		return nil
	}
	return u
}

package resolve

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/ethosagent/tollgate/internal/model"
)

// ErrUnknownProvider means no provider matches the path prefix. It carries no
// status, so the proxy answers with the resolver default, 404.
var ErrUnknownProvider = errors.New("resolve: unknown provider")

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

// builtin is a read-only copy of DefaultProviders for WireForPath.
var builtin = DefaultProviders()

// Resolver is the default model.ProviderResolver: the first path segment
// picks the provider. The upstream-override header is not handled here; the
// core applies it for every resolver (docs/decisions/0007).
type Resolver struct {
	providers map[string]model.Provider
}

// New returns a Resolver over providers (nil means DefaultProviders).
func New(providers map[string]model.Provider) *Resolver {
	if providers == nil {
		providers = DefaultProviders()
	}
	return &Resolver{providers: providers}
}

// Resolve implements model.ProviderResolver. restPath is the request path
// after the provider prefix, "/" when there is none.
func (r *Resolver) Resolve(_ context.Context, _ model.Tenant, req *http.Request) (model.Provider, string, error) {
	seg, rest := split(req.URL.Path)
	p, ok := r.providers[seg]
	if !ok {
		return model.Provider{}, "", ErrUnknownProvider
	}
	return p, rest, nil
}

// InlineSafe implements model.InlineSafe: Resolve is a map lookup.
func (*Resolver) InlineSafe() {}

// WireForPath guesses the wire format from the path prefix using the
// built-in table, before any resolver has run (to shape early errors).
// Unknown prefixes are WirePassthrough.
func WireForPath(path string) model.WireFormat {
	seg, _ := split(path)
	if p, ok := builtin[seg]; ok {
		return p.Wire
	}
	return model.WirePassthrough
}

// split returns the first path segment and the rest, which starts with "/".
func split(path string) (seg, rest string) {
	seg, rest, _ = strings.Cut(strings.TrimPrefix(path, "/"), "/")
	return seg, "/" + rest
}

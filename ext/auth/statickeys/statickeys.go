package statickeys

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/ethosagent/tollgate"
)

var _ tollgate.Authenticator = (*Auth)(nil)

// Tenant is what a key resolves to.
type Tenant struct {
	ID               string   // required
	AllowedProviders []string // empty means all
	AllowedModels    []string // empty means all
	AllowedUpstreams []string // hosts / CIDRs the upstream-override header may target
}

// Config configures New.
type Config struct {
	// Header is the request header that carries the key. Empty uses
	// tollgate.DefaultHeaderNames().TenantKey; it must match
	// Options.Headers.TenantKey if the host changed that.
	Header string
	// Keys maps each secret key to its tenant. Several keys may share a
	// tenant (rotation).
	Keys map[string]Tenant
}

// Auth is a tollgate.Authenticator over a fixed key table.
type Auth struct {
	header  string
	entries []entry
}

type entry struct {
	digest [sha256.Size]byte
	tenant Tenant
}

// authError is a StatusError that carries no secret material.
type authError struct {
	status int
	msg    string
}

func (e *authError) Error() string   { return e.msg }
func (e *authError) HTTPStatus() int { return e.status }

var (
	errMissing = &authError{http.StatusUnauthorized, "statickeys: missing credential"}
	errUnknown = &authError{http.StatusForbidden, "statickeys: unknown credential"}
)

// New validates cfg once and builds the authenticator. Errors never include
// key values.
func New(cfg Config) (*Auth, error) {
	header := cfg.Header
	if header == "" {
		header = tollgate.DefaultHeaderNames().TenantKey
	}
	if !validHeader(header) {
		return nil, fmt.Errorf("statickeys: invalid header name %q", header)
	}
	if len(cfg.Keys) == 0 {
		return nil, errors.New("statickeys: no keys configured")
	}
	entries := make([]entry, 0, len(cfg.Keys))
	for key, t := range cfg.Keys {
		if key == "" {
			return nil, errors.New("statickeys: empty key")
		}
		if t.ID == "" {
			return nil, errors.New("statickeys: empty tenant ID")
		}
		entries = append(entries, entry{digest: sha256.Sum256([]byte(key)), tenant: t})
	}
	return &Auth{header: header, entries: entries}, nil
}

func validHeader(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_'
		if !ok {
			return false
		}
	}
	return true
}

// Authenticate resolves the caller from the key header. A missing key is a
// 401 and an unknown key a 403 (tollgate.StatusError).
func (a *Auth) Authenticate(_ context.Context, r *http.Request) (tollgate.Tenant, error) {
	key := r.Header.Get(a.header)
	if key == "" {
		return tollgate.Tenant{}, errMissing
	}
	// Hash first so every comparison is over equal-length digests, then walk
	// every entry without exiting early so timing does not reveal which
	// key matched.
	sum := sha256.Sum256([]byte(key))
	found := -1
	for i := range a.entries {
		if subtle.ConstantTimeCompare(sum[:], a.entries[i].digest[:]) == 1 {
			found = i
		}
	}
	if found < 0 {
		return tollgate.Tenant{}, errUnknown
	}
	return toTenant(a.entries[found].tenant), nil
}

func toTenant(t Tenant) tollgate.Tenant {
	out := tollgate.Tenant{ID: t.ID}
	if len(t.AllowedProviders)+len(t.AllowedModels)+len(t.AllowedUpstreams) > 0 {
		out.Policy = &tollgate.Policy{
			AllowedProviders: slices.Clone(t.AllowedProviders),
			AllowedModels:    slices.Clone(t.AllowedModels),
			AllowedUpstreams: slices.Clone(t.AllowedUpstreams),
		}
	}
	return out
}

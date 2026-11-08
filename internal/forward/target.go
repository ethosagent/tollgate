package forward

import (
	"net/netip"
	"net/url"
	"strings"
)

// statusError is a sentinel that carries the HTTP status the proxy answers
// with (model.StatusError).
type statusError struct {
	msg    string
	status int
}

func (e *statusError) Error() string   { return e.msg }
func (e *statusError) HTTPStatus() int { return e.status }

// Target errors. Each carries its HTTP status.
var (
	// ErrUpstreamForbidden (403): the upstream override is not in the
	// tenant's Policy.AllowedUpstreams.
	ErrUpstreamForbidden error = &statusError{"forward: upstream not allowed by policy", 403}
	// ErrBadUpstream (400): the upstream override is not a usable http(s) URL.
	ErrBadUpstream error = &statusError{"forward: invalid upstream override", 400}
	// ErrBadTarget (502): the ProviderResolver returned no base URL or one
	// that is not a plain http(s) URL. It is a host misconfiguration.
	ErrBadTarget error = &statusError{"forward: invalid upstream target", 502}
)

// Target returns the URL to dial for one call and is the single check every
// upstream target passes, whichever ProviderResolver produced base
// (docs/decisions/0007). The target is base, or the caller's override when
// allow (the tenant's Policy.AllowedUpstreams) permits it; an override with
// an empty allow list is ignored. Either way it must be http or https with a
// host and no userinfo. in is the inbound URL: its query and the escaped
// form of rest (the path after the provider prefix) are kept byte for byte.
func Target(base *url.URL, override string, allow []string, in *url.URL, rest string) (*url.URL, error) {
	if !plainHTTP(base) {
		return nil, ErrBadTarget
	}
	if override != "" && len(allow) > 0 {
		u, err := url.Parse(override)
		if err != nil || !plainHTTP(u) {
			return nil, ErrBadUpstream
		}
		if !allowed(u, allow) {
			return nil, ErrUpstreamForbidden
		}
		base = u
	}
	return join(base, in, rest), nil
}

func plainHTTP(u *url.URL) bool {
	return u != nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil && u.Opaque == ""
}

// join appends rest to base's path, preserving the inbound percent-encoding
// (a%2Fb stays a%2Fb). The query is base's (e.g. an api-version a provider
// needs) followed by the inbound one.
func join(base, in *url.URL, rest string) *url.URL {
	escRest := escapedSuffix(in.EscapedPath(), rest)
	u := *base
	u.RawPath = strings.TrimSuffix(base.EscapedPath(), "/") + escRest
	u.Path = strings.TrimSuffix(base.Path, "/") + rest
	switch {
	case base.RawQuery == "":
		u.RawQuery = in.RawQuery
	case in.RawQuery != "":
		u.RawQuery = base.RawQuery + "&" + in.RawQuery
	}
	u.Fragment, u.RawFragment = "", ""
	return &u
}

// escapedSuffix returns the suffix of escaped that decodes to rest, or the
// default encoding of rest when none does (a resolver that rewrote the path).
func escapedSuffix(escaped, rest string) string {
	for i := len(escaped) - 1; i >= 0; i-- {
		if escaped[i] != '/' {
			continue
		}
		if s, err := url.PathUnescape(escaped[i:]); err == nil && s == rest {
			return escaped[i:]
		}
	}
	return (&url.URL{Path: rest}).EscapedPath()
}

// allowed reports whether u's host is listed exactly (host or host:port) or,
// for an IP literal, equals a listed IP or falls inside a listed CIDR
// (IPv4-mapped IPv6 is unmapped first). No DNS lookups are made.
func allowed(u *url.URL, list []string) bool {
	host := u.Hostname()
	ip, ipErr := netip.ParseAddr(host)
	ip = ip.Unmap()
	for _, entry := range list {
		if p, err := netip.ParsePrefix(entry); err == nil {
			if ipErr == nil && p.Masked().Contains(ip) {
				return true
			}
			continue
		}
		if a, err := netip.ParseAddr(entry); err == nil && ipErr == nil && a.Unmap() == ip {
			return true
		}
		if strings.EqualFold(entry, host) || strings.EqualFold(entry, u.Host) {
			return true
		}
	}
	return false
}

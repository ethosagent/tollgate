package forward

import (
	"net/http"
	"net/textproto"
	"slices"
	"strings"
)

// tollgate relays by hand instead of using httputil.ReverseProxy: the relay
// must be byte-exact with a flush after every read, and the client-first tee
// (ADR 0004) needs the read loop.

// hopByHop are the connection-scoped headers a proxy must not forward
// (RFC 9110 section 7.6.1), plus the legacy Proxy-Connection.
var hopByHop = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

// HeaderFilter copies headers without hop-by-hop headers (including any
// header named in Connection) and without a fixed set of extra names.
// Build it once; it is safe for concurrent use.
type HeaderFilter struct {
	drop map[string]struct{} // canonical names
}

// NewHeaderFilter returns a filter that drops hop-by-hop headers and own,
// which are tollgate's own headers and must not reach the upstream.
func NewHeaderFilter(own ...string) HeaderFilter {
	drop := make(map[string]struct{}, len(hopByHop)+len(own))
	for _, name := range slices.Concat(hopByHop, own) {
		drop[textproto.CanonicalMIMEHeaderKey(name)] = struct{}{}
	}
	return HeaderFilter{drop: drop}
}

// CopyTo adds every header of src that the filter keeps to dst, in one pass.
// Credentials such as Authorization and x-api-key are kept verbatim. Value
// slices are copied, so src is never aliased or modified.
func (f HeaderFilter) CopyTo(dst, src http.Header) {
	var named map[string]struct{}
	for _, v := range src.Values("Connection") {
		for name := range strings.SplitSeq(v, ",") {
			if name = strings.TrimSpace(name); name != "" {
				if named == nil {
					named = map[string]struct{}{}
				}
				named[textproto.CanonicalMIMEHeaderKey(name)] = struct{}{}
			}
		}
	}
	for k, vs := range src {
		ck := textproto.CanonicalMIMEHeaderKey(k)
		if _, ok := f.drop[ck]; ok {
			continue
		}
		if _, ok := named[ck]; ok {
			continue
		}
		dst[k] = append(dst[k], vs...)
	}
}

// Filter returns a new header set holding what CopyTo would copy from src.
func (f HeaderFilter) Filter(src http.Header) http.Header {
	dst := make(http.Header, len(src))
	f.CopyTo(dst, src)
	return dst
}

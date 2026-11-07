package forward

import (
	"net/http"
	"strings"
)

// hopByHop are the connection-scoped headers a proxy must not forward
// (RFC 9110 section 7.6.1), plus the legacy Proxy-Connection.
var hopByHop = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

// FilterHeaders returns a copy of src without hop-by-hop headers (including
// any header named in Connection) and without the headers in own, which are
// tollgate's own and must not reach the upstream. Credentials such as
// Authorization and x-api-key are kept verbatim. src is not modified.
func FilterHeaders(src http.Header, own ...string) http.Header {
	dst := src.Clone()
	if dst == nil {
		return http.Header{}
	}
	for _, v := range src.Values("Connection") {
		for _, name := range strings.Split(v, ",") {
			if name = strings.TrimSpace(name); name != "" {
				dst.Del(name)
			}
		}
	}
	for _, name := range hopByHop {
		dst.Del(name)
	}
	for _, name := range own {
		dst.Del(name)
	}
	return dst
}

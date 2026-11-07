package main

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// nullProxy is the control for -null-proxy: the least a Go reverse proxy can
// do on this rig (one keep-alive transport, headers copied, every read
// written and flushed) with none of tollgate's pipeline. tollgate's own cost
// is its figure minus this one; the rest is the extra loopback hop and
// scheduling that any proxy pays.
type nullProxy struct {
	rt *http.Transport
}

func newNullProxy(sessions int) *nullProxy {
	return &nullProxy{rt: &http.Transport{MaxConnsPerHost: 2 * sessions, MaxIdleConnsPerHost: sessions, DisableCompression: true}}
}

func (n *nullProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	base, err := url.Parse(r.Header.Get("x-upstream-url"))
	if err != nil || base.Host == "" {
		http.Error(w, "null proxy: no upstream", http.StatusBadGateway)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "null proxy: bad body", http.StatusBadRequest)
		return
	}
	_, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/") // drop the provider prefix
	out, err := http.NewRequestWithContext(r.Context(), r.Method, base.String()+"/"+rest, bytes.NewReader(body))
	if err != nil {
		http.Error(w, "null proxy: bad request", http.StatusBadGateway)
		return
	}
	out.Header = r.Header.Clone()
	resp, err := n.rt.RoundTrip(out)
	if err != nil {
		http.Error(w, "null proxy: upstream", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		w.Header()[k] = vs
	}
	w.WriteHeader(resp.StatusCode)
	n.copy(w, resp.Body)
}

// copy writes and flushes every read until EOF or a failed write.
func (n *nullProxy) copy(w http.ResponseWriter, src io.Reader) {
	rc := http.NewResponseController(w)
	buf := make([]byte, 32<<10)
	for {
		k, err := src.Read(buf)
		if k > 0 {
			if _, werr := w.Write(buf[:k]); werr != nil || rc.Flush() != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

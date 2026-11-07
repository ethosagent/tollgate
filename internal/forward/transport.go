package forward

import (
	"net"
	"net/http"
	"time"
)

// idleConnsPerHostFactor sizes the total idle pool relative to the per-host
// limit: room for this many busy upstream hosts before idle conns are evicted.
const idleConnsPerHostFactor = 8

// TransportConfig sizes the default upstream connection pool.
type TransportConfig struct {
	DialTimeout         time.Duration
	FirstByteTimeout    time.Duration
	MaxConnsPerHost     int
	MaxIdleConnsPerHost int
	IdleConnTimeout     time.Duration
}

// NewTransport returns the default pooled keep-alive transport with HTTP/2
// enabled. Compression is disabled so the transport never adds
// Accept-Encoding and never decodes a body: the client's own Accept-Encoding
// passes through and the upstream bytes are relayed unchanged. It honours HTTP(S)_PROXY like http.DefaultTransport, so a host can
// send egress through a policy proxy without custom code.
func NewTransport(cfg TransportConfig) *http.Transport {
	dialer := &net.Dialer{Timeout: cfg.DialTimeout, KeepAlive: 30 * time.Second}
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		DisableCompression:    true,
		MaxConnsPerHost:       cfg.MaxConnsPerHost,
		MaxIdleConns:          cfg.MaxIdleConnsPerHost * idleConnsPerHostFactor,
		MaxIdleConnsPerHost:   cfg.MaxIdleConnsPerHost,
		IdleConnTimeout:       cfg.IdleConnTimeout,
		TLSHandshakeTimeout:   cfg.DialTimeout,
		ResponseHeaderTimeout: cfg.FirstByteTimeout,
		ExpectContinueTimeout: time.Second,
	}
}
